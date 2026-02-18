package main

import (
	"context"
	"os"
	"path/filepath"
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
