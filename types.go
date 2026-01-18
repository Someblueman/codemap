package main

import "time"

// Codemap represents the full analysis of a codebase.
type Codemap struct {
	ProjectRoot string
	GeneratedAt time.Time
	ContentHash string
	Packages    []Package
	Concerns    []Concern
}

// Package represents a Go package with its metadata.
type Package struct {
	ImportPath    string
	RelativePath  string // e.g., "internal/supervisor"
	Purpose       string // From doc.go or package comment
	FileCount     int
	LineCount     int
	Files         []File // Only populated for large packages
	ExportedTypes []TypeInfo
	Imports       []string // Internal imports only
	EntryPoint    string   // Suggested first file to read
}

// File represents a Go source file.
type File struct {
	Name      string
	LineCount int
	Purpose   string   // From file-level comment
	KeyTypes  []string // Exported types defined in this file
	KeyFuncs  []string // Exported functions defined in this file
}

// TypeInfo represents an exported type.
type TypeInfo struct {
	Name    string
	Kind    string // struct, interface, alias, func
	Comment string
}

// Concern represents a cross-cutting concern grouping files.
type Concern struct {
	Name       string
	Patterns   []string
	Files      []string
	TotalFiles int
	Note       string
}

// ConcernDef defines a concern pattern to match.
type ConcernDef struct {
	Name     string
	Patterns []string
}

// Options configures codemap generation.
type Options struct {
	ProjectRoot         string
	OutputPath          string // Default: "CODEMAP.md"
	PathsOutputPath     string // Default: "CODEMAP.paths"
	LargePackageFiles   int    // Threshold for detailed file listing
	IncludeTests        bool
	Concerns            []ConcernDef
	ConcernExampleLimit int // Max files stored per concern (0 = none)
	DisablePaths        bool
	Verbose             bool
}

// DefaultOptions returns sensible defaults.
func DefaultOptions() Options {
	return Options{
		ProjectRoot:         ".",
		OutputPath:          "CODEMAP.md",
		PathsOutputPath:     "CODEMAP.paths",
		LargePackageFiles:   10,
		IncludeTests:        false,
		Concerns:            defaultConcerns,
		ConcernExampleLimit: 0,
		DisablePaths:        false,
		Verbose:             false,
	}
}

var defaultConcerns = []ConcernDef{
	{Name: "Error Handling", Patterns: []string{"**/error*.go", "**/recovery*.go"}},
	{Name: "Testing", Patterns: []string{"**/*_test.go"}},
	{Name: "CLI", Patterns: []string{"cmd/**/*.go", "**/cli_*.go"}},
	{Name: "Configuration", Patterns: []string{"**/config*.go", "**/options*.go"}},
}
