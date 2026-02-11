package main

import (
	"context"
	"errors"
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

// Renderer formats a codemap model into an output artifact.
type Renderer interface {
	Name() string
	DefaultPath() string
	Render(cm *Codemap) (string, error)
}

// GoAnalyzer is the default analyzer implementation for Go projects.
type GoAnalyzer struct{}

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
