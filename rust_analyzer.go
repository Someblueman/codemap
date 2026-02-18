package main

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
	rustPubStructRE = regexp.MustCompile(`^\s*pub(?:\([^)]*\))?\s+struct\s+([A-Za-z_][A-Za-z0-9_]*)`)
	rustPubEnumRE   = regexp.MustCompile(`^\s*pub(?:\([^)]*\))?\s+enum\s+([A-Za-z_][A-Za-z0-9_]*)`)
	rustPubTraitRE  = regexp.MustCompile(`^\s*pub(?:\([^)]*\))?\s+trait\s+([A-Za-z_][A-Za-z0-9_]*)`)
	rustPubTypeRE   = regexp.MustCompile(`^\s*pub(?:\([^)]*\))?\s+type\s+([A-Za-z_][A-Za-z0-9_]*)`)
	rustPubFnRE     = regexp.MustCompile(`^\s*pub(?:\([^)]*\))?\s+(?:async\s+)?fn\s+([A-Za-z_][A-Za-z0-9_]*)`)
	rustUseRE       = regexp.MustCompile(`^\s*use\s+(crate|super)::([^;]+);`)
)

// RustAnalyzer is the analyzer implementation for Rust projects.
type RustAnalyzer struct{}

func (RustAnalyzer) LanguageID() string { return languageRust }

func (RustAnalyzer) Analyze(ctx context.Context, in AnalysisInput) (*Codemap, error) {
	if in.Index == nil {
		return nil, fmt.Errorf("missing file index")
	}
	return analyzeRustWithIndex(ctx, in.Root, in.Index, in.Options, in.PrevState, in.NextState)
}

func analyzeRustWithIndex(ctx context.Context, root string, idx *FileIndex, opts Options, prevState, nextState *CodemapState) (*Codemap, error) {
	entryByRel := stateEntryByRelPath(nextState)
	plans, crateNames, err := buildRustPackagePlans(root, idx, opts.IncludeTests, entryByRel)
	if err != nil {
		return nil, err
	}

	const modulePath = "rust"
	cachedByRel := cachedPackagesByPath(prevState, opts, modulePath)

	packageResults := make([]*Package, len(plans))
	for i := range plans {
		plan := plans[i]
		if cached, ok := cachedByRel[plan.RelativePath]; ok && plan.Fingerprint != "" && cached.Fingerprint == plan.Fingerprint {
			pkg := cached.Package
			packageResults[i] = &pkg
			continue
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		pkg, err := analyzeRustPackage(root, plan, crateNames[plan.RelativePath], opts)
		if err != nil {
			return nil, fmt.Errorf("analyze rust package %s: %w", plan.RelativePath, err)
		}
		packageResults[i] = pkg
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

func buildRustPackagePlans(root string, idx *FileIndex, includeTests bool, entriesByRel map[string]StateEntry) ([]packagePlan, map[string]string, error) {
	plansByRel := make(map[string]*packagePlan)
	crateNames := make(map[string]string)

	for _, rec := range idx.Files {
		if rec.Language != languageRust {
			continue
		}
		if !includeTests && isRustTestPath(rec.RelPath) {
			continue
		}

		crateRel, crateAbs, err := findRustCrateRoot(root, rec.AbsPath)
		if err != nil {
			return nil, nil, err
		}

		plan, ok := plansByRel[crateRel]
		if !ok {
			plan = &packagePlan{
				RelativePath: crateRel,
				DirAbsPath:   crateAbs,
				FileRelPaths: make([]string, 0, 4),
			}
			plansByRel[crateRel] = plan
			crateNames[crateRel] = readRustCrateName(crateAbs, crateRel)
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

	return plans, crateNames, nil
}

func analyzeRustPackage(root string, plan packagePlan, crateName string, opts Options) (*Package, error) {
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

		filePurpose := extractRustFilePurpose(content)
		if purpose == "" && filePurpose != "" {
			purpose = filePurpose
		}

		typeInfos, keyTypes, keyFuncs, imports := parseRustFileSymbols(content)
		allTypes = append(allTypes, typeInfos...)
		for _, imp := range imports {
			importsSeen[imp] = struct{}{}
		}

		files = append(files, File{
			Name:      withinPackage,
			LineCount: lineCount,
			Purpose:   filePurpose,
			KeyTypes:  keyTypes,
			KeyFuncs:  keyFuncs,
		})

		score := scoreRustEntryPoint(withinPackage, keyTypes, keyFuncs)
		if score > entryScore || (score == entryScore && (entryPoint == "" || withinPackage < entryPoint)) {
			entryScore = score
			entryPoint = withinPackage
		}
	}

	if entryPoint == "" && len(files) > 0 {
		entryPoint = files[0].Name
	}
	if purpose == "" && crateName != "" {
		purpose = "Rust crate " + crateName
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
		ImportPath:    crateName,
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

func findRustCrateRoot(root, fileAbsPath string) (string, string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve root: %w", err)
	}

	dir := filepath.Dir(fileAbsPath)
	for {
		cargoPath := filepath.Join(dir, "Cargo.toml")
		if info, err := os.Stat(cargoPath); err == nil && !info.IsDir() {
			rel, err := filepath.Rel(rootAbs, dir)
			if err != nil || rel == "." {
				return ".", dir, nil
			}
			return filepath.ToSlash(rel), dir, nil
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

	return ".", rootAbs, nil
}

func readRustCrateName(crateAbsPath, crateRelPath string) string {
	cargoPath := filepath.Join(crateAbsPath, "Cargo.toml")
	content, err := os.ReadFile(cargoPath)
	if err != nil {
		return fallbackRustCrateName(crateAbsPath, crateRelPath)
	}

	inPackage := false
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
			inPackage = strings.EqualFold(line, "[package]")
			continue
		}
		if !inPackage {
			continue
		}

		if strings.HasPrefix(line, "name") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			name := strings.TrimSpace(parts[1])
			name = strings.Trim(name, `"`)
			if name != "" {
				return name
			}
		}
	}

	return fallbackRustCrateName(crateAbsPath, crateRelPath)
}

func fallbackRustCrateName(crateAbsPath, crateRelPath string) string {
	if crateRelPath == "." {
		return filepath.Base(crateAbsPath)
	}
	return filepath.Base(crateRelPath)
}

func isRustTestPath(relPath string) bool {
	lower := strings.ToLower(relPath)
	if strings.HasPrefix(lower, "tests/") || strings.Contains(lower, "/tests/") {
		return true
	}
	return strings.HasSuffix(lower, "_test.rs") ||
		strings.HasSuffix(lower, ".test.rs") ||
		strings.HasSuffix(lower, ".spec.rs")
}

func parseRustFileSymbols(content []byte) ([]TypeInfo, []string, []string, []string) {
	typeInfos := make([]TypeInfo, 0)
	keyTypes := make([]string, 0)
	keyFuncs := make([]string, 0)
	imports := make([]string, 0)

	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}

		if matches := rustPubStructRE.FindStringSubmatch(line); len(matches) == 2 {
			name := matches[1]
			typeInfos = append(typeInfos, TypeInfo{Name: name, Kind: "struct"})
			keyTypes = append(keyTypes, name)
			continue
		}
		if matches := rustPubEnumRE.FindStringSubmatch(line); len(matches) == 2 {
			name := matches[1]
			typeInfos = append(typeInfos, TypeInfo{Name: name, Kind: "enum"})
			keyTypes = append(keyTypes, name)
			continue
		}
		if matches := rustPubTraitRE.FindStringSubmatch(line); len(matches) == 2 {
			name := matches[1]
			typeInfos = append(typeInfos, TypeInfo{Name: name, Kind: "trait"})
			keyTypes = append(keyTypes, name)
			continue
		}
		if matches := rustPubTypeRE.FindStringSubmatch(line); len(matches) == 2 {
			name := matches[1]
			typeInfos = append(typeInfos, TypeInfo{Name: name, Kind: "type"})
			keyTypes = append(keyTypes, name)
			continue
		}
		if matches := rustPubFnRE.FindStringSubmatch(line); len(matches) == 2 {
			keyFuncs = append(keyFuncs, matches[1])
			continue
		}
		if matches := rustUseRE.FindStringSubmatch(line); len(matches) == 3 {
			path := strings.TrimSpace(matches[1] + "::" + matches[2])
			path = strings.ReplaceAll(path, " ", "")
			imports = append(imports, path)
			continue
		}
	}

	return typeInfos, keyTypes, keyFuncs, imports
}

func extractRustFilePurpose(content []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		switch {
		case strings.HasPrefix(line, "//!"):
			return extractFirstSentence(strings.TrimSpace(strings.TrimPrefix(line, "//!")))
		case strings.HasPrefix(line, "///"):
			return extractFirstSentence(strings.TrimSpace(strings.TrimPrefix(line, "///")))
		case strings.HasPrefix(line, "//"):
			return extractFirstSentence(strings.TrimSpace(strings.TrimPrefix(line, "//")))
		default:
			return ""
		}
	}
	return ""
}

func scoreRustEntryPoint(relPath string, keyTypes, keyFuncs []string) int {
	score := 0
	lower := strings.ToLower(relPath)

	switch {
	case lower == "src/main.rs":
		score += 120
	case lower == "src/lib.rs":
		score += 110
	case strings.HasPrefix(lower, "src/bin/") && strings.HasSuffix(lower, ".rs"):
		score += 100
	case lower == "main.rs":
		score += 80
	case lower == "lib.rs":
		score += 70
	case strings.HasSuffix(lower, "/mod.rs") || lower == "mod.rs":
		score += 40
	}

	for _, fn := range keyFuncs {
		if strings.EqualFold(fn, "main") || strings.HasPrefix(fn, "new") {
			score += 10
		}
	}
	if len(keyTypes) > 0 {
		score += 5
	}
	return score
}

func lineCountBytes(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	return bytes.Count(content, []byte{'\n'}) + 1
}
