package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
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

func analyzeGoWithIndex(ctx context.Context, root string, idx *FileIndex, opts Options, prevState, nextState *CodemapState) (*Codemap, error) {
	modulePath := findModulePath(root)
	entryByRel := stateEntryByRelPath(nextState)
	plans := buildPackagePlansFromIndex(root, idx, opts.IncludeTests, entryByRel)
	cachedByRel := cachedPackagesByPath(prevState, opts, modulePath)

	packageResults := make([]*Package, len(plans))
	jobs := make([]analysisJob, 0, len(plans))
	for i := range plans {
		plan := plans[i]
		if cached, ok := cachedByRel[plan.RelativePath]; ok && plan.Fingerprint != "" && cached.Fingerprint == plan.Fingerprint {
			pkg := cached.Package
			packageResults[i] = &pkg
			continue
		}
		jobs = append(jobs, analysisJob{
			index: i,
			dir:   plan.DirAbsPath,
		})
	}

	if err := analyzePackagesParallel(ctx, root, modulePath, opts, jobs, packageResults); err != nil {
		return nil, err
	}

	packages := make([]Package, 0, len(packageResults))
	for i := range packageResults {
		if packageResults[i] != nil {
			packages = append(packages, *packageResults[i])
		}
	}

	concerns, err := buildConcerns(idx, opts.Concerns, opts.ConcernExampleLimit)
	if err != nil {
		return nil, fmt.Errorf("build concerns: %w", err)
	}

	updateAnalysisCache(nextState, opts, modulePath, plans, packageResults)

	return &Codemap{
		ProjectRoot: root,
		Packages:    packages,
		Concerns:    concerns,
	}, nil
}

func findPackageDirsFromIndex(idx *FileIndex, includeTests bool) []string {
	plans := buildPackagePlansFromIndex("", idx, includeTests, nil)
	dirs := make([]string, 0, len(plans))
	for _, plan := range plans {
		dirs = append(dirs, plan.DirAbsPath)
	}
	return dirs
}

func analyzePackage(fset *token.FileSet, root, dir, modulePath string, opts Options) (*Package, error) {
	mode := parser.ParseComments | parser.SkipObjectResolution
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

	pkgNames := make([]string, 0, len(pkgs))
	for name := range pkgs {
		if !strings.HasSuffix(name, "_test") {
			pkgNames = append(pkgNames, name)
		}
	}
	if len(pkgNames) == 0 {
		return nil, nil
	}
	sort.Strings(pkgNames)
	pkgName := pkgNames[0]
	pkgAST := pkgs[pkgName]

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

	files := make([]File, 0, len(pkgAST.Files))
	var totalLines int
	allTypes := make([]TypeInfo, 0, len(pkgAST.Files))
	internalImports := make([]string, 0, len(pkgAST.Files))
	importsSeen := make(map[string]struct{}, len(pkgAST.Files))
	var purpose string
	entryPoint := ""
	entryScore := -1

	filenames := make([]string, 0, len(pkgAST.Files))
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
		filePurpose := extractFirstSentence(fileDoc)

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
			if _, seen := importsSeen[imp]; isInternalImport(imp, modulePath) && !seen {
				importsSeen[imp] = struct{}{}
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
			Purpose:   filePurpose,
			KeyTypes:  keyTypes,
			KeyFuncs:  keyFuncs,
		})

		score := scoreEntryPoint(basename, pkgName, keyTypes, keyFuncs)
		if score > entryScore {
			entryScore = score
			entryPoint = basename
		}
	}

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

type packagePlan struct {
	RelativePath string
	DirAbsPath   string
	FileRelPaths []string
	Fingerprint  string
}

type analysisJob struct {
	index int
	dir   string
}

type analysisResult struct {
	index int
	dir   string
	pkg   *Package
	err   error
}

func stateEntryByRelPath(state *CodemapState) map[string]StateEntry {
	if state == nil || len(state.Entries) == 0 {
		return nil
	}
	entriesByRel := make(map[string]StateEntry, len(state.Entries))
	for _, entry := range state.Entries {
		entriesByRel[entry.RelPath] = entry
	}
	return entriesByRel
}

func buildPackagePlansFromIndex(root string, idx *FileIndex, includeTests bool, entriesByRel map[string]StateEntry) []packagePlan {
	plansByRel := make(map[string]*packagePlan)
	for _, rec := range idx.Files {
		if !includeTests && rec.IsTest {
			continue
		}

		relDir := filepath.ToSlash(filepath.Dir(rec.RelPath))
		plan, ok := plansByRel[relDir]
		if !ok {
			absDir := filepath.Join(idx.Root, filepath.FromSlash(relDir))
			if root != "" {
				absDir = filepath.Join(root, filepath.FromSlash(relDir))
			}
			plan = &packagePlan{
				RelativePath: relDir,
				DirAbsPath:   absDir,
				FileRelPaths: make([]string, 0, 4),
			}
			plansByRel[relDir] = plan
		}
		plan.FileRelPaths = append(plan.FileRelPaths, rec.RelPath)
	}

	relPaths := make([]string, 0, len(plansByRel))
	for rel := range plansByRel {
		relPaths = append(relPaths, rel)
	}
	sort.Strings(relPaths)

	plans := make([]packagePlan, 0, len(relPaths))
	for _, rel := range relPaths {
		plan := plansByRel[rel]
		plan.Fingerprint = packageFingerprint(plan.FileRelPaths, entriesByRel)
		plans = append(plans, *plan)
	}
	return plans
}

func packageFingerprint(fileRelPaths []string, entriesByRel map[string]StateEntry) string {
	if len(fileRelPaths) == 0 || entriesByRel == nil {
		return ""
	}

	h := sha256.New()
	sep := []byte{0}
	for _, relPath := range fileRelPaths {
		entry, ok := entriesByRel[relPath]
		if !ok || entry.ContentHash == "" {
			return ""
		}
		_, _ = h.Write([]byte(relPath))
		_, _ = h.Write(sep)
		_, _ = h.Write([]byte(entry.ContentHash))
		_, _ = h.Write(sep)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func cachedPackagesByPath(prevState *CodemapState, opts Options, modulePath string) map[string]CachedPackage {
	if prevState == nil || prevState.Analysis == nil {
		return nil
	}
	cache := prevState.Analysis
	if cache.Version != analysisCacheVersionV2 ||
		cache.IncludeTests != opts.IncludeTests ||
		cache.LargePackageFiles != opts.LargePackageFiles ||
		cache.ModulePath != modulePath {
		return nil
	}

	byRel := make(map[string]CachedPackage, len(cache.Packages))
	for _, cachedPkg := range cache.Packages {
		byRel[cachedPkg.RelativePath] = cachedPkg
	}
	return byRel
}

func analyzePackagesParallel(ctx context.Context, root, modulePath string, opts Options, jobs []analysisJob, out []*Package) error {
	if len(jobs) == 0 {
		return nil
	}

	workerCount := runtime.GOMAXPROCS(0)
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > len(jobs) {
		workerCount = len(jobs)
	}

	if workerCount == 1 {
		for _, job := range jobs {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			pkg, err := analyzePackage(token.NewFileSet(), root, job.dir, modulePath, opts)
			if err != nil {
				if opts.Verbose {
					fmt.Fprintf(os.Stderr, "warning: skipping %s: %v\n", job.dir, err)
				}
				continue
			}
			out[job.index] = pkg
		}
		return nil
	}

	jobsCh := make(chan analysisJob)
	resultsCh := make(chan analysisResult, len(jobs))
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for job := range jobsCh {
			pkg, err := analyzePackage(token.NewFileSet(), root, job.dir, modulePath, opts)
			select {
			case resultsCh <- analysisResult{
				index: job.index,
				dir:   job.dir,
				pkg:   pkg,
				err:   err,
			}:
			case <-ctx.Done():
				return
			}
		}
	}

	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go worker()
	}

	go func() {
		defer close(jobsCh)
		for _, job := range jobs {
			select {
			case jobsCh <- job:
			case <-ctx.Done():
				return
			}
		}
	}()

	for i := 0; i < len(jobs); i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-resultsCh:
			if result.err != nil {
				if opts.Verbose {
					fmt.Fprintf(os.Stderr, "warning: skipping %s: %v\n", result.dir, result.err)
				}
				continue
			}
			out[result.index] = result.pkg
		}
	}

	wg.Wait()
	return nil
}

func updateAnalysisCache(nextState *CodemapState, opts Options, modulePath string, plans []packagePlan, packageResults []*Package) {
	if nextState == nil {
		return
	}

	cachedPkgs := make([]CachedPackage, 0, len(packageResults))
	for i := range packageResults {
		if packageResults[i] == nil || plans[i].Fingerprint == "" {
			continue
		}
		cachedPkgs = append(cachedPkgs, CachedPackage{
			RelativePath: plans[i].RelativePath,
			Fingerprint:  plans[i].Fingerprint,
			FileRelPaths: append([]string(nil), plans[i].FileRelPaths...),
			Package:      *packageResults[i],
		})
	}

	nextState.Analysis = &AnalysisCache{
		Version:           analysisCacheVersionV2,
		IncludeTests:      opts.IncludeTests,
		LargePackageFiles: opts.LargePackageFiles,
		ModulePath:        modulePath,
		Packages:          cachedPkgs,
	}
}
