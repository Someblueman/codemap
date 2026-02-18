package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildFileIndexIncludesTypeScriptByDefault(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tmpDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "src", "index.ts"), []byte("export const x = 1;\n"), 0644); err != nil {
		t.Fatalf("write src/index.ts: %v", err)
	}

	idx, err := BuildFileIndex(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("BuildFileIndex returned error: %v", err)
	}
	if len(idx.Files) != 1 {
		t.Fatalf("expected 1 indexed file, got %d", len(idx.Files))
	}
	if idx.Files[0].Language != languageTypeScript {
		t.Fatalf("expected typescript language, got %q", idx.Files[0].Language)
	}
}

func TestAnalyzeTypeScriptProject(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte("{\"name\":\"ts-app\"}\n"), 0644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	indexTS := `// TypeScript app entry.
import { foo } from "./foo";
export interface AppConfig {}
export function start() {}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "src", "index.ts"), []byte(indexTS), 0644); err != nil {
		t.Fatalf("write src/index.ts: %v", err)
	}

	fooTS := `export const foo = 1;
`
	if err := os.WriteFile(filepath.Join(tmpDir, "src", "foo.ts"), []byte(fooTS), 0644); err != nil {
		t.Fatalf("write src/foo.ts: %v", err)
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
	if pkg.ImportPath != "ts-app" {
		t.Fatalf("expected package name ts-app, got %q", pkg.ImportPath)
	}
	if pkg.RelativePath != "." {
		t.Fatalf("expected root relative path '.', got %q", pkg.RelativePath)
	}
	if pkg.EntryPoint != "src/index.ts" {
		t.Fatalf("expected src/index.ts entrypoint, got %q", pkg.EntryPoint)
	}
	if pkg.FileCount != 2 {
		t.Fatalf("expected 2 files, got %d", pkg.FileCount)
	}
	if !strings.Contains(pkg.Purpose, "TypeScript app entry") {
		t.Fatalf("expected purpose from comment, got %q", pkg.Purpose)
	}
	if len(pkg.Imports) == 0 || pkg.Imports[0] != "./foo" {
		t.Fatalf("expected relative import ./foo, got %v", pkg.Imports)
	}

	paths := RenderPaths(&Codemap{
		ContentHash: "abc123",
		Packages:    []Package{pkg},
	})
	if !strings.Contains(paths, ".\tsrc/index.ts") {
		t.Fatalf("expected CODEMAP.paths root entry, got:\n%s", paths)
	}
}

func TestAnalyzeTypeScriptProjectIncludeTestsFlag(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte("{\"name\":\"ts-tests\"}\n"), 0644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "src", "index.ts"), []byte("export const run = () => {};\n"), 0644); err != nil {
		t.Fatalf("write src/index.ts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "src", "index.test.ts"), []byte("export const testFn = () => {};\n"), 0644); err != nil {
		t.Fatalf("write src/index.test.ts: %v", err)
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
