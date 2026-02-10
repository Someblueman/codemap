package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestBuildFileIndexExcludesKnownDirs(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tmpDir, "vendor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, ".hidden"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "testdata"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "workspace"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "vendor", "dep.go"), []byte("package dep\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".hidden", "x.go"), []byte("package hidden\n"), 0644); err != nil {
		t.Fatal(err)
	}

	idx, err := BuildFileIndex(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("BuildFileIndex failed: %v", err)
	}

	seenMain := false
	for _, rec := range idx.Files {
		if rec.RelPath == "main.go" {
			seenMain = true
		}
		if rec.RelPath == "vendor/dep.go" || rec.RelPath == ".hidden/x.go" {
			t.Fatalf("unexpected excluded file in index: %s", rec.RelPath)
		}
	}

	if !seenMain {
		t.Fatal("expected main.go to be indexed")
	}
}

func TestComputeAggregateHashReusesCachedEntries(t *testing.T) {
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "a.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	idx, err := BuildFileIndex(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("BuildFileIndex failed: %v", err)
	}

	hash1, state1, err := computeAggregateHash(context.Background(), idx, nil)
	if err != nil {
		t.Fatalf("computeAggregateHash failed: %v", err)
	}

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}

	hash2, state2, err := computeAggregateHash(context.Background(), idx, state1)
	if err != nil {
		t.Fatalf("computeAggregateHash with cache failed: %v", err)
	}

	if hash1 != hash2 {
		t.Fatalf("expected identical aggregate hash for unchanged metadata, got %s vs %s", hash1, hash2)
	}
	if len(state2.Entries) != 1 || len(state1.Entries) != 1 {
		t.Fatalf("expected single state entry, got %d and %d", len(state1.Entries), len(state2.Entries))
	}
	if state1.Entries[0].ContentHash != state2.Entries[0].ContentHash {
		t.Fatalf("expected cached content hash reuse, got %s vs %s", state1.Entries[0].ContentHash, state2.Entries[0].ContentHash)
	}
}

func TestComputeAggregateHashUpdatesChangedFile(t *testing.T) {
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "a.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	idx1, err := BuildFileIndex(ctx, tmpDir)
	if err != nil {
		t.Fatalf("BuildFileIndex failed: %v", err)
	}
	hash1, state1, err := computeAggregateHash(ctx, idx1, nil)
	if err != nil {
		t.Fatalf("computeAggregateHash failed: %v", err)
	}

	time.Sleep(2 * time.Millisecond)
	if err := os.WriteFile(file, []byte("package main\n// changed\n"), 0644); err != nil {
		t.Fatal(err)
	}

	idx2, err := BuildFileIndex(ctx, tmpDir)
	if err != nil {
		t.Fatalf("BuildFileIndex failed: %v", err)
	}
	hash2, state2, err := computeAggregateHash(ctx, idx2, state1)
	if err != nil {
		t.Fatalf("computeAggregateHash failed: %v", err)
	}

	if hash1 == hash2 {
		t.Fatalf("expected aggregate hash to change after file mutation")
	}
	if len(state1.Entries) != 1 || len(state2.Entries) != 1 {
		t.Fatalf("expected single state entry, got %d and %d", len(state1.Entries), len(state2.Entries))
	}
	if state1.Entries[0].ContentHash == state2.Entries[0].ContentHash {
		t.Fatalf("expected content hash update after file mutation")
	}
}

func TestEnsureUpToDatePersistsState(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	opts := DefaultOptions()
	opts.ProjectRoot = tmpDir

	cm, generated, err := EnsureUpToDate(context.Background(), opts)
	if err != nil {
		t.Fatalf("EnsureUpToDate failed: %v", err)
	}
	if !generated {
		t.Fatal("expected first run to generate outputs")
	}
	if cm == nil {
		t.Fatal("expected codemap model on generation")
	}

	statePath := filepath.Join(tmpDir, opts.StatePath)
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("expected state file at %s: %v", statePath, err)
	}

	cm2, generated2, err := EnsureUpToDate(context.Background(), opts)
	if err != nil {
		t.Fatalf("EnsureUpToDate second run failed: %v", err)
	}
	if generated2 {
		t.Fatal("expected second run to skip generation")
	}
	if cm2 != nil {
		t.Fatal("expected nil codemap when no generation occurs")
	}
}

func TestBuildConcernsFromIndex(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "cmd", "app"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "internal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "cmd", "app", "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "internal", "cli_runner.go"), []byte("package internal\n"), 0644); err != nil {
		t.Fatal(err)
	}

	idx, err := BuildFileIndex(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("BuildFileIndex failed: %v", err)
	}

	defs := []ConcernDef{
		{Name: "CLI", Patterns: []string{"cmd/**/*.go", "**/cli_*.go"}},
	}
	concerns, err := buildConcerns(idx, defs, 10)
	if err != nil {
		t.Fatalf("buildConcerns failed: %v", err)
	}
	if len(concerns) != 1 {
		t.Fatalf("expected 1 concern, got %d", len(concerns))
	}
	if concerns[0].TotalFiles != 2 {
		t.Fatalf("expected 2 matched files, got %d", concerns[0].TotalFiles)
	}
}

func TestEnsureUpToDateWritesAnalysisCache(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "internal", "foo"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "internal", "bar"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "internal", "foo", "foo.go"), []byte("package foo\n\ntype Foo struct{}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "internal", "bar", "bar.go"), []byte("package bar\n\ntype Bar struct{}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	opts := DefaultOptions()
	opts.ProjectRoot = tmpDir
	if _, _, err := EnsureUpToDate(context.Background(), opts); err != nil {
		t.Fatalf("EnsureUpToDate failed: %v", err)
	}

	statePath := filepath.Join(tmpDir, opts.StatePath)
	state, err := readState(statePath)
	if err != nil {
		t.Fatalf("readState failed: %v", err)
	}
	if state == nil {
		t.Fatal("expected hash state")
	}

	analysisPath := resolveAnalysisStatePath(tmpDir, opts)
	cache, err := readAnalysisCache(analysisPath)
	if err != nil {
		t.Fatalf("readAnalysisCache failed: %v", err)
	}
	if cache == nil {
		t.Fatal("expected analysis cache")
	}
	if cache.Version != analysisCacheVersionV2 {
		t.Fatalf("expected analysis cache version %d, got %d", analysisCacheVersionV2, cache.Version)
	}
	if len(cache.Packages) != 2 {
		t.Fatalf("expected 2 cached packages, got %d", len(cache.Packages))
	}

	paths := []string{
		cache.Packages[0].RelativePath,
		cache.Packages[1].RelativePath,
	}
	sort.Strings(paths)
	if paths[0] != "internal/bar" || paths[1] != "internal/foo" {
		t.Fatalf("unexpected cached package paths: %v", paths)
	}
	for _, pkg := range cache.Packages {
		if pkg.Fingerprint == "" {
			t.Fatalf("expected non-empty fingerprint for %s", pkg.RelativePath)
		}
	}
}

func TestEnsureUpToDateIncrementalCacheHandlesDelete(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "internal", "a"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "internal", "b"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "internal", "a", "a.go"), []byte("package a\n\nvar A = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "internal", "b", "b.go"), []byte("package b\n\nvar B = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	opts := DefaultOptions()
	opts.ProjectRoot = tmpDir
	if _, _, err := EnsureUpToDate(context.Background(), opts); err != nil {
		t.Fatalf("EnsureUpToDate initial failed: %v", err)
	}

	if err := os.Remove(filepath.Join(tmpDir, "internal", "b", "b.go")); err != nil {
		t.Fatal(err)
	}
	if _, generated, err := EnsureUpToDate(context.Background(), opts); err != nil {
		t.Fatalf("EnsureUpToDate after delete failed: %v", err)
	} else if !generated {
		t.Fatal("expected regeneration after delete")
	}

	analysisPath := resolveAnalysisStatePath(tmpDir, opts)
	cache, err := readAnalysisCache(analysisPath)
	if err != nil {
		t.Fatalf("readAnalysisCache failed: %v", err)
	}
	if cache == nil {
		t.Fatal("expected analysis cache")
	}
	for _, pkg := range cache.Packages {
		if pkg.RelativePath == "internal/b" {
			t.Fatalf("did not expect deleted package in cache: %s", pkg.RelativePath)
		}
	}
}

func TestEnsureUpToDateInvalidatesIncompatibleAnalysisCache(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n\nvar X = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	opts := DefaultOptions()
	opts.ProjectRoot = tmpDir
	if _, _, err := EnsureUpToDate(context.Background(), opts); err != nil {
		t.Fatalf("EnsureUpToDate initial failed: %v", err)
	}

	analysisPath := resolveAnalysisStatePath(tmpDir, opts)
	cache, err := readAnalysisCache(analysisPath)
	if err != nil {
		t.Fatalf("readAnalysisCache failed: %v", err)
	}
	if cache == nil {
		t.Fatal("expected analysis cache")
	}
	cache.Version = 1
	if err := writeAnalysisCache(analysisPath, cache); err != nil {
		t.Fatalf("writeAnalysisCache failed: %v", err)
	}

	time.Sleep(2 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n\nvar X = 2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, generated, err := EnsureUpToDate(context.Background(), opts); err != nil {
		t.Fatalf("EnsureUpToDate failed after invalid cache: %v", err)
	} else if !generated {
		t.Fatal("expected regeneration after source change")
	}

	after, err := readAnalysisCache(analysisPath)
	if err != nil {
		t.Fatalf("readAnalysisCache failed: %v", err)
	}
	if after == nil || after.Version != analysisCacheVersionV2 {
		t.Fatalf("expected analysis cache version %d after rebuild", analysisCacheVersionV2)
	}
}

func TestParseHashLine(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{line: "<!-- codemap-hash: deadbeef -->", want: "deadbeef"},
		{line: "# codemap-hash: 0123abcd", want: "0123abcd"},
		{line: "codemap-hash: 00ff", want: "00ff"},
		{line: "# codemap-hash: INVALID", want: ""},
		{line: "random", want: ""},
	}

	for _, tt := range tests {
		got := parseHashLine(tt.line)
		if got != tt.want {
			t.Fatalf("parseHashLine(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}

func TestAggregateHashFromFilesystemState(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "b.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	idx, err := BuildFileIndex(ctx, tmpDir)
	if err != nil {
		t.Fatalf("BuildFileIndex failed: %v", err)
	}
	hash, state, err := computeAggregateHash(ctx, idx, nil)
	if err != nil {
		t.Fatalf("computeAggregateHash failed: %v", err)
	}

	got, ok, err := aggregateHashFromFilesystemState(ctx, tmpDir, state, nil)
	if err != nil {
		t.Fatalf("aggregateHashFromFilesystemState failed: %v", err)
	}
	if !ok || got != hash {
		t.Fatalf("expected fast-path match with same hash, got ok=%v hash=%s", ok, got)
	}

	time.Sleep(2 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(tmpDir, "c.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, ok, err = aggregateHashFromFilesystemState(ctx, tmpDir, state, nil)
	if err != nil {
		t.Fatalf("aggregateHashFromFilesystemState failed: %v", err)
	}
	if ok {
		t.Fatal("expected fast-path mismatch after adding file")
	}
}

func TestAggregateHashFromFilesystemStateDetectsGoFileInPreviouslyNonGoDir(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "docs", "readme.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	idx, err := BuildFileIndex(ctx, tmpDir)
	if err != nil {
		t.Fatalf("BuildFileIndex failed: %v", err)
	}
	_, state, err := computeAggregateHash(ctx, idx, nil)
	if err != nil {
		t.Fatalf("computeAggregateHash failed: %v", err)
	}

	time.Sleep(2 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(tmpDir, "docs", "new.go"), []byte("package docs\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, ok, err := aggregateHashFromFilesystemState(ctx, tmpDir, state, nil)
	if err != nil {
		t.Fatalf("aggregateHashFromFilesystemState failed: %v", err)
	}
	if ok {
		t.Fatal("expected mismatch after adding go file in previously non-go directory")
	}
}
