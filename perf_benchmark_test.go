package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkCodemapIsStaleWarm(b *testing.B) {
	root := buildBenchmarkRepo(b, 80, 6)
	opts := DefaultOptions()
	opts.ProjectRoot = root

	if _, err := Generate(context.Background(), opts); err != nil {
		b.Fatalf("Generate failed: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stale, err := IsStale(context.Background(), opts)
		if err != nil {
			b.Fatalf("IsStale failed: %v", err)
		}
		if stale {
			b.Fatal("expected warm codemap state to be up to date")
		}
	}
}

func BenchmarkCodemapEnsureUpToDateWarm(b *testing.B) {
	root := buildBenchmarkRepo(b, 80, 6)
	opts := DefaultOptions()
	opts.ProjectRoot = root

	if _, err := Generate(context.Background(), opts); err != nil {
		b.Fatalf("Generate failed: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm, generated, err := EnsureUpToDate(context.Background(), opts)
		if err != nil {
			b.Fatalf("EnsureUpToDate failed: %v", err)
		}
		if generated {
			b.Fatal("expected warm EnsureUpToDate to skip generation")
		}
		if cm != nil {
			b.Fatal("expected nil codemap model when no generation occurs")
		}
	}
}

func BenchmarkCodemapEnsureUpToDateOnChange(b *testing.B) {
	root := buildBenchmarkRepo(b, 40, 4)
	opts := DefaultOptions()
	opts.ProjectRoot = root

	if _, err := Generate(context.Background(), opts); err != nil {
		b.Fatalf("Generate failed: %v", err)
	}

	target := filepath.Join(root, "internal", "pkg000", "pkg000_00.go")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// File write is setup for this iteration, not the codemap operation itself.
		b.StopTimer()
		changeMarker := strings.Repeat("x", (i%17)+1)
		content := fmt.Sprintf("package pkg000\n\n// change %s\nvar BenchmarkTick = %d\n", changeMarker, i)
		if err := os.WriteFile(target, []byte(content), 0644); err != nil {
			b.Fatalf("write changed file: %v", err)
		}
		b.StartTimer()

		cm, generated, err := EnsureUpToDate(context.Background(), opts)
		if err != nil {
			b.Fatalf("EnsureUpToDate failed: %v", err)
		}
		if !generated {
			b.Fatal("expected EnsureUpToDate to regenerate after source change")
		}
		if cm == nil {
			b.Fatal("expected codemap model when regeneration occurs")
		}
	}
}

func buildBenchmarkRepo(b *testing.B, packageCount, filesPerPackage int) string {
	b.Helper()

	root := b.TempDir()
	goMod := "module example.com/bench\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0644); err != nil {
		b.Fatalf("write go.mod: %v", err)
	}

	for p := 0; p < packageCount; p++ {
		pkgName := fmt.Sprintf("pkg%03d", p)
		pkgDir := filepath.Join(root, "internal", pkgName)
		if err := os.MkdirAll(pkgDir, 0755); err != nil {
			b.Fatalf("mkdir %s: %v", pkgDir, err)
		}

		doc := fmt.Sprintf("// Package %s contains benchmark fixture code.\npackage %s\n", pkgName, pkgName)
		if err := os.WriteFile(filepath.Join(pkgDir, "doc.go"), []byte(doc), 0644); err != nil {
			b.Fatalf("write doc.go for %s: %v", pkgName, err)
		}

		for f := 0; f < filesPerPackage; f++ {
			typeName := fmt.Sprintf("Type%03d%02d", p, f)
			fileName := fmt.Sprintf("%s_%02d.go", pkgName, f)
			src := fmt.Sprintf(`package %s

// %s is benchmark fixture data.
type %s struct {
	Value int
}

// New%s constructs %s.
func New%s(v int) *%s {
	return &%s{Value: v}
}
`, pkgName, typeName, typeName, typeName, typeName, typeName, typeName, typeName)
			if err := os.WriteFile(filepath.Join(pkgDir, fileName), []byte(src), 0644); err != nil {
				b.Fatalf("write %s: %v", fileName, err)
			}
		}
	}

	return root
}
