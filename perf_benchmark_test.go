package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type benchmarkFixtureKind string

const (
	benchmarkFixtureGo         benchmarkFixtureKind = "go"
	benchmarkFixtureRust       benchmarkFixtureKind = "rust"
	benchmarkFixtureTypeScript benchmarkFixtureKind = "typescript"
)

type benchmarkRepo struct {
	root           string
	changeTarget   string
	changeTemplate func(iteration int) string
}

func BenchmarkCodemapIsStaleWarm(b *testing.B) {
	benchmarkCodemapIsStaleWarm(b, benchmarkFixtureGo, 80, 6)
}

func BenchmarkCodemapRustIsStaleWarm(b *testing.B) {
	benchmarkCodemapIsStaleWarm(b, benchmarkFixtureRust, 80, 6)
}

func BenchmarkCodemapTypeScriptIsStaleWarm(b *testing.B) {
	benchmarkCodemapIsStaleWarm(b, benchmarkFixtureTypeScript, 80, 6)
}

func BenchmarkCodemapEnsureUpToDateWarm(b *testing.B) {
	benchmarkCodemapEnsureUpToDateWarm(b, benchmarkFixtureGo, 80, 6)
}

func BenchmarkCodemapRustEnsureUpToDateWarm(b *testing.B) {
	benchmarkCodemapEnsureUpToDateWarm(b, benchmarkFixtureRust, 80, 6)
}

func BenchmarkCodemapTypeScriptEnsureUpToDateWarm(b *testing.B) {
	benchmarkCodemapEnsureUpToDateWarm(b, benchmarkFixtureTypeScript, 80, 6)
}

func BenchmarkCodemapEnsureUpToDateOnChange(b *testing.B) {
	benchmarkCodemapEnsureUpToDateOnChange(b, benchmarkFixtureGo, 40, 4)
}

func BenchmarkCodemapRustEnsureUpToDateOnChange(b *testing.B) {
	benchmarkCodemapEnsureUpToDateOnChange(b, benchmarkFixtureRust, 40, 4)
}

func BenchmarkCodemapTypeScriptEnsureUpToDateOnChange(b *testing.B) {
	benchmarkCodemapEnsureUpToDateOnChange(b, benchmarkFixtureTypeScript, 40, 4)
}

func benchmarkCodemapIsStaleWarm(b *testing.B, kind benchmarkFixtureKind, packageCount, filesPerPackage int) {
	repo := buildBenchmarkRepo(b, kind, packageCount, filesPerPackage)
	opts := DefaultOptions()
	opts.ProjectRoot = repo.root

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

func benchmarkCodemapEnsureUpToDateWarm(b *testing.B, kind benchmarkFixtureKind, packageCount, filesPerPackage int) {
	repo := buildBenchmarkRepo(b, kind, packageCount, filesPerPackage)
	opts := DefaultOptions()
	opts.ProjectRoot = repo.root

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

func benchmarkCodemapEnsureUpToDateOnChange(b *testing.B, kind benchmarkFixtureKind, packageCount, filesPerPackage int) {
	repo := buildBenchmarkRepo(b, kind, packageCount, filesPerPackage)
	opts := DefaultOptions()
	opts.ProjectRoot = repo.root

	if _, err := Generate(context.Background(), opts); err != nil {
		b.Fatalf("Generate failed: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// File write is setup for this iteration, not the codemap operation itself.
		b.StopTimer()
		content := repo.changeTemplate(i)
		if err := os.WriteFile(repo.changeTarget, []byte(content), 0644); err != nil {
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

func buildBenchmarkRepo(b *testing.B, kind benchmarkFixtureKind, packageCount, filesPerPackage int) benchmarkRepo {
	b.Helper()

	root := b.TempDir()
	switch kind {
	case benchmarkFixtureGo:
		return buildGoBenchmarkRepo(b, root, packageCount, filesPerPackage)
	case benchmarkFixtureRust:
		return buildRustBenchmarkRepo(b, root, packageCount, filesPerPackage)
	case benchmarkFixtureTypeScript:
		return buildTypeScriptBenchmarkRepo(b, root, packageCount, filesPerPackage)
	default:
		b.Fatalf("unknown benchmark fixture kind: %s", kind)
		return benchmarkRepo{}
	}
}

func buildGoBenchmarkRepo(b *testing.B, root string, packageCount, filesPerPackage int) benchmarkRepo {
	b.Helper()

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

	target := filepath.Join(root, "internal", "pkg000", "pkg000_00.go")
	return benchmarkRepo{
		root:         root,
		changeTarget: target,
		changeTemplate: func(iteration int) string {
			changeMarker := strings.Repeat("x", (iteration%17)+1)
			return fmt.Sprintf("package pkg000\n\n// change %s\nvar BenchmarkTick = %d\n", changeMarker, iteration)
		},
	}
}

func buildRustBenchmarkRepo(b *testing.B, root string, packageCount, filesPerPackage int) benchmarkRepo {
	b.Helper()

	for p := 0; p < packageCount; p++ {
		crateName := fmt.Sprintf("crate%03d", p)
		crateDir := filepath.Join(root, "crates", crateName)
		srcDir := filepath.Join(crateDir, "src")
		if err := os.MkdirAll(srcDir, 0755); err != nil {
			b.Fatalf("mkdir %s: %v", srcDir, err)
		}

		cargoTOML := fmt.Sprintf(`[package]
name = "%s"
version = "0.1.0"
edition = "2021"
`, crateName)
		if err := os.WriteFile(filepath.Join(crateDir, "Cargo.toml"), []byte(cargoTOML), 0644); err != nil {
			b.Fatalf("write Cargo.toml for %s: %v", crateName, err)
		}

		var moduleLines []string
		for f := 0; f < filesPerPackage; f++ {
			moduleName := fmt.Sprintf("module_%02d", f)
			moduleLines = append(moduleLines, fmt.Sprintf("pub mod %s;", moduleName))

			typeName := fmt.Sprintf("Type%03d%02d", p, f)
			fileName := moduleName + ".rs"
			src := fmt.Sprintf(`pub struct %s {
    pub value: i32,
}

pub fn new_%s(v: i32) -> %s {
    %s { value: v }
}
`, typeName, strings.ToLower(typeName), typeName, typeName)
			if err := os.WriteFile(filepath.Join(srcDir, fileName), []byte(src), 0644); err != nil {
				b.Fatalf("write %s: %v", fileName, err)
			}
		}

		libSrc := fmt.Sprintf("// crate %s benchmark fixture\n%s\n", crateName, strings.Join(moduleLines, "\n"))
		if err := os.WriteFile(filepath.Join(srcDir, "lib.rs"), []byte(libSrc), 0644); err != nil {
			b.Fatalf("write lib.rs for %s: %v", crateName, err)
		}
	}

	target := filepath.Join(root, "crates", "crate000", "src", "module_00.rs")
	return benchmarkRepo{
		root:         root,
		changeTarget: target,
		changeTemplate: func(iteration int) string {
			changeMarker := strings.Repeat("x", (iteration%17)+1)
			return fmt.Sprintf(`pub struct BenchmarkTick {
    pub value: i32,
}

pub fn benchmark_tick() -> i32 {
    %d
}
// change %s
`, iteration, changeMarker)
		},
	}
}

func buildTypeScriptBenchmarkRepo(b *testing.B, root string, packageCount, filesPerPackage int) benchmarkRepo {
	b.Helper()

	for p := 0; p < packageCount; p++ {
		pkgName := fmt.Sprintf("pkg%03d", p)
		pkgDir := filepath.Join(root, "packages", pkgName)
		srcDir := filepath.Join(pkgDir, "src")
		if err := os.MkdirAll(srcDir, 0755); err != nil {
			b.Fatalf("mkdir %s: %v", srcDir, err)
		}

		manifest := fmt.Sprintf("{\"name\":\"%s\",\"version\":\"0.1.0\"}\n", pkgName)
		if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(manifest), 0644); err != nil {
			b.Fatalf("write package.json for %s: %v", pkgName, err)
		}

		importLines := make([]string, 0, filesPerPackage)
		for f := 0; f < filesPerPackage; f++ {
			typeName := fmt.Sprintf("Type%03d%02d", p, f)
			symbolName := fmt.Sprintf("make%03d%02d", p, f)
			fileName := fmt.Sprintf("file%02d.ts", f)

			fileSrc := fmt.Sprintf(`export interface %s {
  value: number;
}

export function %s(v: number): %s {
  return { value: v };
}
`, typeName, symbolName, typeName)
			if err := os.WriteFile(filepath.Join(srcDir, fileName), []byte(fileSrc), 0644); err != nil {
				b.Fatalf("write %s: %v", fileName, err)
			}
			importLines = append(importLines, fmt.Sprintf("import { %s } from \"./file%02d\";", symbolName, f))
		}

		indexSrc := fmt.Sprintf("// package %s benchmark fixture\n%s\nexport const benchmarkReady = true;\n", pkgName, strings.Join(importLines, "\n"))
		if err := os.WriteFile(filepath.Join(srcDir, "index.ts"), []byte(indexSrc), 0644); err != nil {
			b.Fatalf("write index.ts for %s: %v", pkgName, err)
		}
	}

	target := filepath.Join(root, "packages", "pkg000", "src", "file00.ts")
	return benchmarkRepo{
		root:         root,
		changeTarget: target,
		changeTemplate: func(iteration int) string {
			changeMarker := strings.Repeat("x", (iteration%17)+1)
			return fmt.Sprintf(`export interface BenchmarkTick {
  value: number;
}

export const benchmarkTick: BenchmarkTick = { value: %d };
// change %s
`, iteration, changeMarker)
		},
	}
}
