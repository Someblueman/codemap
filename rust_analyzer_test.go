package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildFileIndexIncludesRustByDefault(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tmpDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "src", "main.rs"), []byte("fn main() {}\n"), 0644); err != nil {
		t.Fatalf("write main.rs: %v", err)
	}

	idx, err := BuildFileIndex(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("BuildFileIndex returned error: %v", err)
	}
	if len(idx.Files) != 1 {
		t.Fatalf("expected 1 indexed file, got %d", len(idx.Files))
	}
	if idx.Files[0].Language != languageRust {
		t.Fatalf("expected rust language, got %q", idx.Files[0].Language)
	}
}

func TestAnalyzeRustProject(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "Cargo.toml"), []byte("[package]\nname = \"rust-app\"\nversion = \"0.1.0\"\n"), 0644); err != nil {
		t.Fatalf("write Cargo.toml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	mainSrc := `//! Rust app binary.
use crate::core::Service;
pub fn main() {}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "src", "main.rs"), []byte(mainSrc), 0644); err != nil {
		t.Fatalf("write src/main.rs: %v", err)
	}
	libSrc := `pub struct Service {}
pub fn build_service() -> Service { Service {} }
`
	if err := os.WriteFile(filepath.Join(tmpDir, "src", "lib.rs"), []byte(libSrc), 0644); err != nil {
		t.Fatalf("write src/lib.rs: %v", err)
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
	if pkg.ImportPath != "rust-app" {
		t.Fatalf("expected crate name rust-app, got %q", pkg.ImportPath)
	}
	if pkg.RelativePath != "." {
		t.Fatalf("expected root relative path '.', got %q", pkg.RelativePath)
	}
	if pkg.EntryPoint != "src/main.rs" {
		t.Fatalf("expected src/main.rs entrypoint, got %q", pkg.EntryPoint)
	}
	if pkg.FileCount != 2 {
		t.Fatalf("expected 2 files, got %d", pkg.FileCount)
	}
	if !strings.Contains(pkg.Purpose, "Rust app binary") {
		t.Fatalf("expected purpose from rust doc comment, got %q", pkg.Purpose)
	}

	paths := RenderPaths(&Codemap{
		ContentHash: "abc123",
		Packages:    []Package{pkg},
	})
	if !strings.Contains(paths, ".\tsrc/main.rs") {
		t.Fatalf("expected CODEMAP.paths root entry, got:\n%s", paths)
	}
}

func TestAnalyzeRustProjectIncludeTestsFlag(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "Cargo.toml"), []byte("[package]\nname = \"rust-tests\"\nversion = \"0.1.0\"\n"), 0644); err != nil {
		t.Fatalf("write Cargo.toml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "tests"), 0755); err != nil {
		t.Fatalf("mkdir tests: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "src", "lib.rs"), []byte("pub fn run() {}\n"), 0644); err != nil {
		t.Fatalf("write src/lib.rs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "tests", "integration_test.rs"), []byte("pub fn smoke() {}\n"), 0644); err != nil {
		t.Fatalf("write tests/integration_test.rs: %v", err)
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

func TestFindRustCrateRootPrefersNearestCargoToml(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "Cargo.toml"), []byte("[package]\nname = \"root\"\n"), 0644); err != nil {
		t.Fatalf("write root Cargo.toml: %v", err)
	}
	crateDir := filepath.Join(tmpDir, "crates", "worker")
	if err := os.MkdirAll(filepath.Join(crateDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir crate src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(crateDir, "Cargo.toml"), []byte("[package]\nname = \"worker\"\n"), 0644); err != nil {
		t.Fatalf("write nested Cargo.toml: %v", err)
	}

	filePath := filepath.Join(crateDir, "src", "main.rs")
	if err := os.WriteFile(filePath, []byte("pub fn main() {}\n"), 0644); err != nil {
		t.Fatalf("write main.rs: %v", err)
	}

	crateRel, crateAbs, err := findRustCrateRoot(tmpDir, filePath)
	if err != nil {
		t.Fatalf("findRustCrateRoot returned error: %v", err)
	}
	if crateRel != "crates/worker" {
		t.Fatalf("expected crate root crates/worker, got %q", crateRel)
	}
	if crateAbs != crateDir {
		t.Fatalf("expected crate abs path %q, got %q", crateDir, crateAbs)
	}
}

func TestFindRustCrateRootFallsBackToRepoRootWithoutManifest(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "src", "lib.rs")
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filePath, []byte("pub fn run() {}\n"), 0644); err != nil {
		t.Fatalf("write lib.rs: %v", err)
	}

	crateRel, crateAbs, err := findRustCrateRoot(tmpDir, filePath)
	if err != nil {
		t.Fatalf("findRustCrateRoot returned error: %v", err)
	}
	if crateRel != "." {
		t.Fatalf("expected fallback crate root '.', got %q", crateRel)
	}
	if crateAbs != tmpDir {
		t.Fatalf("expected fallback crate abs path %q, got %q", tmpDir, crateAbs)
	}
}

func TestReadRustCrateNameFallsBackWhenManifestIsMalformed(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "Cargo.toml"), []byte("[package]\nname =\n"), 0644); err != nil {
		t.Fatalf("write malformed Cargo.toml: %v", err)
	}

	if got := readRustCrateName(tmpDir, "crates/api"); got != "api" {
		t.Fatalf("expected fallback crate name api, got %q", got)
	}
}

func TestIsRustTestPathHeuristics(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{path: "tests/integration.rs", want: true},
		{path: "src/foo_test.rs", want: true},
		{path: "src/foo.test.rs", want: true},
		{path: "src/foo.spec.rs", want: true},
		{path: "src/lib.rs", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isRustTestPath(tc.path); got != tc.want {
				t.Fatalf("isRustTestPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestParseRustFileSymbolsExtractsTypesFuncsAndImports(t *testing.T) {
	content := []byte(`
pub struct Service {}
pub enum Mode { Fast }
pub trait Runner {}
pub type AppResult = Result<(), String>;
pub async fn run() {}
use crate::core::Service;
use super::helpers::Tool;
`)

	types, keyTypes, keyFuncs, imports := parseRustFileSymbols(content)

	wantTypes := []string{"Service", "Mode", "Runner", "AppResult"}
	if !reflect.DeepEqual(keyTypes, wantTypes) {
		t.Fatalf("unexpected key types: got %v want %v", keyTypes, wantTypes)
	}
	if !reflect.DeepEqual(keyFuncs, []string{"run"}) {
		t.Fatalf("unexpected key funcs: %v", keyFuncs)
	}
	if !reflect.DeepEqual(imports, []string{"crate::core::Service", "super::helpers::Tool"}) {
		t.Fatalf("unexpected imports: %v", imports)
	}
	if len(types) != 4 {
		t.Fatalf("expected 4 type infos, got %d", len(types))
	}
}

func TestScoreRustEntryPointHeuristics(t *testing.T) {
	mainScore := scoreRustEntryPoint("src/main.rs", nil, []string{"main"})
	libScore := scoreRustEntryPoint("src/lib.rs", []string{"Service"}, nil)
	binScore := scoreRustEntryPoint("src/bin/worker.rs", nil, nil)
	modScore := scoreRustEntryPoint("src/core/mod.rs", nil, nil)

	if !(mainScore > libScore && libScore > binScore && binScore > modScore) {
		t.Fatalf("unexpected score ordering: main=%d lib=%d bin=%d mod=%d", mainScore, libScore, binScore, modScore)
	}
}

func TestAnalyzeRustWithIndexSkipsBrokenPackageAndKeepsHealthyOnes(t *testing.T) {
	tmpDir := t.TempDir()

	healthyDir := filepath.Join(tmpDir, "crates", "healthy")
	if err := os.MkdirAll(filepath.Join(healthyDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir healthy crate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(healthyDir, "Cargo.toml"), []byte("[package]\nname = \"healthy\"\nversion = \"0.1.0\"\n"), 0644); err != nil {
		t.Fatalf("write healthy Cargo.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(healthyDir, "src", "lib.rs"), []byte("pub fn run() {}\n"), 0644); err != nil {
		t.Fatalf("write healthy lib.rs: %v", err)
	}

	brokenDir := filepath.Join(tmpDir, "crates", "broken")
	if err := os.MkdirAll(filepath.Join(brokenDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir broken crate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "Cargo.toml"), []byte("[package]\nname = \"broken\"\nversion = \"0.1.0\"\n"), 0644); err != nil {
		t.Fatalf("write broken Cargo.toml: %v", err)
	}

	brokenMissing := filepath.Join(brokenDir, "src", "lib.rs")
	idx := &FileIndex{
		Root: tmpDir,
		Files: []FileRecord{
			{
				AbsPath:  filepath.Join(healthyDir, "src", "lib.rs"),
				RelPath:  "crates/healthy/src/lib.rs",
				Language: languageRust,
			},
			{
				AbsPath:  brokenMissing,
				RelPath:  "crates/broken/src/lib.rs",
				Language: languageRust,
			},
		},
	}

	opts := DefaultOptions()
	opts.ProjectRoot = tmpDir
	cm, err := analyzeRustWithIndex(context.Background(), tmpDir, idx, opts, nil, nil)
	if err != nil {
		t.Fatalf("analyzeRustWithIndex returned error: %v", err)
	}
	if len(cm.Packages) != 1 {
		t.Fatalf("expected one healthy package, got %d", len(cm.Packages))
	}
	if cm.Packages[0].ImportPath != "healthy" {
		t.Fatalf("expected healthy package to remain, got %q", cm.Packages[0].ImportPath)
	}
}
