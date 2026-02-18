package main

import "testing"

func TestResolveLanguageSpecsDefaultsToGo(t *testing.T) {
	specs, err := resolveLanguageSpecs(nil)
	if err != nil {
		t.Fatalf("resolveLanguageSpecs returned error: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected one default language, got %d", len(specs))
	}
	if specs[0].ID != languageGo {
		t.Fatalf("expected default language %q, got %q", languageGo, specs[0].ID)
	}
}

func TestResolveLanguageSpecsSupportsAliasesAndDedupes(t *testing.T) {
	specs, err := resolveLanguageSpecs([]string{"go", "ts", "rust", "go"})
	if err != nil {
		t.Fatalf("resolveLanguageSpecs returned error: %v", err)
	}
	if len(specs) != 3 {
		t.Fatalf("expected 3 resolved languages, got %d", len(specs))
	}

	got := []string{specs[0].ID, specs[1].ID, specs[2].ID}
	want := []string{languageGo, languageRust, languageTypeScript}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected resolved language %q at index %d, got %q", want[i], i, got[i])
		}
	}
}

func TestResolveLanguageSpecsRejectsUnknown(t *testing.T) {
	if _, err := resolveLanguageSpecs([]string{"python"}); err == nil {
		t.Fatal("expected error for unsupported language")
	}
}

func TestMatchLanguageForPathDetectsTests(t *testing.T) {
	goMatch, ok := matchLanguageForPath("service_test.go", allBuiltinLanguageSpecs())
	if !ok {
		t.Fatal("expected Go file to match")
	}
	if goMatch.ID != languageGo || !goMatch.IsTest {
		t.Fatalf("unexpected Go match: %#v", goMatch)
	}

	tsMatch, ok := matchLanguageForPath("index.spec.ts", allBuiltinLanguageSpecs())
	if !ok {
		t.Fatal("expected TypeScript file to match")
	}
	if tsMatch.ID != languageTypeScript || !tsMatch.IsTest {
		t.Fatalf("unexpected TypeScript match: %#v", tsMatch)
	}
}

func TestDominantLanguage(t *testing.T) {
	idx := &FileIndex{
		Files: []FileRecord{
			{Language: languageGo},
			{Language: languageGo},
			{Language: languageRust},
		},
	}

	if got := dominantLanguage(idx, languageGo); got != languageGo {
		t.Fatalf("expected dominant language %q, got %q", languageGo, got)
	}
}

func TestDominantLanguageTieBreaksDeterministically(t *testing.T) {
	idx := &FileIndex{
		Files: []FileRecord{
			{Language: languageTypeScript},
			{Language: languageRust},
			{Language: languageTypeScript},
			{Language: languageRust},
		},
	}

	for i := 0; i < 100; i++ {
		if got := dominantLanguage(idx, languageGo); got != languageRust {
			t.Fatalf("expected deterministic tie-break to pick %q, got %q", languageRust, got)
		}
	}
}
