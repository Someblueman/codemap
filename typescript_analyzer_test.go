package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
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

func TestFindTypeScriptPackageRootPrefersNearestManifest(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte("{\"name\":\"root\"}\n"), 0644); err != nil {
		t.Fatalf("write root package.json: %v", err)
	}
	pkgDir := filepath.Join(tmpDir, "packages", "web")
	if err := os.MkdirAll(filepath.Join(pkgDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir package src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte("{\"name\":\"web\"}\n"), 0644); err != nil {
		t.Fatalf("write nested package.json: %v", err)
	}

	filePath := filepath.Join(pkgDir, "src", "index.ts")
	if err := os.WriteFile(filePath, []byte("export const run = 1;\n"), 0644); err != nil {
		t.Fatalf("write index.ts: %v", err)
	}

	pkgRel, pkgAbs, err := findTypeScriptPackageRoot(tmpDir, filePath)
	if err != nil {
		t.Fatalf("findTypeScriptPackageRoot returned error: %v", err)
	}
	if pkgRel != "packages/web" {
		t.Fatalf("expected package root packages/web, got %q", pkgRel)
	}
	if pkgAbs != pkgDir {
		t.Fatalf("expected package abs path %q, got %q", pkgDir, pkgAbs)
	}
}

func TestFindTypeScriptPackageRootFallsBackToRepoRootWithoutManifest(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "src", "index.ts")
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filePath, []byte("export const run = 1;\n"), 0644); err != nil {
		t.Fatalf("write index.ts: %v", err)
	}

	pkgRel, pkgAbs, err := findTypeScriptPackageRoot(tmpDir, filePath)
	if err != nil {
		t.Fatalf("findTypeScriptPackageRoot returned error: %v", err)
	}
	if pkgRel != "." {
		t.Fatalf("expected fallback package root '.', got %q", pkgRel)
	}
	if pkgAbs != tmpDir {
		t.Fatalf("expected fallback package abs path %q, got %q", tmpDir, pkgAbs)
	}
}

func TestReadTypeScriptPackageNameFallsBackWhenManifestIsMalformed(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte("{"), 0644); err != nil {
		t.Fatalf("write malformed package.json: %v", err)
	}

	if got := readTypeScriptPackageName(tmpDir, "packages/web"); got != "web" {
		t.Fatalf("expected fallback package name web, got %q", got)
	}
}

func TestIsTypeScriptTestPathHeuristics(t *testing.T) {
	cases := []struct {
		name          string
		path          string
		fileMatchTest bool
		want          bool
	}{
		{name: "suffix matched by index", path: "src/component.test.ts", fileMatchTest: true, want: true},
		{name: "root __tests__", path: "__tests__/component.ts", want: true},
		{name: "nested __tests__", path: "src/__tests__/component.ts", want: true},
		{name: "non-test", path: "src/component.ts", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTypeScriptTestPath(tc.path, tc.fileMatchTest); got != tc.want {
				t.Fatalf("isTypeScriptTestPath(%q, %v) = %v, want %v", tc.path, tc.fileMatchTest, got, tc.want)
			}
		})
	}
}

func TestParseTypeScriptFileSymbolsExtractsExportsAndRelativeImports(t *testing.T) {
	content := []byte(`
import React from "react";
import { helper } from "./helper";
export class App {}
export interface Config {}
export type ID = string;
export enum Mode { Fast }
export function start() {}
export const VERSION = "1.0.0";
`)

	types, keyTypes, keyFuncs, imports := parseTypeScriptFileSymbols(content)

	wantTypes := []string{"App", "Config", "ID", "Mode"}
	if !reflect.DeepEqual(keyTypes, wantTypes) {
		t.Fatalf("unexpected key types: got %v want %v", keyTypes, wantTypes)
	}
	if !reflect.DeepEqual(keyFuncs, []string{"start", "VERSION"}) {
		t.Fatalf("unexpected key funcs: %v", keyFuncs)
	}
	if !reflect.DeepEqual(imports, []string{"./helper"}) {
		t.Fatalf("unexpected imports: %v", imports)
	}
	if len(types) != 4 {
		t.Fatalf("expected 4 type infos, got %d", len(types))
	}
}

func TestScoreTypeScriptEntryPointHeuristics(t *testing.T) {
	srcIndexScore := scoreTypeScriptEntryPoint("src/index.ts", nil, nil)
	srcIndexMTSScore := scoreTypeScriptEntryPoint("src/index.mts", nil, nil)
	rootIndexScore := scoreTypeScriptEntryPoint("index.ts", nil, nil)
	srcMainScore := scoreTypeScriptEntryPoint("src/main.ts", nil, nil)
	binScore := scoreTypeScriptEntryPoint("src/bin/worker.ts", nil, nil)

	if srcIndexScore != srcIndexMTSScore {
		t.Fatalf("expected src index ts/mts scores to match, got %d vs %d", srcIndexScore, srcIndexMTSScore)
	}
	if !(srcIndexScore > rootIndexScore && rootIndexScore > srcMainScore && srcMainScore > binScore) {
		t.Fatalf("unexpected score ordering: srcIndex=%d rootIndex=%d srcMain=%d bin=%d", srcIndexScore, rootIndexScore, srcMainScore, binScore)
	}
}

func TestAnalyzeTypeScriptWithIndexSkipsBrokenPackageAndKeepsHealthyOnes(t *testing.T) {
	tmpDir := t.TempDir()

	healthyDir := filepath.Join(tmpDir, "packages", "healthy")
	if err := os.MkdirAll(filepath.Join(healthyDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir healthy package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(healthyDir, "package.json"), []byte("{\"name\":\"healthy\"}\n"), 0644); err != nil {
		t.Fatalf("write healthy package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(healthyDir, "src", "index.ts"), []byte("export const run = 1;\n"), 0644); err != nil {
		t.Fatalf("write healthy index.ts: %v", err)
	}

	brokenDir := filepath.Join(tmpDir, "packages", "broken")
	if err := os.MkdirAll(filepath.Join(brokenDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir broken package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "package.json"), []byte("{\"name\":\"broken\"}\n"), 0644); err != nil {
		t.Fatalf("write broken package.json: %v", err)
	}

	brokenMissing := filepath.Join(brokenDir, "src", "index.ts")
	idx := &FileIndex{
		Root: tmpDir,
		Files: []FileRecord{
			{
				AbsPath:  filepath.Join(healthyDir, "src", "index.ts"),
				RelPath:  "packages/healthy/src/index.ts",
				Language: languageTypeScript,
			},
			{
				AbsPath:  brokenMissing,
				RelPath:  "packages/broken/src/index.ts",
				Language: languageTypeScript,
			},
		},
	}

	opts := DefaultOptions()
	opts.ProjectRoot = tmpDir
	cm, err := analyzeTypeScriptWithIndex(context.Background(), tmpDir, idx, opts, nil, nil)
	if err != nil {
		t.Fatalf("analyzeTypeScriptWithIndex returned error: %v", err)
	}
	if len(cm.Packages) != 1 {
		t.Fatalf("expected one healthy package, got %d", len(cm.Packages))
	}
	if cm.Packages[0].ImportPath != "healthy" {
		t.Fatalf("expected healthy package to remain, got %q", cm.Packages[0].ImportPath)
	}
}
