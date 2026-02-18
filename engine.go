package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// AnalysisInput provides shared context for analyzer implementations.
type AnalysisInput struct {
	Root      string
	Index     *FileIndex
	Options   Options
	PrevState *CodemapState
	NextState *CodemapState
}

// Analyzer builds a codemap model from a project snapshot.
type Analyzer interface {
	Analyze(ctx context.Context, in AnalysisInput) (*Codemap, error)
}

// LanguageAnalyzer is an analyzer bound to a specific language.
type LanguageAnalyzer interface {
	Analyzer
	LanguageID() string
}

// AnalyzerRegistry stores language-specific analyzers.
type AnalyzerRegistry struct {
	analyzers map[string]LanguageAnalyzer
}

// NewAnalyzerRegistry constructs an empty analyzer registry.
func NewAnalyzerRegistry() *AnalyzerRegistry {
	return &AnalyzerRegistry{
		analyzers: make(map[string]LanguageAnalyzer),
	}
}

// Register adds or replaces an analyzer for a language.
func (r *AnalyzerRegistry) Register(analyzer LanguageAnalyzer) {
	if r == nil || analyzer == nil {
		return
	}
	r.analyzers[analyzer.LanguageID()] = analyzer
}

// AnalyzerFor returns the analyzer registered for a language.
func (r *AnalyzerRegistry) AnalyzerFor(languageID string) (LanguageAnalyzer, bool) {
	if r == nil {
		return nil, false
	}
	analyzer, ok := r.analyzers[languageID]
	return analyzer, ok
}

// LanguageIDs returns registered language IDs sorted lexicographically.
func (r *AnalyzerRegistry) LanguageIDs() []string {
	if r == nil || len(r.analyzers) == 0 {
		return nil
	}
	ids := make([]string, 0, len(r.analyzers))
	for id := range r.analyzers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// DefaultAnalyzerRegistry returns the current built-in analyzer registry.
func DefaultAnalyzerRegistry() *AnalyzerRegistry {
	registry := NewAnalyzerRegistry()
	registry.Register(GoAnalyzer{})
	registry.Register(TypeScriptAnalyzer{})
	registry.Register(RustAnalyzer{})
	return registry
}

// AnalyzeWithRegistry runs analyzers for all detected languages in deterministic order.
func AnalyzeWithRegistry(ctx context.Context, in AnalysisInput, registry *AnalyzerRegistry) (*Codemap, error) {
	if in.Index == nil {
		return nil, errors.New("missing file index")
	}
	if registry == nil {
		registry = DefaultAnalyzerRegistry()
	}

	selectedIDs := selectedAnalyzerLanguageIDs(in.Index, registry)
	if len(selectedIDs) == 0 {
		fallback, ok := fallbackAnalyzerLanguageID(registry)
		if !ok {
			return nil, errors.New("no analyzers registered")
		}
		selectedIDs = []string{fallback}
	}

	merged := &Codemap{
		ProjectRoot: in.Root,
		Packages:    make([]Package, 0),
	}

	for i, languageID := range selectedIDs {
		analyzer, ok := registry.AnalyzerFor(languageID)
		if !ok {
			return nil, fmt.Errorf("no analyzer registered for language: %s", languageID)
		}
		cm, err := analyzer.Analyze(ctx, in)
		if err != nil {
			return nil, err
		}
		if cm == nil {
			continue
		}
		merged.Packages = append(merged.Packages, cm.Packages...)
		if i == 0 {
			merged.Concerns = cm.Concerns
		}
	}

	sortPackages(merged.Packages)
	if merged.Concerns == nil {
		concerns, err := buildConcerns(in.Index, in.Options.Concerns, in.Options.ConcernExampleLimit)
		if err != nil {
			return nil, fmt.Errorf("build concerns: %w", err)
		}
		merged.Concerns = concerns
	}
	return merged, nil
}

func selectedAnalyzerLanguageIDs(idx *FileIndex, registry *AnalyzerRegistry) []string {
	if idx == nil || registry == nil {
		return nil
	}
	present := make(map[string]struct{})
	for _, rec := range idx.Files {
		if rec.Language == "" {
			continue
		}
		present[rec.Language] = struct{}{}
	}
	ids := make([]string, 0, len(present))
	for _, id := range registry.LanguageIDs() {
		if _, ok := present[id]; ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func fallbackAnalyzerLanguageID(registry *AnalyzerRegistry) (string, bool) {
	if registry == nil {
		return "", false
	}
	if _, ok := registry.AnalyzerFor(languageGo); ok {
		return languageGo, true
	}
	ids := registry.LanguageIDs()
	if len(ids) == 0 {
		return "", false
	}
	return ids[0], true
}

func sortPackages(packages []Package) {
	sort.Slice(packages, func(i, j int) bool {
		if packages[i].RelativePath != packages[j].RelativePath {
			return packages[i].RelativePath < packages[j].RelativePath
		}
		if packages[i].ImportPath != packages[j].ImportPath {
			return packages[i].ImportPath < packages[j].ImportPath
		}
		if packages[i].EntryPoint != packages[j].EntryPoint {
			return packages[i].EntryPoint < packages[j].EntryPoint
		}
		return packages[i].Purpose < packages[j].Purpose
	})
}

// Renderer formats a codemap model into an output artifact.
type Renderer interface {
	Name() string
	DefaultPath() string
	Render(cm *Codemap) (string, error)
}

// GoAnalyzer is the default analyzer implementation for Go projects.
type GoAnalyzer struct{}

func (GoAnalyzer) LanguageID() string { return languageGo }

func (GoAnalyzer) Analyze(ctx context.Context, in AnalysisInput) (*Codemap, error) {
	if in.Index == nil {
		return nil, errors.New("missing file index")
	}
	return analyzeGoWithIndex(ctx, in.Root, in.Index, in.Options, in.PrevState, in.NextState)
}

// MarkdownRenderer renders CODEMAP.md output.
type MarkdownRenderer struct{}

func (MarkdownRenderer) Name() string        { return "markdown" }
func (MarkdownRenderer) DefaultPath() string { return "CODEMAP.md" }
func (MarkdownRenderer) Render(cm *Codemap) (string, error) {
	return Render(cm)
}

// PathsRenderer renders CODEMAP.paths output.
type PathsRenderer struct{}

func (PathsRenderer) Name() string        { return "paths" }
func (PathsRenderer) DefaultPath() string { return "CODEMAP.paths" }
func (PathsRenderer) Render(cm *Codemap) (string, error) {
	return RenderPaths(cm), nil
}
