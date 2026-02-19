package codemap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyze(t *testing.T) {
	// Create a temp directory with Go files
	tmpDir := t.TempDir()

	// Create a simple Go package
	pkgDir := filepath.Join(tmpDir, "internal", "foo")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a Go file
	goFile := `// Package foo provides test functionality.
package foo

// Bar is an exported type.
type Bar struct {
	Name string
}

// NewBar creates a new Bar.
func NewBar(name string) *Bar {
	return &Bar{Name: name}
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "foo.go"), []byte(goFile), 0644); err != nil {
		t.Fatal(err)
	}

	// Write go.mod
	goMod := "module example.com/test\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	opts := Options{
		ProjectRoot:       tmpDir,
		LargePackageFiles: 10,
	}

	cm, err := Analyze(ctx, opts)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	if len(cm.Packages) != 1 {
		t.Errorf("expected 1 package, got %d", len(cm.Packages))
	}

	pkg := cm.Packages[0]
	if pkg.RelativePath != "internal/foo" {
		t.Errorf("expected internal/foo, got %s", pkg.RelativePath)
	}

	if pkg.Purpose == "" || !strings.Contains(pkg.Purpose, "test functionality") {
		t.Errorf("expected purpose to contain 'test functionality', got %q", pkg.Purpose)
	}

	if len(pkg.ExportedTypes) != 1 {
		t.Errorf("expected 1 exported type, got %d", len(pkg.ExportedTypes))
	}

	if pkg.ExportedTypes[0].Name != "Bar" {
		t.Errorf("expected Bar, got %s", pkg.ExportedTypes[0].Name)
	}

	if pkg.EntryPoint != "foo.go" {
		t.Errorf("expected foo.go entry point, got %s", pkg.EntryPoint)
	}
}

func TestComputeHash(t *testing.T) {
	tmpDir := t.TempDir()

	// Create some files
	if err := os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "b.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	hash1, err := ComputeHash(ctx, tmpDir)
	if err != nil {
		t.Fatalf("ComputeHash failed: %v", err)
	}

	if len(hash1) != 64 {
		t.Errorf("expected 64 char hash, got %d", len(hash1))
	}

	// Same content should give same hash
	hash2, err := ComputeHash(ctx, tmpDir)
	if err != nil {
		t.Fatalf("ComputeHash failed: %v", err)
	}
	if hash1 != hash2 {
		t.Error("same content should give same hash")
	}

	// Different content should give different hash
	if err := os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package foo"), 0644); err != nil {
		t.Fatal(err)
	}
	hash3, err := ComputeHash(ctx, tmpDir)
	if err != nil {
		t.Fatalf("ComputeHash failed: %v", err)
	}
	if hash1 == hash3 {
		t.Error("different content should give different hash")
	}
}

func TestIsStale(t *testing.T) {
	tmpDir := t.TempDir()

	// Create go.mod
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a Go file
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	opts := Options{
		ProjectRoot: tmpDir,
		OutputPath:  "CODEMAP.md",
	}

	// No CODEMAP.md should be stale
	stale, err := IsStale(ctx, opts)
	if err != nil {
		t.Fatalf("IsStale failed: %v", err)
	}
	if !stale {
		t.Error("expected stale when no CODEMAP.md exists")
	}

	// Generate CODEMAP.md
	opts.LargePackageFiles = 10
	if _, err := Generate(ctx, opts); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Should not be stale now
	stale, err = IsStale(ctx, opts)
	if err != nil {
		t.Fatalf("IsStale failed: %v", err)
	}
	if stale {
		t.Error("expected not stale after generation")
	}

	// Modify source
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n// changed\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Should be stale now
	stale, err = IsStale(ctx, opts)
	if err != nil {
		t.Fatalf("IsStale failed: %v", err)
	}
	if !stale {
		t.Error("expected stale after source modification")
	}
}

func TestRender(t *testing.T) {
	cm := &Codemap{
		ContentHash: "abc123",
		Packages: []Package{
			{
				RelativePath:  "internal/foo",
				FileCount:     2,
				LineCount:     100,
				Purpose:       "Foo functionality",
				EntryPoint:    "foo.go",
				ExportedTypes: []TypeInfo{{Name: "Foo", Kind: "struct"}},
			},
		},
		Concerns: []Concern{
			{
				Name:       "Testing",
				TotalFiles: 1,
			},
		},
	}

	content, err := Render(cm)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	if !strings.Contains(content, "codemap-hash: abc123") {
		t.Error("expected hash in output")
	}

	if !strings.Contains(content, "internal/foo") {
		t.Error("expected package path in output")
	}

	if !strings.Contains(content, "Foo functionality") {
		t.Error("expected purpose in output")
	}

	if !strings.Contains(content, "Testing") {
		t.Error("expected concern in output")
	}
}

func TestRenderPaths(t *testing.T) {
	cm := &Codemap{
		ContentHash: "abc123",
		Packages: []Package{
			{
				RelativePath: "internal/foo",
				Purpose:      "Foo functionality",
				EntryPoint:   "foo.go",
			},
		},
	}

	content := RenderPaths(cm)
	if !strings.Contains(content, "codemap-hash: abc123") {
		t.Error("expected hash in output")
	}
	if !strings.Contains(content, "internal/foo\tinternal/foo/foo.go") {
		t.Error("expected package and entry file path in output")
	}
}

func TestExtractFirstSentence(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Simple sentence.", "Simple sentence."},
		{"First. Second.", "First."},
		{"First\nSecond", "First"},
		{"", ""},
		{strings.Repeat("a", 150), strings.Repeat("a", 100) + "..."},
	}

	for _, tt := range tests {
		got := extractFirstSentence(tt.input)
		if got != tt.expected {
			t.Errorf("extractFirstSentence(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"this is longer", 10, "this is..."},
		{"exactly10!", 10, "exactly10!"},
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.expected)
		}
	}
}

func TestScoreEntryPoint(t *testing.T) {
	// Package name match should score highest
	score1 := scoreEntryPoint("foo.go", "foo", nil, nil)
	score2 := scoreEntryPoint("bar.go", "foo", nil, nil)

	if score1 <= score2 {
		t.Errorf("expected foo.go to score higher than bar.go for package foo")
	}

	// New function should add score
	score3 := scoreEntryPoint("bar.go", "foo", nil, []string{"NewFoo"})
	if score3 <= score2 {
		t.Errorf("expected NewFoo to increase score")
	}
}

func TestMatchDoubleGlob(t *testing.T) {
	tmpDir := t.TempDir()

	// Create nested structure
	nested := filepath.Join(tmpDir, "a", "b")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "error.go"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "error_handler.go"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	matches, err := matchDoubleGlob(tmpDir, "**/error*.go")
	if err != nil {
		t.Fatalf("matchDoubleGlob failed: %v", err)
	}

	if len(matches) != 2 {
		t.Errorf("expected 2 matches, got %d", len(matches))
	}
}
