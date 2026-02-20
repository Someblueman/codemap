package codemap

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	pythonClassPattern          = regexp.MustCompile(`^class\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	pythonFuncPattern           = regexp.MustCompile(`^def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	pythonAsyncFuncPattern      = regexp.MustCompile(`^async\s+def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	pythonImportPattern         = regexp.MustCompile(`^import\s+(.+)$`)
	pythonFromImportPattern     = regexp.MustCompile(`^from\s+([\.A-Za-z_][A-Za-z0-9_\.]*)\s+import\s+`)
	pythonConstantAssignPattern = regexp.MustCompile(`^([A-Z][A-Z0-9_]*)\s*=`)
	pythonIdentifierPattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	pythonSetupPyNamePattern    = regexp.MustCompile(`name\s*=\s*[\"']([^\"']+)[\"']`)
)

// PythonAnalyzer is the analyzer implementation for Python projects.
type PythonAnalyzer struct{}

func (PythonAnalyzer) LanguageID() string { return languagePython }

func (PythonAnalyzer) Analyze(ctx context.Context, in AnalysisInput) (*Codemap, error) {
	if in.Index == nil {
		return nil, fmt.Errorf("missing file index")
	}
	return analyzePythonWithIndex(ctx, in.Root, in.Index, in.Options, in.PrevState, in.NextState)
}

func analyzePythonWithIndex(ctx context.Context, root string, idx *FileIndex, opts Options, prevState, nextState *CodemapState) (*Codemap, error) {
	entryByRel := stateEntryByRelPath(nextState)
	plans, err := buildPythonPackagePlans(root, idx, opts.IncludeTests, entryByRel)
	if err != nil {
		return nil, err
	}

	const modulePath = languagePython
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

	if err := analyzePackagePlansParallel(ctx, opts, jobs, packageResults, func(job analysisJob) (*Package, error) {
		plan := plans[job.index]
		packageName := readPythonPackageName(plan.DirAbsPath, plan.RelativePath)
		pkg, err := analyzePythonPackage(root, plan, packageName, opts)
		if err != nil {
			return nil, fmt.Errorf("analyze python package %s: %w", plan.RelativePath, err)
		}
		return pkg, nil
	}); err != nil {
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

func buildPythonPackagePlans(root string, idx *FileIndex, includeTests bool, entriesByRel map[string]StateEntry) ([]packagePlan, error) {
	plansByRel := make(map[string]*packagePlan)
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	rootByDir := map[string]string{
		rootAbs: rootAbs,
	}
	manifestExistsByRel := make(map[string]bool)
	packageRootBySourceDir := make(map[string]struct {
		rel string
		abs string
	})

	for _, rec := range idx.Files {
		if rec.Language != languagePython {
			continue
		}
		if !includeTests && isPythonTestPath(rec.RelPath, rec.IsTest) {
			continue
		}

		sourceDir := filepath.Dir(rec.AbsPath)
		pkgRoot, ok := packageRootBySourceDir[sourceDir]
		if !ok {
			if guessedRel, guessed := likelyPackageRootRelBySegments(rec.RelPath, []string{"src", "tests", "test"}); guessed {
				useGuess := pathContainsSegment(rec.RelPath, "src")
				if !useGuess {
					exists, err := pythonManifestExistsAtCached(rootAbs, guessedRel, manifestExistsByRel)
					if err != nil {
						return nil, err
					}
					useGuess = exists
				}
				if useGuess {
					guessedAbs := rootAbs
					if guessedRel != "." {
						guessedAbs = filepath.Join(rootAbs, filepath.FromSlash(guessedRel))
					}
					pkgRoot = struct {
						rel string
						abs string
					}{
						rel: guessedRel,
						abs: guessedAbs,
					}
					packageRootBySourceDir[sourceDir] = pkgRoot
				}
			}
		}
		if !ok {
			if pkgRoot.rel == "" {
				pkgAbs, err := resolvePythonPackageRootDirCached(rootAbs, sourceDir, rootByDir, manifestExistsByRel)
				if err != nil {
					return nil, err
				}
				pkgRel, err := relativePathWithinRoot(rootAbs, pkgAbs)
				if err != nil {
					return nil, err
				}
				pkgRoot = struct {
					rel string
					abs string
				}{
					rel: pkgRel,
					abs: pkgAbs,
				}
				packageRootBySourceDir[sourceDir] = pkgRoot
			}
		}
		pkgRel, pkgAbs := pkgRoot.rel, pkgRoot.abs

		plan, ok := plansByRel[pkgRel]
		if !ok {
			plan = &packagePlan{
				RelativePath: pkgRel,
				DirAbsPath:   pkgAbs,
				FileRelPaths: make([]string, 0, 4),
			}
			plansByRel[pkgRel] = plan
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
		sort.Strings(plan.FileRelPaths)
		plan.Fingerprint = packageFingerprint(plan.FileRelPaths, entriesByRel)
		plans = append(plans, *plan)
	}

	return plans, nil
}

func analyzePythonPackage(root string, plan packagePlan, packageName string, opts Options) (*Package, error) {
	if len(plan.FileRelPaths) == 0 {
		return nil, nil
	}

	fileRelPaths := append([]string(nil), plan.FileRelPaths...)
	sort.Strings(fileRelPaths)

	files := make([]File, 0, len(fileRelPaths))
	allTypes := make([]TypeInfo, 0, len(fileRelPaths))
	importsSeen := make(map[string]struct{}, len(fileRelPaths))
	totalLines := 0
	purpose := ""
	entryPoint := ""
	entryScore := -1
	importPrefix := pythonImportPrefix(packageName, plan.RelativePath)

	for _, relPath := range fileRelPaths {
		absPath := filepath.Join(root, filepath.FromSlash(relPath))
		content, err := os.ReadFile(absPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", relPath, err)
		}

		lineCount := lineCountBytes(content)
		totalLines += lineCount

		withinPackage := relPath
		if plan.RelativePath != "." {
			prefix := plan.RelativePath + "/"
			if strings.HasPrefix(relPath, prefix) {
				withinPackage = strings.TrimPrefix(relPath, prefix)
			}
		}

		filePurpose := extractPythonFilePurpose(content)
		if purpose == "" && filePurpose != "" {
			purpose = filePurpose
		}

		typeInfos, keyTypes, keyFuncs, imports := parsePythonFileSymbols(content, withinPackage)
		allTypes = append(allTypes, typeInfos...)
		for _, imp := range imports {
			if isPythonInternalImport(imp, importPrefix) {
				importsSeen[imp] = struct{}{}
			}
		}

		files = append(files, File{
			Name:      withinPackage,
			LineCount: lineCount,
			Purpose:   filePurpose,
			KeyTypes:  keyTypes,
			KeyFuncs:  keyFuncs,
		})

		score := scorePythonEntryPoint(withinPackage, keyTypes, keyFuncs)
		if score > entryScore || (score == entryScore && (entryPoint == "" || withinPackage < entryPoint)) {
			entryScore = score
			entryPoint = withinPackage
		}
	}

	if entryPoint == "" && len(files) > 0 {
		entryPoint = files[0].Name
	}
	if purpose == "" && packageName != "" {
		purpose = "Python package " + packageName
	}

	internalImports := make([]string, 0, len(importsSeen))
	for imp := range importsSeen {
		internalImports = append(internalImports, imp)
	}
	sort.Strings(internalImports)
	sort.Slice(allTypes, func(i, j int) bool {
		return allTypes[i].Name < allTypes[j].Name
	})

	var detailedFiles []File
	if len(files) >= opts.LargePackageFiles {
		detailedFiles = files
	}

	return &Package{
		ImportPath:    packageName,
		RelativePath:  plan.RelativePath,
		Purpose:       purpose,
		FileCount:     len(files),
		LineCount:     totalLines,
		Files:         detailedFiles,
		ExportedTypes: allTypes,
		Imports:       internalImports,
		EntryPoint:    entryPoint,
	}, nil
}

func findPythonPackageRoot(root, fileAbsPath string) (string, string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve root: %w", err)
	}

	rootsByDir := map[string]string{rootAbs: rootAbs}
	manifestExistsByRel := make(map[string]bool)
	pkgAbs, err := resolvePythonPackageRootDirCached(rootAbs, filepath.Dir(fileAbsPath), rootsByDir, manifestExistsByRel)
	if err != nil {
		return "", "", err
	}
	pkgRel, err := relativePathWithinRoot(rootAbs, pkgAbs)
	if err != nil {
		return "", "", err
	}
	return pkgRel, pkgAbs, nil
}

func resolvePythonPackageRootDirCached(rootAbs, startDir string, rootsByDir map[string]string, manifestExistsByRel map[string]bool) (string, error) {
	dir := startDir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(rootAbs, dir)
	}
	dir = filepath.Clean(dir)

	visited := make([]string, 0, 8)
	for {
		if cachedRoot, ok := rootsByDir[dir]; ok {
			for _, visitedDir := range visited {
				rootsByDir[visitedDir] = cachedRoot
			}
			return cachedRoot, nil
		}
		visited = append(visited, dir)

		relDir, err := relativePathWithinRoot(rootAbs, dir)
		if err != nil {
			return "", err
		}
		hasManifest, err := pythonManifestExistsAtCached(rootAbs, relDir, manifestExistsByRel)
		if err != nil {
			return "", err
		}
		if hasManifest {
			for _, visitedDir := range visited {
				rootsByDir[visitedDir] = dir
			}
			return dir, nil
		}

		if dir == rootAbs {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	for _, visitedDir := range visited {
		rootsByDir[visitedDir] = rootAbs
	}
	return rootAbs, nil
}

func pythonManifestExistsAtCached(rootAbs, relDir string, existsByRelDir map[string]bool) (bool, error) {
	if relDir == "" {
		relDir = "."
	}
	if exists, ok := existsByRelDir[relDir]; ok {
		return exists, nil
	}

	absDir := rootAbs
	if relDir != "." {
		absDir = filepath.Join(rootAbs, filepath.FromSlash(relDir))
	}

	manifestNames := []string{"pyproject.toml", "setup.cfg", "setup.py"}
	for _, manifestName := range manifestNames {
		info, err := os.Stat(filepath.Join(absDir, manifestName))
		if err == nil {
			exists := !info.IsDir()
			existsByRelDir[relDir] = exists
			return exists, nil
		}
		if !os.IsNotExist(err) {
			return false, err
		}
	}

	existsByRelDir[relDir] = false
	return false, nil
}

func readPythonPackageName(packageAbsPath, packageRelPath string) string {
	if name := readPythonPackageNameFromPyproject(filepath.Join(packageAbsPath, "pyproject.toml")); name != "" {
		return name
	}
	if name := readPythonPackageNameFromSetupCfg(filepath.Join(packageAbsPath, "setup.cfg")); name != "" {
		return name
	}
	if name := readPythonPackageNameFromSetupPy(filepath.Join(packageAbsPath, "setup.py")); name != "" {
		return name
	}
	return fallbackPythonPackageName(packageAbsPath, packageRelPath)
}

func readPythonPackageNameFromPyproject(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	section := ""
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			continue
		}
		if !strings.EqualFold(section, "project") && !strings.EqualFold(section, "tool.poetry") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(line), "name") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[1])
		name = strings.Trim(name, "\"'")
		if name != "" {
			return name
		}
	}

	return ""
}

func readPythonPackageNameFromSetupCfg(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	inMetadata := false
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			inMetadata = strings.EqualFold(section, "metadata")
			continue
		}
		if !inMetadata {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(parts[0]), "name") {
			continue
		}
		name := strings.TrimSpace(parts[1])
		name = strings.Trim(name, "\"'")
		if name != "" {
			return name
		}
	}

	return ""
}

func readPythonPackageNameFromSetupPy(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	match := pythonSetupPyNamePattern.FindSubmatch(content)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(string(match[1]))
}

func fallbackPythonPackageName(packageAbsPath, packageRelPath string) string {
	if packageRelPath == "." {
		return filepath.Base(packageAbsPath)
	}
	return filepath.Base(packageRelPath)
}

func isPythonTestPath(relPath string, fileMatchTest bool) bool {
	if fileMatchTest {
		return true
	}
	lower := strings.ToLower(relPath)
	base := filepath.Base(lower)
	if strings.HasPrefix(lower, "tests/") || strings.Contains(lower, "/tests/") {
		return true
	}
	if strings.HasPrefix(lower, "test/") || strings.Contains(lower, "/test/") {
		return true
	}
	return isPythonTestPathLike(base)
}

func parsePythonFileSymbols(content []byte, _ string) ([]TypeInfo, []string, []string, []string) {
	typeInfos := make([]TypeInfo, 0)
	keyTypes := make([]string, 0)
	keyFuncs := make([]string, 0)
	imports := make([]string, 0)

	typesSeen := make(map[string]struct{})
	funcsSeen := make(map[string]struct{})
	importsSeen := make(map[string]struct{})

	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Keep symbol extraction focused on top-level definitions.
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}

		if match := pythonClassPattern.FindStringSubmatch(trimmed); len(match) == 2 {
			name := match[1]
			if isPublicPythonSymbol(name) {
				if _, exists := typesSeen[name]; !exists {
					typesSeen[name] = struct{}{}
					typeInfos = append(typeInfos, TypeInfo{Name: name, Kind: "class"})
					keyTypes = append(keyTypes, name)
				}
			}
			continue
		}

		if match := pythonAsyncFuncPattern.FindStringSubmatch(trimmed); len(match) == 2 {
			name := match[1]
			if isPublicPythonSymbol(name) {
				if _, exists := funcsSeen[name]; !exists {
					funcsSeen[name] = struct{}{}
					keyFuncs = append(keyFuncs, name)
				}
			}
			continue
		}
		if match := pythonFuncPattern.FindStringSubmatch(trimmed); len(match) == 2 {
			name := match[1]
			if isPublicPythonSymbol(name) {
				if _, exists := funcsSeen[name]; !exists {
					funcsSeen[name] = struct{}{}
					keyFuncs = append(keyFuncs, name)
				}
			}
			continue
		}

		if match := pythonConstantAssignPattern.FindStringSubmatch(trimmed); len(match) == 2 {
			name := match[1]
			if _, exists := funcsSeen[name]; !exists {
				funcsSeen[name] = struct{}{}
				keyFuncs = append(keyFuncs, name)
			}
			continue
		}

		if match := pythonFromImportPattern.FindStringSubmatch(trimmed); len(match) == 2 {
			imp := strings.TrimSpace(match[1])
			imp = strings.Trim(imp, "()")
			if imp != "" {
				if _, exists := importsSeen[imp]; !exists {
					importsSeen[imp] = struct{}{}
					imports = append(imports, imp)
				}
			}
			continue
		}

		if match := pythonImportPattern.FindStringSubmatch(trimmed); len(match) == 2 {
			for _, imp := range parsePythonImportStatement(match[1]) {
				if _, exists := importsSeen[imp]; !exists {
					importsSeen[imp] = struct{}{}
					imports = append(imports, imp)
				}
			}
		}
	}

	return typeInfos, keyTypes, keyFuncs, imports
}

func parsePythonImportStatement(spec string) []string {
	spec = strings.TrimSpace(spec)
	spec = strings.Trim(spec, "()")
	if spec == "" {
		return nil
	}

	parts := strings.Split(spec, ",")
	imports := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		name = strings.Trim(name, "()")
		if name == "" {
			continue
		}
		imports = append(imports, name)
	}
	return imports
}

func extractPythonFilePurpose(content []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			return extractFirstSentence(strings.TrimSpace(strings.TrimPrefix(trimmed, "#")))
		}

		delimiter := ""
		switch {
		case strings.HasPrefix(trimmed, `"""`):
			delimiter = `"""`
		case strings.HasPrefix(trimmed, `'''`):
			delimiter = `'''`
		}
		if delimiter == "" {
			return ""
		}

		first := strings.TrimSpace(strings.TrimPrefix(trimmed, delimiter))
		if idx := strings.Index(first, delimiter); idx >= 0 {
			first = first[:idx]
			return extractFirstSentence(strings.TrimSpace(first))
		}

		var doc strings.Builder
		if first != "" {
			doc.WriteString(first)
			doc.WriteByte('\n')
		}
		for scanner.Scan() {
			next := scanner.Text()
			if idx := strings.Index(next, delimiter); idx >= 0 {
				doc.WriteString(strings.TrimSpace(next[:idx]))
				break
			}
			doc.WriteString(strings.TrimSpace(next))
			doc.WriteByte('\n')
		}
		return extractFirstSentence(strings.TrimSpace(doc.String()))
	}
	return ""
}

func scorePythonEntryPoint(relPath string, keyTypes, keyFuncs []string) int {
	score := 0
	lower := strings.ToLower(relPath)

	switch {
	case lower == "__main__.py" || strings.HasSuffix(lower, "/__main__.py"):
		score += 140
	case lower == "src/main.py" || lower == "main.py":
		score += 120
	case lower == "src/cli.py" || lower == "cli.py":
		score += 110
	case lower == "__init__.py" || strings.HasSuffix(lower, "/__init__.py"):
		score += 80
	}

	if strings.HasPrefix(lower, "bin/") || strings.Contains(lower, "/bin/") {
		score += 100
	}
	if strings.HasPrefix(lower, "scripts/") || strings.Contains(lower, "/scripts/") {
		score += 90
	}

	for _, fn := range keyFuncs {
		if strings.EqualFold(fn, "main") || strings.EqualFold(fn, "cli") || strings.HasPrefix(strings.ToLower(fn), "run") {
			score += 10
		}
	}
	if len(keyTypes) > 0 {
		score += 5
	}
	return score
}

func pythonImportPrefix(packageName, packageRelPath string) string {
	if candidate := normalizePythonImportPrefix(packageName); candidate != "" {
		return candidate
	}
	if packageRelPath != "" && packageRelPath != "." {
		if candidate := normalizePythonImportPrefix(filepath.Base(packageRelPath)); candidate != "" {
			return candidate
		}
	}
	return ""
}

func normalizePythonImportPrefix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "-", "_")
	if idx := strings.Index(value, "."); idx > 0 {
		value = value[:idx]
	}
	if !pythonIdentifierPattern.MatchString(value) {
		return ""
	}
	return value
}

func isPythonInternalImport(imp, prefix string) bool {
	imp = strings.TrimSpace(imp)
	if imp == "" {
		return false
	}
	if strings.HasPrefix(imp, ".") {
		return true
	}
	if prefix == "" {
		return false
	}
	return imp == prefix || strings.HasPrefix(imp, prefix+".")
}

func isPublicPythonSymbol(name string) bool {
	return name != "" && !strings.HasPrefix(name, "_")
}
