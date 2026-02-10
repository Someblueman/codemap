package main

import (
	"context"
	"os"
	"path/filepath"
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
