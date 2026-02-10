package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

const codemapTemplate = `<!-- codemap-hash: {{.ContentHash}} -->
<!-- Generated: {{.GeneratedAt.Format "2006-01-02 15:04:05 UTC"}} -->
<!-- Regenerate: codemap -->

# Codemap

Prefer ` + "`CODEMAP.paths`" + ` for the most token-efficient routing to the files agents should open/edit.

## Package Entry Points

| Package | Entry File | Purpose |
|---------|------------|---------|
{{- range .Packages}}
| {{.RelativePath}} | {{entryPath .}} | {{truncate .Purpose 60}} |
{{- end}}

{{if .Concerns}}

## Concerns (Summary)

| Concern | Files |
|---------|-------|
{{- range .Concerns}}
| {{.Name}} | {{.TotalFiles}} |
{{- end}}

{{end}}
`

// Render generates the CODEMAP.md content.
func Render(cm *Codemap) (string, error) {
	funcMap := template.FuncMap{
		"truncate":  truncate,
		"entryPath": entryPath,
	}

	tmpl, err := template.New("codemap").Funcs(funcMap).Parse(codemapTemplate)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, cm); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}

	return sb.String(), nil
}

func RenderPaths(cm *Codemap) string {
	var sb strings.Builder
	sb.WriteString("# codemap-hash: ")
	sb.WriteString(cm.ContentHash)
	sb.WriteString("\n")
	sb.WriteString("# Generated: ")
	sb.WriteString(cm.GeneratedAt.Format("2006-01-02 15:04:05 UTC"))
	sb.WriteString("\n")
	sb.WriteString("# Regenerate: codemap\n")
	sb.WriteString("# Format: <package>\\t<entry_file>\\t[purpose]\n")

	for _, pkg := range cm.Packages {
		sb.WriteString(pkg.RelativePath)
		sb.WriteString("\t")
		sb.WriteString(entryPath(pkg))
		if purpose := strings.TrimSpace(pkg.Purpose); purpose != "" {
			sb.WriteString("\t")
			sb.WriteString(truncate(purpose, 80))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// EnsureUpToDate generates outputs only if they're stale.
func EnsureUpToDate(ctx context.Context, opts Options) (*Codemap, bool, error) {
	root, err := filepath.Abs(opts.ProjectRoot)
	if err != nil {
		return nil, false, fmt.Errorf("resolve root: %w", err)
	}

	markdownRenderer := MarkdownRenderer{}
	pathsRenderer := PathsRenderer{}
	if opts.OutputPath == "" {
		opts.OutputPath = markdownRenderer.DefaultPath()
	}
	if opts.PathsOutputPath == "" {
		opts.PathsOutputPath = pathsRenderer.DefaultPath()
	}

	idx, err := BuildFileIndex(ctx, root)
	if err != nil {
		return nil, false, fmt.Errorf("build file index: %w", err)
	}

	statePath := resolveStatePath(root, opts)
	state, err := readState(statePath)
	if err != nil {
		return nil, false, fmt.Errorf("read state: %w", err)
	}
	currentHash, err := computeAggregateHashOnly(ctx, idx, state)
	if err != nil {
		return nil, false, fmt.Errorf("compute hash: %w", err)
	}

	outputPath := filepath.Join(root, opts.OutputPath)
	existingHash, err := ReadExistingHash(outputPath)
	if err != nil {
		return nil, false, fmt.Errorf("read existing hash: %w", err)
	}
	if existingHash != "" && existingHash == currentHash {
		if opts.DisablePaths {
			return nil, false, nil
		}

		pathsPath := filepath.Join(root, opts.PathsOutputPath)
		existingPathsHash, err := ReadExistingHash(pathsPath)
		if err != nil {
			return nil, false, fmt.Errorf("read existing paths hash: %w", err)
		}
		if existingPathsHash != "" && existingPathsHash == currentHash {
			return nil, false, nil
		}
	}

	currentHash, nextState, err := computeAggregateHash(ctx, idx, state)
	if err != nil {
		return nil, false, fmt.Errorf("compute hash for write: %w", err)
	}

	analysisPath := resolveAnalysisStatePath(root, opts)
	analysisCache, err := readAnalysisCache(analysisPath)
	if err != nil {
		return nil, false, fmt.Errorf("read analysis cache: %w", err)
	}
	prevState := mergeStateWithAnalysis(state, analysisCache)

	analyzer := GoAnalyzer{}
	cm, err := analyzer.Analyze(ctx, AnalysisInput{
		Root:      root,
		Index:     idx,
		Options:   opts,
		PrevState: prevState,
		NextState: nextState,
	})
	if err != nil {
		return nil, false, fmt.Errorf("analyze: %w", err)
	}

	cm.ContentHash = currentHash
	cm.GeneratedAt = time.Now().UTC()

	if err := writeRenderedOutput(outputPath, markdownRenderer, cm); err != nil {
		return nil, false, err
	}
	if !opts.DisablePaths {
		pathsPath := filepath.Join(root, opts.PathsOutputPath)
		if err := writeRenderedOutput(pathsPath, pathsRenderer, cm); err != nil {
			return nil, false, err
		}
	}
	if err := writeState(statePath, nextState); err != nil {
		return nil, false, fmt.Errorf("write state: %w", err)
	}
	if err := writeAnalysisCache(analysisPath, nextState.Analysis); err != nil {
		return nil, false, fmt.Errorf("write analysis cache: %w", err)
	}

	return cm, true, nil
}

// Generate creates or updates the codemap outputs (always regenerates).
func Generate(ctx context.Context, opts Options) (*Codemap, error) {
	root, err := filepath.Abs(opts.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}

	markdownRenderer := MarkdownRenderer{}
	pathsRenderer := PathsRenderer{}
	if opts.OutputPath == "" {
		opts.OutputPath = markdownRenderer.DefaultPath()
	}
	if opts.PathsOutputPath == "" {
		opts.PathsOutputPath = pathsRenderer.DefaultPath()
	}

	idx, err := BuildFileIndex(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("build file index: %w", err)
	}

	statePath := resolveStatePath(root, opts)
	state, err := readState(statePath)
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}

	analysisPath := resolveAnalysisStatePath(root, opts)
	analysisCache, err := readAnalysisCache(analysisPath)
	if err != nil {
		return nil, fmt.Errorf("read analysis cache: %w", err)
	}

	hash, nextState, err := computeAggregateHash(ctx, idx, state)
	if err != nil {
		return nil, fmt.Errorf("compute hash: %w", err)
	}

	prevState := mergeStateWithAnalysis(state, analysisCache)
	analyzer := GoAnalyzer{}
	cm, err := analyzer.Analyze(ctx, AnalysisInput{
		Root:      root,
		Index:     idx,
		Options:   opts,
		PrevState: prevState,
		NextState: nextState,
	})
	if err != nil {
		return nil, fmt.Errorf("analyze: %w", err)
	}

	cm.ContentHash = hash
	cm.GeneratedAt = time.Now().UTC()

	outputPath := filepath.Join(root, opts.OutputPath)
	if err := writeRenderedOutput(outputPath, markdownRenderer, cm); err != nil {
		return nil, err
	}
	if !opts.DisablePaths {
		pathsPath := filepath.Join(root, opts.PathsOutputPath)
		if err := writeRenderedOutput(pathsPath, pathsRenderer, cm); err != nil {
			return nil, err
		}
	}
	if err := writeState(statePath, nextState); err != nil {
		return nil, fmt.Errorf("write state: %w", err)
	}
	if err := writeAnalysisCache(analysisPath, nextState.Analysis); err != nil {
		return nil, fmt.Errorf("write analysis cache: %w", err)
	}

	return cm, nil
}

func mergeStateWithAnalysis(state *CodemapState, analysis *AnalysisCache) *CodemapState {
	if state == nil || analysis == nil {
		return state
	}
	copy := *state
	copy.Analysis = analysis
	return &copy
}

func writeRenderedOutput(outputPath string, renderer Renderer, cm *Codemap) error {
	content, err := renderer.Render(cm)
	if err != nil {
		return fmt.Errorf("render %s: %w", renderer.Name(), err)
	}
	if err := os.WriteFile(outputPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write %s output: %w", renderer.Name(), err)
	}
	return nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func entryPath(pkg Package) string {
	if pkg.EntryPoint == "" {
		return ""
	}
	if pkg.RelativePath == "" || pkg.RelativePath == "." {
		return pkg.EntryPoint
	}
	return pkg.RelativePath + "/" + pkg.EntryPoint
}
