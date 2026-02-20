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
	shellFuncPattern       = regexp.MustCompile(`^(?:function\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:\(\))?\s*\{`)
	shellSourcePattern     = regexp.MustCompile(`^(?:source|\.)\s+([^\s;#]+)`)
	shellIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ShellAnalyzer is the analyzer implementation for shell script projects.
type ShellAnalyzer struct{}

func (ShellAnalyzer) LanguageID() string { return languageShell }

func (ShellAnalyzer) Analyze(ctx context.Context, in AnalysisInput) (*Codemap, error) {
	if in.Index == nil {
		return nil, fmt.Errorf("missing file index")
	}
	return analyzeShellWithIndex(ctx, in.Root, in.Index, in.Options, in.PrevState, in.NextState)
}

func analyzeShellWithIndex(ctx context.Context, root string, idx *FileIndex, opts Options, prevState, nextState *CodemapState) (*Codemap, error) {
	entryByRel := stateEntryByRelPath(nextState)
	plans, err := buildShellPackagePlans(root, idx, opts.IncludeTests, entryByRel)
	if err != nil {
		return nil, err
	}

	const modulePath = languageShell
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
		packageName := shellPackageName(root, plan.RelativePath)
		pkg, err := analyzeShellPackage(root, plan, packageName, opts)
		if err != nil {
			return nil, fmt.Errorf("analyze shell package %s: %w", plan.RelativePath, err)
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

func buildShellPackagePlans(root string, idx *FileIndex, includeTests bool, entriesByRel map[string]StateEntry) ([]packagePlan, error) {
	plansByRel := make(map[string]*packagePlan)
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}

	for _, rec := range idx.Files {
		if rec.Language != languageShell {
			continue
		}
		if !includeTests && isShellTestPath(rec.RelPath, rec.IsTest) {
			continue
		}

		pkgRel := shellPackageRootRel(rec.RelPath)
		pkgAbs := rootAbs
		if pkgRel != "." {
			pkgAbs = filepath.Join(rootAbs, filepath.FromSlash(pkgRel))
		}

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

func analyzeShellPackage(root string, plan packagePlan, packageName string, opts Options) (*Package, error) {
	if len(plan.FileRelPaths) == 0 {
		return nil, nil
	}

	fileRelPaths := append([]string(nil), plan.FileRelPaths...)
	sort.Strings(fileRelPaths)

	files := make([]File, 0, len(fileRelPaths))
	importsSeen := make(map[string]struct{}, len(fileRelPaths))
	totalLines := 0
	purpose := ""
	entryPoint := ""
	entryScore := -1

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

		filePurpose := extractShellFilePurpose(content)
		if purpose == "" && filePurpose != "" {
			purpose = filePurpose
		}

		keyFuncs, imports := parseShellFileSymbols(content)
		for _, imp := range imports {
			importsSeen[imp] = struct{}{}
		}

		files = append(files, File{
			Name:      withinPackage,
			LineCount: lineCount,
			Purpose:   filePurpose,
			KeyFuncs:  keyFuncs,
		})

		score := scoreShellEntryPoint(withinPackage, keyFuncs)
		if score > entryScore || (score == entryScore && (entryPoint == "" || withinPackage < entryPoint)) {
			entryScore = score
			entryPoint = withinPackage
		}
	}

	if entryPoint == "" && len(files) > 0 {
		entryPoint = files[0].Name
	}
	if purpose == "" {
		purpose = "Shell scripts"
		if packageName != "" {
			purpose = "Shell scripts in " + packageName
		}
	}

	internalImports := make([]string, 0, len(importsSeen))
	for imp := range importsSeen {
		internalImports = append(internalImports, imp)
	}
	sort.Strings(internalImports)

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
		ExportedTypes: nil,
		Imports:       internalImports,
		EntryPoint:    entryPoint,
	}, nil
}

func shellPackageRootRel(relPath string) string {
	if guessedRel, guessed := likelyPackageRootRelBySegments(relPath, []string{"scripts", "script", "bin", "hack", "tools", "tests", "test"}); guessed {
		if pathContainsSegment(relPath, "scripts") || pathContainsSegment(relPath, "bin") {
			return guessedRel
		}
	}

	relDir := filepath.ToSlash(filepath.Dir(relPath))
	if relDir == "" {
		return "."
	}
	return relDir
}

func shellPackageName(root, packageRelPath string) string {
	if packageRelPath == "." || packageRelPath == "" {
		return filepath.Base(root)
	}
	return filepath.Base(packageRelPath)
}

func isShellTestPath(relPath string, fileMatchTest bool) bool {
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
	return isShellTestPathLike(base)
}

func parseShellFileSymbols(content []byte) ([]string, []string) {
	keyFuncs := make([]string, 0)
	imports := make([]string, 0)
	funcsSeen := make(map[string]struct{})
	importsSeen := make(map[string]struct{})

	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}

		if match := shellFuncPattern.FindStringSubmatch(trimmed); len(match) == 2 {
			name := match[1]
			if shellIdentifierPattern.MatchString(name) {
				if _, exists := funcsSeen[name]; !exists {
					funcsSeen[name] = struct{}{}
					keyFuncs = append(keyFuncs, name)
				}
			}
			continue
		}

		if match := shellSourcePattern.FindStringSubmatch(trimmed); len(match) == 2 {
			target := strings.TrimSpace(match[1])
			target = strings.Trim(target, `"'`)
			if target == "" {
				continue
			}
			if _, exists := importsSeen[target]; !exists {
				importsSeen[target] = struct{}{}
				imports = append(imports, target)
			}
		}
	}

	return keyFuncs, imports
}

func extractShellFilePurpose(content []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#!") {
			continue
		}
		if strings.HasPrefix(line, "#") {
			return extractFirstSentence(strings.TrimSpace(strings.TrimPrefix(line, "#")))
		}
		return ""
	}
	return ""
}

func scoreShellEntryPoint(relPath string, keyFuncs []string) int {
	score := 0
	lower := strings.ToLower(relPath)

	switch {
	case lower == "scripts/main.sh" || lower == "scripts/main.bash":
		score += 140
	case lower == "main.sh" || lower == "main.bash":
		score += 130
	case strings.HasSuffix(lower, "/main.sh") || strings.HasSuffix(lower, "/main.bash"):
		score += 110
	}

	if strings.HasPrefix(lower, "bin/") || strings.Contains(lower, "/bin/") {
		score += 120
	}
	if strings.HasPrefix(lower, "scripts/") || strings.Contains(lower, "/scripts/") {
		score += 100
	}
	if strings.HasSuffix(lower, ".sh") || strings.HasSuffix(lower, ".bash") || strings.HasSuffix(lower, ".bats") {
		score += 20
	}

	for _, fn := range keyFuncs {
		if strings.EqualFold(fn, "main") {
			score += 10
		}
	}
	if len(keyFuncs) > 0 {
		score += 5
	}

	return score
}
