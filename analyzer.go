package main

import (
	"bufio"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Analyze walks the project and extracts package information.
func Analyze(ctx context.Context, opts Options) (*Codemap, error) {
	root, err := filepath.Abs(opts.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve project root: %w", err)
	}

	// Find all Go packages
	pkgDirs, err := findPackageDirs(root, opts.IncludeTests)
	if err != nil {
		return nil, fmt.Errorf("find packages: %w", err)
	}

	var packages []Package
	fset := token.NewFileSet()
	modulePath := findModulePath(root)

	for _, dir := range pkgDirs {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		pkg, err := analyzePackage(fset, root, dir, modulePath, opts)
		if err != nil {
			if opts.Verbose {
				fmt.Fprintf(os.Stderr, "warning: skipping %s: %v\n", dir, err)
			}
			continue
		}
		if pkg != nil {
			packages = append(packages, *pkg)
		}
	}

	// Sort by relative path
	sort.Slice(packages, func(i, j int) bool {
		return packages[i].RelativePath < packages[j].RelativePath
	})

	// Build concerns from file patterns
	concerns, err := buildConcerns(root, opts.Concerns, opts.ConcernExampleLimit)
	if err != nil {
		return nil, fmt.Errorf("build concerns: %w", err)
	}

	return &Codemap{
		ProjectRoot: root,
		Packages:    packages,
		Concerns:    concerns,
	}, nil
}

func findPackageDirs(root string, includeTests bool) ([]string, error) {
	var dirs []string
	seen := make(map[string]bool)

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip hidden directories and vendor
		name := info.Name()
		if info.IsDir() {
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "workspace" {
				return filepath.SkipDir
			}
			return nil
		}

		// Only consider Go files
		if !strings.HasSuffix(name, ".go") {
			return nil
		}

		// Skip test files if not included
		if !includeTests && strings.HasSuffix(name, "_test.go") {
			return nil
		}

		dir := filepath.Dir(path)
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}

		return nil
	})

	return dirs, err
}

func analyzePackage(fset *token.FileSet, root, dir, modulePath string, opts Options) (*Package, error) {
	// Parse the package
	mode := parser.ParseComments
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		name := fi.Name()
		if !strings.HasSuffix(name, ".go") {
			return false
		}
		if !opts.IncludeTests && strings.HasSuffix(name, "_test.go") {
			return false
		}
		return true
	}, mode)
	if err != nil {
		return nil, err
	}

	// Find the main package (skip _test packages)
	var pkgName string
	var pkgAST *ast.Package
	for name, pkg := range pkgs {
		if strings.HasSuffix(name, "_test") {
			continue
		}
		pkgName = name
		pkgAST = pkg
		break
	}

	if pkgAST == nil {
		return nil, nil // No non-test package
	}

	relPath, err := filepath.Rel(root, dir)
	if err != nil {
		relPath = dir
	}
	relPath = filepath.ToSlash(relPath)

	// Calculate import path
	importPath := relPath
	if modulePath != "" {
		if relPath == "." {
			importPath = modulePath
		} else {
			importPath = modulePath + "/" + relPath
		}
	}

	// Analyze files
	var files []File
	var totalLines int
	var allTypes []TypeInfo
	var internalImports []string
	importsSeen := make(map[string]bool)
	var purpose string
	entryPoint := ""
	entryScore := -1

	var filenames []string
	for filename := range pkgAST.Files {
		filenames = append(filenames, filename)
	}
	sort.Strings(filenames)

	for _, filename := range filenames {
		file := pkgAST.Files[filename]
		basename := filepath.Base(filename)

		// Count lines
		lineCount, err := countLines(filename)
		if err != nil {
			continue
		}
		totalLines += lineCount

		// Extract file-level doc
		fileDoc := ""
		if file.Doc != nil {
			fileDoc = strings.TrimSpace(file.Doc.Text())
		}

		// Check for doc.go or package purpose
		if basename == "doc.go" && file.Doc != nil {
			purpose = extractFirstSentence(file.Doc.Text())
		} else if purpose == "" && file.Doc != nil {
			purpose = extractFirstSentence(file.Doc.Text())
		}

		// Extract exports
		var keyTypes []string
		var keyFuncs []string

		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							kind := "type"
							switch s.Type.(type) {
							case *ast.StructType:
								kind = "struct"
							case *ast.InterfaceType:
								kind = "interface"
							}
							comment := ""
							if d.Doc != nil {
								comment = extractFirstSentence(d.Doc.Text())
							}
							allTypes = append(allTypes, TypeInfo{
								Name:    s.Name.Name,
								Kind:    kind,
								Comment: comment,
							})
							keyTypes = append(keyTypes, s.Name.Name)
						}
					case *ast.ImportSpec:
						if s.Path != nil {
							imp := strings.Trim(s.Path.Value, `"`)
							// Track internal imports
							if isInternalImport(imp, modulePath) && !importsSeen[imp] {
								importsSeen[imp] = true
								internalImports = append(internalImports, imp)
							}
						}
					}
				}
			case *ast.FuncDecl:
				if d.Name.IsExported() && d.Recv == nil {
					keyFuncs = append(keyFuncs, d.Name.Name)
				}
			}
		}

		f := File{
			Name:      basename,
			LineCount: lineCount,
			Purpose:   extractFirstSentence(fileDoc),
			KeyTypes:  keyTypes,
			KeyFuncs:  keyFuncs,
		}
		files = append(files, f)

		// Determine entry point heuristic
		score := scoreEntryPoint(basename, pkgName, keyTypes, keyFuncs)
		if score > entryScore {
			entryScore = score
			entryPoint = basename
		}
	}

	// Sort files by name
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name < files[j].Name
	})

	// Only include file details for large packages
	var detailedFiles []File
	if len(files) >= opts.LargePackageFiles {
		detailedFiles = files
	}

	return &Package{
		ImportPath:    importPath,
		RelativePath:  relPath,
		Purpose:       purpose,
		FileCount:     len(files),
		LineCount:     totalLines,
		Files:         detailedFiles,
		ExportedTypes: allTypes,
		Imports:       internalImports,
		EntryPoint:    entryPoint,
	}, nil
}

func findModulePath(root string) string {
	modFile := filepath.Join(root, "go.mod")
	f, err := os.Open(modFile)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

func countLines(filename string) (int, error) {
	f, err := os.Open(filename)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		count++
	}
	return count, scanner.Err()
}

func extractFirstSentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// Find first sentence ending
	for i, r := range text {
		if r == '.' || r == '\n' {
			s := strings.TrimSpace(text[:i+1])
			if r == '.' {
				return s
			}
			return strings.TrimSuffix(s, ".")
		}
	}

	// No period found, return up to 100 chars
	if len(text) > 100 {
		return text[:100] + "..."
	}
	return text
}

func isInternalImport(imp, pkgImportPath string) bool {
	if pkgImportPath == "" {
		return false
	}
	return imp == pkgImportPath || strings.HasPrefix(imp, pkgImportPath+"/")
}

func scoreEntryPoint(filename, pkgName string, types, funcs []string) int {
	score := 0
	base := strings.TrimSuffix(filename, ".go")

	// Exact package name match
	if base == pkgName {
		score += 100
	}

	// Common entry point names
	if base == "main" || base == "server" || base == "client" {
		score += 50
	}

	// Has main type matching package
	for _, t := range types {
		if strings.EqualFold(t, pkgName) {
			score += 30
		}
	}

	// Has New function
	for _, f := range funcs {
		if strings.HasPrefix(f, "New") {
			score += 20
		}
	}

	return score
}

func buildConcerns(root string, defs []ConcernDef, exampleLimit int) ([]Concern, error) {
	var concerns []Concern

	for _, def := range defs {
		uniqueFiles := make(map[string]struct{})
		for _, pattern := range def.Patterns {
			matches, err := matchPattern(root, pattern)
			if err != nil {
				continue
			}
			for _, m := range matches {
				rel, err := filepath.Rel(root, m)
				if err != nil {
					rel = m
				}
				uniqueFiles[filepath.ToSlash(rel)] = struct{}{}
			}
		}

		totalFiles := len(uniqueFiles)
		if totalFiles > 0 {
			var examples []string
			if exampleLimit > 0 {
				all := make([]string, 0, totalFiles)
				for f := range uniqueFiles {
					all = append(all, f)
				}
				sort.Strings(all)
				if len(all) > exampleLimit {
					all = all[:exampleLimit]
				}
				examples = all
			}

			concerns = append(concerns, Concern{
				Name:       def.Name,
				Patterns:   def.Patterns,
				Files:      examples,
				TotalFiles: totalFiles,
			})
		}
	}

	return concerns, nil
}

func matchPattern(root, pattern string) ([]string, error) {
	// Handle ** glob patterns
	if strings.Contains(pattern, "**") {
		return matchDoubleGlob(root, pattern)
	}
	return filepath.Glob(filepath.Join(root, pattern))
}

func matchDoubleGlob(root, pattern string) ([]string, error) {
	var matches []string

	// Split on **
	parts := strings.Split(pattern, "**")
	if len(parts) != 2 {
		return nil, fmt.Errorf("unsupported pattern: %s", pattern)
	}

	prefix := strings.TrimSuffix(parts[0], "/")
	suffix := strings.TrimPrefix(parts[1], "/")

	startDir := root
	if prefix != "" {
		startDir = filepath.Join(root, prefix)
	}

	err := filepath.Walk(startDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") || info.Name() == "vendor" || info.Name() == "testdata" || info.Name() == "workspace" {
				return filepath.SkipDir
			}
			return nil
		}

		// Check if filename matches suffix pattern
		matched, err := filepath.Match(suffix, info.Name())
		if err != nil {
			return nil
		}
		if matched {
			matches = append(matches, path)
		}
		return nil
	})

	return matches, err
}
