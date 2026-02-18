package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type stubLanguageAnalyzer struct {
	id       string
	packages []Package
	concerns []Concern
	calls    int
}

func (a *stubLanguageAnalyzer) LanguageID() string {
	return a.id
}

func (a *stubLanguageAnalyzer) Analyze(_ context.Context, in AnalysisInput) (*Codemap, error) {
	a.calls++
	return &Codemap{
		ProjectRoot: in.Root,
		Packages:    append([]Package(nil), a.packages...),
		Concerns:    append([]Concern(nil), a.concerns...),
	}, nil
}

func TestAnalyzeWithRegistryRunsAllDetectedLanguageAnalyzers(t *testing.T) {
	goAnalyzer := &stubLanguageAnalyzer{
		id: languageGo,
		packages: []Package{
			{ImportPath: "example.com/app", RelativePath: ".", EntryPoint: "main.go"},
		},
		concerns: []Concern{{Name: "Testing", TotalFiles: 1}},
	}
	rustAnalyzer := &stubLanguageAnalyzer{
		id: languageRust,
		packages: []Package{
			{ImportPath: "rust-app", RelativePath: ".", EntryPoint: "src/main.rs"},
		},
		concerns: []Concern{{Name: "Ignored", TotalFiles: 100}},
	}
	tsAnalyzer := &stubLanguageAnalyzer{
		id: languageTypeScript,
		packages: []Package{
			{ImportPath: "ts-app", RelativePath: ".", EntryPoint: "src/index.ts"},
		},
	}

	registry := NewAnalyzerRegistry()
	registry.Register(goAnalyzer)
	registry.Register(rustAnalyzer)
	registry.Register(tsAnalyzer)

	idx := &FileIndex{
		Files: []FileRecord{
			{Language: languageRust},
			{Language: languageGo},
		},
	}

	cm, err := AnalyzeWithRegistry(context.Background(), AnalysisInput{
		Root:    "/tmp/repo",
		Index:   idx,
		Options: DefaultOptions(),
	}, registry)
	if err != nil {
		t.Fatalf("AnalyzeWithRegistry returned error: %v", err)
	}

	if goAnalyzer.calls != 1 {
		t.Fatalf("expected Go analyzer to run once, got %d", goAnalyzer.calls)
	}
	if rustAnalyzer.calls != 1 {
		t.Fatalf("expected Rust analyzer to run once, got %d", rustAnalyzer.calls)
	}
	if tsAnalyzer.calls != 0 {
		t.Fatalf("expected TypeScript analyzer not to run, got %d", tsAnalyzer.calls)
	}

	if len(cm.Packages) != 2 {
		t.Fatalf("expected 2 merged packages, got %d", len(cm.Packages))
	}
	if cm.Packages[0].ImportPath != "example.com/app" || cm.Packages[1].ImportPath != "rust-app" {
		t.Fatalf("unexpected package merge/sort order: %+v", cm.Packages)
	}
	if len(cm.Concerns) != 1 || cm.Concerns[0].Name != "Testing" {
		t.Fatalf("expected concerns from first executed analyzer, got %+v", cm.Concerns)
	}
}

func TestAnalyzeWithRegistryFallsBackWhenNoKnownLanguageDetected(t *testing.T) {
	goAnalyzer := &stubLanguageAnalyzer{
		id: languageGo,
		packages: []Package{
			{ImportPath: "example.com/app", RelativePath: ".", EntryPoint: "main.go"},
		},
	}
	registry := NewAnalyzerRegistry()
	registry.Register(goAnalyzer)

	idx := &FileIndex{
		Files: []FileRecord{
			{Language: "python"},
		},
	}

	cm, err := AnalyzeWithRegistry(context.Background(), AnalysisInput{
		Root:    "/tmp/repo",
		Index:   idx,
		Options: DefaultOptions(),
	}, registry)
	if err != nil {
		t.Fatalf("AnalyzeWithRegistry returned error: %v", err)
	}
	if goAnalyzer.calls != 1 {
		t.Fatalf("expected fallback Go analyzer to run once, got %d", goAnalyzer.calls)
	}
	if len(cm.Packages) != 1 || cm.Packages[0].ImportPath != "example.com/app" {
		t.Fatalf("unexpected fallback output: %+v", cm.Packages)
	}
}

func TestAnalyzeWithRegistryErrorsWithoutRegisteredAnalyzers(t *testing.T) {
	idx := &FileIndex{Files: []FileRecord{{Language: languageGo}}}
	_, err := AnalyzeWithRegistry(context.Background(), AnalysisInput{
		Index:   idx,
		Options: DefaultOptions(),
	}, NewAnalyzerRegistry())
	if err == nil {
		t.Fatal("expected error with empty analyzer registry")
	}
}

func TestAnalyzeIncludesPackagesFromMultipleLanguages(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/mixed\n\ngo 1.25.0\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	webDir := filepath.Join(tmpDir, "web")
	if err := os.MkdirAll(filepath.Join(webDir, "src"), 0755); err != nil {
		t.Fatalf("mkdir web/src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "package.json"), []byte("{\"name\":\"web-app\"}\n"), 0644); err != nil {
		t.Fatalf("write web/package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "src", "index.ts"), []byte("export const boot = 1;\n"), 0644); err != nil {
		t.Fatalf("write web/src/index.ts: %v", err)
	}

	opts := DefaultOptions()
	opts.ProjectRoot = tmpDir
	cm, err := Analyze(context.Background(), opts)
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	var foundGo bool
	var foundTS bool
	for _, pkg := range cm.Packages {
		if pkg.ImportPath == "example.com/mixed" {
			foundGo = true
		}
		if pkg.ImportPath == "web-app" && pkg.RelativePath == "web" {
			foundTS = true
		}
	}
	if !foundGo || !foundTS {
		t.Fatalf("expected both Go and TypeScript packages, got %+v", cm.Packages)
	}
}
