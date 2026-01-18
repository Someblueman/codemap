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

	if opts.OutputPath == "" {
		opts.OutputPath = "CODEMAP.md"
	}
	if opts.PathsOutputPath == "" {
		opts.PathsOutputPath = "CODEMAP.paths"
	}

	currentHash, err := ComputeHash(ctx, root)
	if err != nil {
		return nil, false, fmt.Errorf("compute hash: %w", err)
	}

	// Avoid expensive analysis if outputs are up to date.
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

	// Analyze the codebase
	cm, err := Analyze(ctx, opts)
	if err != nil {
		return nil, false, fmt.Errorf("analyze: %w", err)
	}

	cm.ContentHash = currentHash
	cm.GeneratedAt = time.Now().UTC()

	// Render markdown output
	content, err := Render(cm)
	if err != nil {
		return nil, false, fmt.Errorf("render: %w", err)
	}

	// Write markdown output
	if err := os.WriteFile(outputPath, []byte(content), 0644); err != nil {
		return nil, false, fmt.Errorf("write output: %w", err)
	}

	// Write paths output
	if !opts.DisablePaths {
		pathsContent := RenderPaths(cm)
		pathsPath := filepath.Join(root, opts.PathsOutputPath)
		if err := os.WriteFile(pathsPath, []byte(pathsContent), 0644); err != nil {
			return nil, false, fmt.Errorf("write paths output: %w", err)
		}
	}

	return cm, true, nil
}

// Generate creates or updates the codemap outputs (always regenerates).
func Generate(ctx context.Context, opts Options) (*Codemap, error) {
	// Analyze the codebase
	cm, err := Analyze(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("analyze: %w", err)
	}

	// Compute hash
	root, err := filepath.Abs(opts.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}

	hash, err := ComputeHash(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("compute hash: %w", err)
	}

	cm.ContentHash = hash
	cm.GeneratedAt = time.Now().UTC()

	// Render
	content, err := Render(cm)
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}

	if opts.OutputPath == "" {
		opts.OutputPath = "CODEMAP.md"
	}
	if opts.PathsOutputPath == "" {
		opts.PathsOutputPath = "CODEMAP.paths"
	}

	// Write markdown output
	outputPath := filepath.Join(root, opts.OutputPath)
	if err := os.WriteFile(outputPath, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("write output: %w", err)
	}

	// Write paths output
	if !opts.DisablePaths {
		pathsContent := RenderPaths(cm)
		pathsPath := filepath.Join(root, opts.PathsOutputPath)
		if err := os.WriteFile(pathsPath, []byte(pathsContent), 0644); err != nil {
			return nil, fmt.Errorf("write paths output: %w", err)
		}
	}

	return cm, nil
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
