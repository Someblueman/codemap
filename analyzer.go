package main

import (
	"bufio"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Analyze walks the project and extracts package information.
func Analyze(ctx context.Context, opts Options) (*Codemap, error) {
	idx, err := BuildFileIndex(ctx, opts.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("build file index: %w", err)
	}

	analyzer := GoAnalyzer{}
	return analyzer.Analyze(ctx, AnalysisInput{
		Root:    idx.Root,
		Index:   idx,
		Options: opts,
	})
}

func analyzeGoWithIndex(ctx context.Context, root string, idx *FileIndex, opts Options) (*Codemap, error) {
	pkgDirs := findPackageDirsFromIndex(idx, opts.IncludeTests)

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

	sort.Slice(packages, func(i, j int) bool {
		return packages[i].RelativePath < packages[j].RelativePath
	})

	concerns, err := buildConcerns(idx, opts.Concerns, opts.ConcernExampleLimit)
	if err != nil {
		return nil, fmt.Errorf("build concerns: %w", err)
	}

	return &Codemap{
		ProjectRoot: root,
		Packages:    packages,
		Concerns:    concerns,
	}, nil
}

func findPackageDirsFromIndex(idx *FileIndex, includeTests bool) []string {
	seen := make(map[string]struct{})
	for _, rec := range idx.Files {
		if !rec.IsGo {
			continue
		}
		if !includeTests && rec.IsTest {
			continue
		}
		dir := filepath.Dir(rec.AbsPath)
		seen[dir] = struct{}{}
	}

	dirs := make([]string, 0, len(seen))
	for dir := range seen {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}

func analyzePackage(fset *token.FileSet, root, dir, modulePath string, opts Options) (*Package, error) {
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
		return nil, nil
	}

	relPath, err := filepath.Rel(root, dir)
	if err != nil {
		relPath = dir
	}
	relPath = filepath.ToSlash(relPath)

	importPath := relPath
	if modulePath != "" {
		if relPath == "." {
			importPath = modulePath
		} else {
			importPath = modulePath + "/" + relPath
		}
	}

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

		lineCount := fset.Position(file.End()).Line
		if lineCount < 0 {
			lineCount = 0
		}
		totalLines += lineCount

		fileDoc := ""
		if file.Doc != nil {
			fileDoc = strings.TrimSpace(file.Doc.Text())
		}

		if basename == "doc.go" && file.Doc != nil {
			purpose = extractFirstSentence(file.Doc.Text())
		} else if purpose == "" && file.Doc != nil {
			purpose = extractFirstSentence(file.Doc.Text())
		}

		for _, impSpec := range file.Imports {
			if impSpec.Path == nil {
				continue
			}
			imp := strings.Trim(impSpec.Path.Value, `"`)
			if isInternalImport(imp, modulePath) && !importsSeen[imp] {
				importsSeen[imp] = true
				internalImports = append(internalImports, imp)
			}
		}

		var keyTypes []string
		var keyFuncs []string
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					t, ok := spec.(*ast.TypeSpec)
					if !ok || !t.Name.IsExported() {
						continue
					}
					kind := "type"
					switch t.Type.(type) {
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
						Name:    t.Name.Name,
						Kind:    kind,
						Comment: comment,
					})
					keyTypes = append(keyTypes, t.Name.Name)
				}
			case *ast.FuncDecl:
				if d.Name.IsExported() && d.Recv == nil {
					keyFuncs = append(keyFuncs, d.Name.Name)
				}
			}
		}

		files = append(files, File{
			Name:      basename,
			LineCount: lineCount,
			Purpose:   extractFirstSentence(fileDoc),
			KeyTypes:  keyTypes,
			KeyFuncs:  keyFuncs,
		})

		score := scoreEntryPoint(basename, pkgName, keyTypes, keyFuncs)
		if score > entryScore {
			entryScore = score
			entryPoint = basename
		}
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Name < files[j].Name
	})

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

func extractFirstSentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	for i, r := range text {
		if r == '.' || r == '\n' {
			s := strings.TrimSpace(text[:i+1])
			if r == '.' {
				return s
			}
			return strings.TrimSuffix(s, ".")
		}
	}

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

	if base == pkgName {
		score += 100
	}
	if base == "main" || base == "server" || base == "client" {
		score += 50
	}
	for _, t := range types {
		if strings.EqualFold(t, pkgName) {
			score += 30
		}
	}
	for _, f := range funcs {
		if strings.HasPrefix(f, "New") {
			score += 20
		}
	}

	return score
}

func buildConcerns(idx *FileIndex, defs []ConcernDef, exampleLimit int) ([]Concern, error) {
	var concerns []Concern

	for _, def := range defs {
		matchers := make([]concernMatcher, 0, len(def.Patterns))
		for _, pattern := range def.Patterns {
			matcher, err := compileConcernPattern(pattern)
			if err != nil {
				continue
			}
			matchers = append(matchers, matcher)
		}
		if len(matchers) == 0 {
			continue
		}

		uniqueFiles := make(map[string]struct{})
		for _, rec := range idx.Files {
			for _, matcher := range matchers {
				if matcher.matches(rec.RelPath) {
					uniqueFiles[rec.RelPath] = struct{}{}
					break
				}
			}
		}

		totalFiles := len(uniqueFiles)
		if totalFiles == 0 {
			continue
		}

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

	return concerns, nil
}

type concernMatcher struct {
	pattern   string
	hasDouble bool
	prefix    string
	suffix    string
}

func compileConcernPattern(pattern string) (concernMatcher, error) {
	normalized := filepath.ToSlash(pattern)
	if !strings.Contains(normalized, "**") {
		return concernMatcher{pattern: normalized}, nil
	}

	parts := strings.Split(normalized, "**")
	if len(parts) != 2 {
		return concernMatcher{}, fmt.Errorf("unsupported pattern: %s", pattern)
	}

	return concernMatcher{
		pattern:   normalized,
		hasDouble: true,
		prefix:    strings.TrimSuffix(parts[0], "/"),
		suffix:    strings.TrimPrefix(parts[1], "/"),
	}, nil
}

func (m concernMatcher) matches(relPath string) bool {
	relPath = filepath.ToSlash(relPath)
	if m.hasDouble {
		if m.prefix != "" && relPath != m.prefix && !strings.HasPrefix(relPath, m.prefix+"/") {
			return false
		}
		if m.suffix == "" {
			return true
		}
		matched, err := path.Match(m.suffix, path.Base(relPath))
		return err == nil && matched
	}

	matched, err := path.Match(m.pattern, relPath)
	return err == nil && matched
}

func matchPattern(root, pattern string) ([]string, error) {
	idx, err := BuildFileIndex(context.Background(), root)
	if err != nil {
		return nil, err
	}

	matcher, err := compileConcernPattern(pattern)
	if err != nil {
		return nil, err
	}

	var matches []string
	for _, rec := range idx.Files {
		if matcher.matches(rec.RelPath) {
			matches = append(matches, rec.AbsPath)
		}
	}
	return matches, nil
}

func matchDoubleGlob(root, pattern string) ([]string, error) {
	matcher, err := compileConcernPattern(pattern)
	if err != nil {
		return nil, err
	}
	if !matcher.hasDouble {
		return nil, fmt.Errorf("unsupported pattern: %s", pattern)
	}
	return matchPattern(root, pattern)
}
