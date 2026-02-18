package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const (
	languageGo         = "go"
	languageRust       = "rust"
	languageTypeScript = "typescript"
)

// LanguageSpec describes source file matching rules for a language.
type LanguageSpec struct {
	ID               string
	FileSuffixes     []string
	TestFileSuffixes []string
}

type languageMatch struct {
	ID     string
	IsTest bool
}

func defaultLanguageSpecs() []LanguageSpec {
	specs, err := resolveLanguageSpecs(DefaultAnalyzerRegistry().LanguageIDs())
	if err != nil {
		// Built-ins should always resolve.
		panic(err)
	}
	return specs
}

func resolveLanguageSpecs(ids []string) ([]LanguageSpec, error) {
	if len(ids) == 0 {
		return []LanguageSpec{builtinLanguageSpecs[languageGo]}, nil
	}

	seen := make(map[string]struct{}, len(ids))
	specs := make([]LanguageSpec, 0, len(ids))
	for _, raw := range ids {
		id := canonicalLanguageID(raw)
		spec, ok := builtinLanguageSpecs[id]
		if !ok {
			return nil, fmt.Errorf("unsupported language: %s", raw)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		specs = append(specs, spec)
	}

	sort.Slice(specs, func(i, j int) bool {
		return specs[i].ID < specs[j].ID
	})
	return specs, nil
}

func canonicalLanguageID(id string) string {
	normalized := strings.ToLower(strings.TrimSpace(id))
	switch normalized {
	case "ts":
		return languageTypeScript
	default:
		return normalized
	}
}

func matchLanguageForPath(path string, specs []LanguageSpec) (languageMatch, bool) {
	name := strings.ToLower(filepath.Base(path))
	for _, spec := range specs {
		for _, suffix := range spec.FileSuffixes {
			if strings.HasSuffix(name, strings.ToLower(suffix)) {
				return languageMatch{
					ID:     spec.ID,
					IsTest: hasAnySuffix(name, spec.TestFileSuffixes),
				}, true
			}
		}
	}
	return languageMatch{}, false
}

func inferLanguageForPath(path string) string {
	match, ok := matchLanguageForPath(path, allBuiltinLanguageSpecs())
	if !ok {
		return ""
	}
	return match.ID
}

func allBuiltinLanguageSpecs() []LanguageSpec {
	specs := make([]LanguageSpec, 0, len(builtinLanguageSpecs))
	for _, spec := range builtinLanguageSpecs {
		specs = append(specs, spec)
	}
	sort.Slice(specs, func(i, j int) bool {
		return specs[i].ID < specs[j].ID
	})
	return specs
}

func hasAnySuffix(value string, suffixes []string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(value, strings.ToLower(suffix)) {
			return true
		}
	}
	return false
}

func dominantLanguage(idx *FileIndex, fallback string) string {
	if idx == nil || len(idx.Files) == 0 {
		return fallback
	}

	counts := make(map[string]int)
	for _, rec := range idx.Files {
		if rec.Language == "" {
			continue
		}
		counts[rec.Language]++
	}
	if len(counts) == 0 {
		return fallback
	}

	bestID := fallback
	bestCount := -1
	for id, count := range counts {
		if count > bestCount || (count == bestCount && id < bestID) {
			bestID = id
			bestCount = count
		}
	}
	return bestID
}

var builtinLanguageSpecs = map[string]LanguageSpec{
	languageGo: {
		ID:               languageGo,
		FileSuffixes:     []string{".go"},
		TestFileSuffixes: []string{"_test.go"},
	},
	languageRust: {
		ID:               languageRust,
		FileSuffixes:     []string{".rs"},
		TestFileSuffixes: []string{"_test.rs"},
	},
	languageTypeScript: {
		ID: languageTypeScript,
		FileSuffixes: []string{
			".ts",
			".tsx",
			".mts",
			".cts",
		},
		TestFileSuffixes: []string{
			".test.ts",
			".spec.ts",
			".test.tsx",
			".spec.tsx",
			".test.mts",
			".spec.mts",
			".test.cts",
			".spec.cts",
		},
	},
}
