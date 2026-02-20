package codemap

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildFileIndexIncludesShellByDefault(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tmpDir, "scripts"), 0755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "scripts", "main.sh"), []byte("#!/bin/sh\nmain() { :; }\n"), 0644); err != nil {
		t.Fatalf("write scripts/main.sh: %v", err)
	}

	idx, err := BuildFileIndex(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("BuildFileIndex returned error: %v", err)
	}
	if len(idx.Files) != 1 {
		t.Fatalf("expected 1 indexed file, got %d", len(idx.Files))
	}
	if idx.Files[0].Language != languageShell {
		t.Fatalf("expected shell language, got %q", idx.Files[0].Language)
	}
}

func TestBuildFileIndexDetectsShellShebangWithoutExtension(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tmpDir, "scripts"), 0755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	path := filepath.Join(tmpDir, "scripts", "deploy")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nmain() { :; }\n"), 0755); err != nil {
		t.Fatalf("write scripts/deploy: %v", err)
	}

	idx, err := BuildFileIndex(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("BuildFileIndex returned error: %v", err)
	}
	if len(idx.Files) != 1 {
		t.Fatalf("expected 1 indexed file, got %d", len(idx.Files))
	}
	if idx.Files[0].Language != languageShell {
		t.Fatalf("expected shell language for shebang script, got %q", idx.Files[0].Language)
	}
	if idx.Files[0].RelPath != "scripts/deploy" {
		t.Fatalf("expected scripts/deploy rel path, got %q", idx.Files[0].RelPath)
	}
}

func TestAnalyzeShellProject(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tmpDir, "scripts"), 0755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}

	mainSrc := `#!/bin/sh
# App bootstrap script.
source ./lib.sh

main() {
  run_app
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "scripts", "main.sh"), []byte(mainSrc), 0755); err != nil {
		t.Fatalf("write scripts/main.sh: %v", err)
	}
	libSrc := `run_app() {
  echo "ok"
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "scripts", "lib.sh"), []byte(libSrc), 0644); err != nil {
		t.Fatalf("write scripts/lib.sh: %v", err)
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
	if pkg.RelativePath != "." {
		t.Fatalf("expected root relative path '.', got %q", pkg.RelativePath)
	}
	if pkg.EntryPoint != "scripts/main.sh" {
		t.Fatalf("expected scripts/main.sh entrypoint, got %q", pkg.EntryPoint)
	}
	if pkg.FileCount != 2 {
		t.Fatalf("expected 2 files, got %d", pkg.FileCount)
	}
	if !strings.Contains(pkg.Purpose, "App bootstrap script") {
		t.Fatalf("expected purpose from shell comment, got %q", pkg.Purpose)
	}
	if !reflect.DeepEqual(pkg.Imports, []string{"./lib.sh"}) {
		t.Fatalf("unexpected imports: %v", pkg.Imports)
	}

	paths := RenderPaths(&Codemap{
		ContentHash: "abc123",
		Packages:    []Package{pkg},
	})
	if !strings.Contains(paths, ".\tscripts/main.sh") {
		t.Fatalf("expected CODEMAP.paths root entry, got:\n%s", paths)
	}
}

func TestAnalyzeShellProjectIncludeTestsFlag(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tmpDir, "scripts"), 0755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "tests"), 0755); err != nil {
		t.Fatalf("mkdir tests: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "scripts", "main.sh"), []byte("main() { :; }\n"), 0644); err != nil {
		t.Fatalf("write scripts/main.sh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "tests", "test_main.sh"), []byte("test_main() { :; }\n"), 0644); err != nil {
		t.Fatalf("write tests/test_main.sh: %v", err)
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
	totalFiles := 0
	for _, pkg := range cm.Packages {
		totalFiles += pkg.FileCount
	}
	if totalFiles != 2 {
		t.Fatalf("expected test files included across packages, got total file count %d", totalFiles)
	}
}

func TestIsShellTestPathHeuristics(t *testing.T) {
	cases := []struct {
		name          string
		path          string
		fileMatchTest bool
		want          bool
	}{
		{name: "suffix matched by index", path: "scripts/main_test.sh", fileMatchTest: true, want: true},
		{name: "tests dir", path: "tests/main.sh", want: true},
		{name: "test prefix", path: "scripts/test_main.sh", want: true},
		{name: "bats", path: "tests/cli.bats", want: true},
		{name: "non-test", path: "scripts/main.sh", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isShellTestPath(tc.path, tc.fileMatchTest); got != tc.want {
				t.Fatalf("isShellTestPath(%q, %v) = %v, want %v", tc.path, tc.fileMatchTest, got, tc.want)
			}
		})
	}
}

func TestParseShellFileSymbolsExtractsFunctionsAndSources(t *testing.T) {
	content := []byte(`
#!/usr/bin/env bash
source ./lib.sh
. ../shared/common.sh
function run() {
  echo "run"
}
main() {
  run
}
`)

	keyFuncs, imports := parseShellFileSymbols(content)
	if !reflect.DeepEqual(keyFuncs, []string{"run", "main"}) {
		t.Fatalf("unexpected key funcs: %v", keyFuncs)
	}
	if !reflect.DeepEqual(imports, []string{"./lib.sh", "../shared/common.sh"}) {
		t.Fatalf("unexpected imports: %v", imports)
	}
}

func TestScoreShellEntryPointHeuristics(t *testing.T) {
	mainScore := scoreShellEntryPoint("scripts/main.sh", []string{"main"})
	binScore := scoreShellEntryPoint("bin/worker.sh", nil)
	moduleScore := scoreShellEntryPoint("scripts/lib.sh", nil)
	miscScore := scoreShellEntryPoint("lib/helpers.sh", nil)

	if !(mainScore > binScore && binScore > moduleScore && moduleScore > miscScore) {
		t.Fatalf("unexpected score ordering: main=%d bin=%d module=%d misc=%d", mainScore, binScore, moduleScore, miscScore)
	}
}

func TestAnalyzeShellWithIndexSkipsBrokenPackageAndKeepsHealthyOnes(t *testing.T) {
	tmpDir := t.TempDir()

	healthyDir := filepath.Join(tmpDir, "scripts")
	if err := os.MkdirAll(healthyDir, 0755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(healthyDir, "main.sh"), []byte("main() { :; }\n"), 0644); err != nil {
		t.Fatalf("write healthy main.sh: %v", err)
	}

	brokenMissing := filepath.Join(tmpDir, "tools", "missing.sh")
	idx := &FileIndex{
		Root: tmpDir,
		Files: []FileRecord{
			{
				AbsPath:  filepath.Join(healthyDir, "main.sh"),
				RelPath:  "scripts/main.sh",
				Language: languageShell,
			},
			{
				AbsPath:  brokenMissing,
				RelPath:  "tools/missing.sh",
				Language: languageShell,
			},
		},
	}

	opts := DefaultOptions()
	opts.ProjectRoot = tmpDir
	cm, err := analyzeShellWithIndex(context.Background(), tmpDir, idx, opts, nil, nil)
	if err != nil {
		t.Fatalf("analyzeShellWithIndex returned error: %v", err)
	}
	if len(cm.Packages) != 1 {
		t.Fatalf("expected one healthy package, got %d", len(cm.Packages))
	}
	if cm.Packages[0].EntryPoint != "scripts/main.sh" {
		t.Fatalf("expected healthy package to remain, got %+v", cm.Packages[0])
	}
}
