package codemap

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const (
	languageGo         = "go"
	languagePython     = "python"
	languageRust       = "rust"
	languageShell      = "shell"
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

var allBuiltinLanguageSpecList = buildAllBuiltinLanguageSpecs()

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
	case "py", "python3":
		return languagePython
	case "bash", "sh":
		return languageShell
	case "ts":
		return languageTypeScript
	default:
		return normalized
	}
}

func matchLanguageForPath(path string, specs []LanguageSpec) (languageMatch, bool) {
	if builtinMatch, ok := matchBuiltinLanguageForPath(path); ok {
		if languageEnabled(specs, builtinMatch.ID) {
			return builtinMatch, true
		}
		return languageMatch{}, false
	}

	name := strings.ToLower(filepath.Base(path))
	for _, spec := range specs {
		for _, suffix := range spec.FileSuffixes {
			lowerSuffix := suffix
			if suffix != strings.ToLower(suffix) {
				lowerSuffix = strings.ToLower(suffix)
			}
			if strings.HasSuffix(name, lowerSuffix) {
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
	match, ok := matchBuiltinLanguageForPath(path)
	if !ok {
		return ""
	}
	return match.ID
}

func allBuiltinLanguageSpecs() []LanguageSpec {
	return allBuiltinLanguageSpecList
}

func hasAnySuffix(value string, suffixes []string) bool {
	for _, suffix := range suffixes {
		lowerSuffix := suffix
		if suffix != strings.ToLower(suffix) {
			lowerSuffix = strings.ToLower(suffix)
		}
		if strings.HasSuffix(value, lowerSuffix) {
			return true
		}
	}
	return false
}

func matchBuiltinLanguageForPath(path string) (languageMatch, bool) {
	name := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasSuffix(name, ".go"):
		return languageMatch{
			ID:     languageGo,
			IsTest: strings.HasSuffix(name, "_test.go"),
		}, true
	case strings.HasSuffix(name, ".py"):
		return languageMatch{
			ID:     languagePython,
			IsTest: isPythonTestPathLike(name),
		}, true
	case strings.HasSuffix(name, ".rs"):
		return languageMatch{
			ID:     languageRust,
			IsTest: strings.HasSuffix(name, "_test.rs"),
		}, true
	case strings.HasSuffix(name, ".sh"),
		strings.HasSuffix(name, ".bash"),
		strings.HasSuffix(name, ".bats"):
		return languageMatch{
			ID:     languageShell,
			IsTest: isShellTestPathLike(name),
		}, true
	case strings.HasSuffix(name, ".ts"),
		strings.HasSuffix(name, ".tsx"),
		strings.HasSuffix(name, ".mts"),
		strings.HasSuffix(name, ".cts"):
		return languageMatch{
			ID:     languageTypeScript,
			IsTest: hasAnySuffix(name, builtinLanguageSpecs[languageTypeScript].TestFileSuffixes),
		}, true
	default:
		return languageMatch{}, false
	}
}

func isPythonTestPathLike(path string) bool {
	lower := strings.ToLower(path)
	base := filepath.Base(lower)
	return strings.HasSuffix(base, "_test.py") ||
		strings.HasSuffix(base, ".test.py") ||
		strings.HasSuffix(base, ".spec.py") ||
		(strings.HasSuffix(base, ".py") && strings.HasPrefix(base, "test_"))
}

func isShellTestPathLike(path string) bool {
	lower := strings.ToLower(path)
	base := filepath.Base(lower)
	if strings.HasSuffix(base, ".bats") {
		return true
	}
	return strings.HasSuffix(base, "_test.sh") ||
		strings.HasSuffix(base, ".test.sh") ||
		strings.HasSuffix(base, ".spec.sh") ||
		strings.HasSuffix(base, "_test.bash") ||
		strings.HasSuffix(base, ".test.bash") ||
		strings.HasSuffix(base, ".spec.bash") ||
		((strings.HasSuffix(base, ".sh") || strings.HasSuffix(base, ".bash")) && strings.HasPrefix(base, "test_"))
}

func languageEnabled(specs []LanguageSpec, id string) bool {
	for _, spec := range specs {
		if spec.ID == id {
			return true
		}
	}
	return false
}

func buildAllBuiltinLanguageSpecs() []LanguageSpec {
	specs := make([]LanguageSpec, 0, len(builtinLanguageSpecs))
	for _, spec := range builtinLanguageSpecs {
		specs = append(specs, spec)
	}
	sort.Slice(specs, func(i, j int) bool {
		return specs[i].ID < specs[j].ID
	})
	return specs
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
	ids := make([]string, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		count := counts[id]
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
	languagePython: {
		ID:           languagePython,
		FileSuffixes: []string{".py"},
		TestFileSuffixes: []string{
			"_test.py",
			".test.py",
			".spec.py",
		},
	},
	languageRust: {
		ID:               languageRust,
		FileSuffixes:     []string{".rs"},
		TestFileSuffixes: []string{"_test.rs"},
	},
	languageShell: {
		ID: languageShell,
		FileSuffixes: []string{
			".sh",
			".bash",
			".bats",
		},
		TestFileSuffixes: []string{
			"_test.sh",
			".test.sh",
			".spec.sh",
			"_test.bash",
			".test.bash",
			".spec.bash",
			".bats",
		},
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
