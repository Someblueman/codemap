package codemap

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildFileIndexIncludesPythonByDefault(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tmpDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "src", "main.py"), []byte("def main():\n    return 1\n"), 0644); err != nil {
		t.Fatalf("write src/main.py: %v", err)
	}

	idx, err := BuildFileIndex(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("BuildFileIndex returned error: %v", err)
	}
	if len(idx.Files) != 1 {
		t.Fatalf("expected 1 indexed file, got %d", len(idx.Files))
	}
	if idx.Files[0].Language != languagePython {
		t.Fatalf("expected python language, got %q", idx.Files[0].Language)
	}
}

func TestAnalyzePythonProject(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "pyproject.toml"), []byte("[project]\nname = \"py-app\"\nversion = \"0.1.0\"\n"), 0644); err != nil {
		t.Fatalf("write pyproject.toml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	mainSrc := `"""Python app entry."""
from .core import Service
from py_app.bootstrap import run

class App:
    pass

def main():
    return Service()
`
	if err := os.WriteFile(filepath.Join(tmpDir, "src", "main.py"), []byte(mainSrc), 0644); err != nil {
		t.Fatalf("write src/main.py: %v", err)
	}
	coreSrc := `class Service:
    pass
`
	if err := os.WriteFile(filepath.Join(tmpDir, "src", "core.py"), []byte(coreSrc), 0644); err != nil {
		t.Fatalf("write src/core.py: %v", err)
	}

	opts := DefaultOptions()
	opts.ProjectRoot = tmpDir
	opts.LargePackageFiles = 1

	cm, err := Analyze(context.Background(), opts)
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if len(cm.Packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(cm.Packages))
	}

	pkg := cm.Packages[0]
	if pkg.ImportPath != "py-app" {
		t.Fatalf("expected package name py-app, got %q", pkg.ImportPath)
	}
	if pkg.RelativePath != "." {
		t.Fatalf("expected root relative path '.', got %q", pkg.RelativePath)
	}
	if pkg.EntryPoint != "src/main.py" {
		t.Fatalf("expected src/main.py entrypoint, got %q", pkg.EntryPoint)
	}
	if pkg.FileCount != 2 {
		t.Fatalf("expected 2 files, got %d", pkg.FileCount)
	}
	if !strings.Contains(pkg.Purpose, "Python app entry") {
		t.Fatalf("expected purpose from module docstring, got %q", pkg.Purpose)
	}
	if !reflect.DeepEqual(pkg.Imports, []string{".core", "py_app.bootstrap"}) {
		t.Fatalf("unexpected imports: %v", pkg.Imports)
	}

	paths := RenderPaths(&Codemap{
		ContentHash: "abc123",
		Packages:    []Package{pkg},
	})
	if !strings.Contains(paths, ".\tsrc/main.py") {
		t.Fatalf("expected CODEMAP.paths root entry, got:\n%s", paths)
	}
}

func TestAnalyzePythonProjectIncludeTestsFlag(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "pyproject.toml"), []byte("[project]\nname = \"py-tests\"\n"), 0644); err != nil {
		t.Fatalf("write pyproject.toml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "tests"), 0755); err != nil {
		t.Fatalf("mkdir tests: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "src", "app.py"), []byte("def run():\n    return 1\n"), 0644); err != nil {
		t.Fatalf("write src/app.py: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "tests", "test_app.py"), []byte("def test_run():\n    assert True\n"), 0644); err != nil {
		t.Fatalf("write tests/test_app.py: %v", err)
	}

	opts := DefaultOptions()
	opts.ProjectRoot = tmpDir
	opts.IncludeTests = false

	cm, err := Analyze(context.Background(), opts)
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if len(cm.Packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(cm.Packages))
	}
	if got := cm.Packages[0].FileCount; got != 1 {
		t.Fatalf("expected test files excluded, got file count %d", got)
	}

	opts.IncludeTests = true
	cm, err = Analyze(context.Background(), opts)
	if err != nil {
		t.Fatalf("Analyze with tests returned error: %v", err)
	}
	if got := cm.Packages[0].FileCount; got != 2 {
		t.Fatalf("expected test files included, got file count %d", got)
	}
}

func TestFindPythonPackageRootPrefersNearestPyproject(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "pyproject.toml"), []byte("[project]\nname = \"root\"\n"), 0644); err != nil {
		t.Fatalf("write root pyproject.toml: %v", err)
	}
	pkgDir := filepath.Join(tmpDir, "packages", "worker")
	if err := os.MkdirAll(filepath.Join(pkgDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir package src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "pyproject.toml"), []byte("[project]\nname = \"worker\"\n"), 0644); err != nil {
		t.Fatalf("write nested pyproject.toml: %v", err)
	}

	filePath := filepath.Join(pkgDir, "src", "main.py")
	if err := os.WriteFile(filePath, []byte("def main():\n    return 1\n"), 0644); err != nil {
		t.Fatalf("write main.py: %v", err)
	}

	pkgRel, pkgAbs, err := findPythonPackageRoot(tmpDir, filePath)
	if err != nil {
		t.Fatalf("findPythonPackageRoot returned error: %v", err)
	}
	if pkgRel != "packages/worker" {
		t.Fatalf("expected package root packages/worker, got %q", pkgRel)
	}
	if pkgAbs != pkgDir {
		t.Fatalf("expected package abs path %q, got %q", pkgDir, pkgAbs)
	}
}

func TestFindPythonPackageRootFallsBackToRepoRootWithoutManifest(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "src", "main.py")
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filePath, []byte("def main():\n    return 1\n"), 0644); err != nil {
		t.Fatalf("write main.py: %v", err)
	}

	pkgRel, pkgAbs, err := findPythonPackageRoot(tmpDir, filePath)
	if err != nil {
		t.Fatalf("findPythonPackageRoot returned error: %v", err)
	}
	if pkgRel != "." {
		t.Fatalf("expected fallback package root '.', got %q", pkgRel)
	}
	if pkgAbs != tmpDir {
		t.Fatalf("expected fallback package abs path %q, got %q", tmpDir, pkgAbs)
	}
}

func TestReadPythonPackageNameFallsBackWhenManifestIsMalformed(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "pyproject.toml"), []byte("[project]\nname =\n"), 0644); err != nil {
		t.Fatalf("write malformed pyproject.toml: %v", err)
	}

	if got := readPythonPackageName(tmpDir, "services/api"); got != "api" {
		t.Fatalf("expected fallback package name api, got %q", got)
	}
}

func TestIsPythonTestPathHeuristics(t *testing.T) {
	cases := []struct {
		name          string
		path          string
		fileMatchTest bool
		want          bool
	}{
		{name: "suffix matched by index", path: "src/service_test.py", fileMatchTest: true, want: true},
		{name: "tests dir", path: "tests/test_service.py", want: true},
		{name: "test prefix", path: "src/test_service.py", want: true},
		{name: "non-test", path: "src/service.py", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPythonTestPath(tc.path, tc.fileMatchTest); got != tc.want {
				t.Fatalf("isPythonTestPath(%q, %v) = %v, want %v", tc.path, tc.fileMatchTest, got, tc.want)
			}
		})
	}
}

func TestParsePythonFileSymbolsExtractsTypesFuncsAndImports(t *testing.T) {
	content := []byte(`
import os
import internal.helpers as helpers
from .core import Service
from app.config import Settings

class Service:
    pass

class _Private:
    pass

async def run():
    return 1

def start():
    return 2

def _helper():
    return 3

APP_VERSION = "1.0.0"
`)

	types, keyTypes, keyFuncs, imports := parsePythonFileSymbols(content, "src/main.py")

	wantTypes := []string{"Service"}
	if !reflect.DeepEqual(keyTypes, wantTypes) {
		t.Fatalf("unexpected key types: got %v want %v", keyTypes, wantTypes)
	}
	if !reflect.DeepEqual(keyFuncs, []string{"run", "start", "APP_VERSION"}) {
		t.Fatalf("unexpected key funcs: %v", keyFuncs)
	}
	if !reflect.DeepEqual(imports, []string{"os", "internal.helpers", ".core", "app.config"}) {
		t.Fatalf("unexpected imports: %v", imports)
	}
	if len(types) != 1 {
		t.Fatalf("expected 1 type info, got %d", len(types))
	}
}

func TestScorePythonEntryPointHeuristics(t *testing.T) {
	mainScore := scorePythonEntryPoint("src/main.py", nil, []string{"main"})
	cliScore := scorePythonEntryPoint("src/cli.py", nil, nil)
	initScore := scorePythonEntryPoint("pkg/__init__.py", nil, nil)
	moduleScore := scorePythonEntryPoint("pkg/module.py", nil, nil)

	if !(mainScore > cliScore && cliScore > initScore && initScore > moduleScore) {
		t.Fatalf("unexpected score ordering: main=%d cli=%d init=%d module=%d", mainScore, cliScore, initScore, moduleScore)
	}
}

func TestAnalyzePythonWithIndexSkipsBrokenPackageAndKeepsHealthyOnes(t *testing.T) {
	tmpDir := t.TempDir()

	healthyDir := filepath.Join(tmpDir, "services", "healthy")
	if err := os.MkdirAll(filepath.Join(healthyDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir healthy package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(healthyDir, "pyproject.toml"), []byte("[project]\nname = \"healthy\"\n"), 0644); err != nil {
		t.Fatalf("write healthy pyproject.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(healthyDir, "src", "main.py"), []byte("def run():\n    return 1\n"), 0644); err != nil {
		t.Fatalf("write healthy main.py: %v", err)
	}

	brokenDir := filepath.Join(tmpDir, "services", "broken")
	if err := os.MkdirAll(filepath.Join(brokenDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir broken package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "pyproject.toml"), []byte("[project]\nname = \"broken\"\n"), 0644); err != nil {
		t.Fatalf("write broken pyproject.toml: %v", err)
	}

	brokenMissing := filepath.Join(brokenDir, "src", "main.py")
	idx := &FileIndex{
		Root: tmpDir,
		Files: []FileRecord{
			{
				AbsPath:  filepath.Join(healthyDir, "src", "main.py"),
				RelPath:  "services/healthy/src/main.py",
				Language: languagePython,
			},
			{
				AbsPath:  brokenMissing,
				RelPath:  "services/broken/src/main.py",
				Language: languagePython,
			},
		},
	}

	opts := DefaultOptions()
	opts.ProjectRoot = tmpDir
	cm, err := analyzePythonWithIndex(context.Background(), tmpDir, idx, opts, nil, nil)
	if err != nil {
		t.Fatalf("analyzePythonWithIndex returned error: %v", err)
	}
	if len(cm.Packages) != 1 {
		t.Fatalf("expected one healthy package, got %d", len(cm.Packages))
	}
	if cm.Packages[0].ImportPath != "healthy" {
		t.Fatalf("expected healthy package to remain, got %q", cm.Packages[0].ImportPath)
	}
}
