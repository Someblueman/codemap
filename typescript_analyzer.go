package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// TypeScriptAnalyzer is the analyzer implementation for TypeScript projects.
type TypeScriptAnalyzer struct{}

func (TypeScriptAnalyzer) LanguageID() string { return languageTypeScript }

func (TypeScriptAnalyzer) Analyze(ctx context.Context, in AnalysisInput) (*Codemap, error) {
	if in.Index == nil {
		return nil, fmt.Errorf("missing file index")
	}
	return analyzeTypeScriptWithIndex(ctx, in.Root, in.Index, in.Options, in.PrevState, in.NextState)
}

func analyzeTypeScriptWithIndex(ctx context.Context, root string, idx *FileIndex, opts Options, prevState, nextState *CodemapState) (*Codemap, error) {
	entryByRel := stateEntryByRelPath(nextState)
	plans, packageNames, err := buildTypeScriptPackagePlans(root, idx, opts.IncludeTests, entryByRel)
	if err != nil {
		return nil, err
	}

	const modulePath = "typescript"
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
		pkg, err := analyzeTypeScriptPackage(root, plan, packageNames[plan.RelativePath], opts)
		if err != nil {
			return nil, fmt.Errorf("analyze typescript package %s: %w", plan.RelativePath, err)
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

func buildTypeScriptPackagePlans(root string, idx *FileIndex, includeTests bool, entriesByRel map[string]StateEntry) ([]packagePlan, map[string]string, error) {
	plansByRel := make(map[string]*packagePlan)
	packageNames := make(map[string]string)

	for _, rec := range idx.Files {
		if rec.Language != languageTypeScript {
			continue
		}
		if !includeTests && isTypeScriptTestPath(rec.RelPath, rec.IsTest) {
			continue
		}

		pkgRel, pkgAbs, err := findTypeScriptPackageRoot(root, rec.AbsPath)
		if err != nil {
			return nil, nil, err
		}

		plan, ok := plansByRel[pkgRel]
		if !ok {
			plan = &packagePlan{
				RelativePath: pkgRel,
				DirAbsPath:   pkgAbs,
				FileRelPaths: make([]string, 0, 4),
			}
			plansByRel[pkgRel] = plan
			packageNames[pkgRel] = readTypeScriptPackageName(pkgAbs, pkgRel)
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

	return plans, packageNames, nil
}

func analyzeTypeScriptPackage(root string, plan packagePlan, packageName string, opts Options) (*Package, error) {
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

		lineCount := lineCountBytesTS(content)
		totalLines += lineCount

		withinPackage := relPath
		if plan.RelativePath != "." {
			prefix := plan.RelativePath + "/"
			if strings.HasPrefix(relPath, prefix) {
				withinPackage = strings.TrimPrefix(relPath, prefix)
			}
		}

		filePurpose := extractTypeScriptFilePurpose(content)
		if purpose == "" && filePurpose != "" {
			purpose = filePurpose
		}

		typeInfos, keyTypes, keyFuncs, imports := parseTypeScriptFileSymbols(content, withinPackage)
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

		score := scoreTypeScriptEntryPoint(withinPackage, keyTypes, keyFuncs)
		if score > entryScore || (score == entryScore && (entryPoint == "" || withinPackage < entryPoint)) {
			entryScore = score
			entryPoint = withinPackage
		}
	}

	if entryPoint == "" && len(files) > 0 {
		entryPoint = files[0].Name
	}
	if purpose == "" && packageName != "" {
		purpose = "TypeScript package " + packageName
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

func findTypeScriptPackageRoot(root, fileAbsPath string) (string, string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve root: %w", err)
	}

	dir := filepath.Dir(fileAbsPath)
	for {
		manifestPath := filepath.Join(dir, "package.json")
		if info, err := os.Stat(manifestPath); err == nil && !info.IsDir() {
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

func readTypeScriptPackageName(packageAbsPath, packageRelPath string) string {
	manifestPath := filepath.Join(packageAbsPath, "package.json")
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		return fallbackTypeScriptPackageName(packageAbsPath, packageRelPath)
	}

	var manifest struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		return fallbackTypeScriptPackageName(packageAbsPath, packageRelPath)
	}
	if strings.TrimSpace(manifest.Name) == "" {
		return fallbackTypeScriptPackageName(packageAbsPath, packageRelPath)
	}
	return strings.TrimSpace(manifest.Name)
}

func fallbackTypeScriptPackageName(packageAbsPath, packageRelPath string) string {
	if packageRelPath == "." {
		return filepath.Base(packageAbsPath)
	}
	return filepath.Base(packageRelPath)
}

func isTypeScriptTestPath(relPath string, fileMatchTest bool) bool {
	if fileMatchTest {
		return true
	}
	lower := strings.ToLower(relPath)
	return strings.HasPrefix(lower, "__tests__/") || strings.Contains(lower, "/__tests__/")
}

func parseTypeScriptFileSymbols(content []byte, filePath string) ([]TypeInfo, []string, []string, []string) {
	typeInfos := make([]TypeInfo, 0)
	keyTypes := make([]string, 0)
	keyFuncs := make([]string, 0)
	imports := make([]string, 0)

	parser, err := newTypeScriptParser(isTypeScriptTSXPath(filePath))
	if err != nil {
		return typeInfos, keyTypes, keyFuncs, imports
	}
	defer parser.Close()

	tree := parser.Parse(content, nil)
	if tree == nil {
		return typeInfos, keyTypes, keyFuncs, imports
	}
	defer tree.Close()

	root := tree.RootNode()
	if root == nil {
		return typeInfos, keyTypes, keyFuncs, imports
	}

	for i := uint(0); i < root.NamedChildCount(); i++ {
		stmt := root.NamedChild(i)
		if stmt == nil {
			continue
		}

		switch stmt.Kind() {
		case "import_statement":
			if target := typeScriptRelativeSource(stmt, content); target != "" {
				imports = append(imports, target)
			}
		case "export_statement":
			exportTypes, exportKeyTypes, exportKeyFuncs := parseTypeScriptExportStatement(stmt, content)
			typeInfos = append(typeInfos, exportTypes...)
			keyTypes = append(keyTypes, exportKeyTypes...)
			keyFuncs = append(keyFuncs, exportKeyFuncs...)
			if target := typeScriptRelativeSource(stmt, content); target != "" {
				imports = append(imports, target)
			}
		}
	}

	return typeInfos, keyTypes, keyFuncs, imports
}

func parseTypeScriptExportStatement(stmt *sitter.Node, content []byte) ([]TypeInfo, []string, []string) {
	typeInfos := make([]TypeInfo, 0)
	keyTypes := make([]string, 0)
	keyFuncs := make([]string, 0)

	declaration := stmt.ChildByFieldName("declaration")
	if declaration != nil {
		switch declaration.Kind() {
		case "class_declaration":
			typeScriptAppendTypeInfo(declaration, content, "class", &typeInfos, &keyTypes)
		case "interface_declaration":
			typeScriptAppendTypeInfo(declaration, content, "interface", &typeInfos, &keyTypes)
		case "type_alias_declaration":
			typeScriptAppendTypeInfo(declaration, content, "type", &typeInfos, &keyTypes)
		case "enum_declaration":
			typeScriptAppendTypeInfo(declaration, content, "enum", &typeInfos, &keyTypes)
		case "function_declaration":
			name := typeScriptDeclarationName(declaration, content)
			if name != "" {
				keyFuncs = append(keyFuncs, name)
			}
		case "lexical_declaration", "variable_declaration":
			keyFuncs = append(keyFuncs, typeScriptVariableDeclaratorNames(declaration, content)...)
		}
	}

	value := stmt.ChildByFieldName("value")
	if value != nil {
		switch value.Kind() {
		case "function_expression", "arrow_function":
			keyFuncs = append(keyFuncs, "default")
		case "class":
			typeInfos = append(typeInfos, TypeInfo{Name: "default", Kind: "class"})
			keyTypes = append(keyTypes, "default")
		}
	}

	for i := uint(0); i < stmt.NamedChildCount(); i++ {
		child := stmt.NamedChild(i)
		if child == nil {
			continue
		}
		switch child.Kind() {
		case "export_clause":
			keyFuncs = append(keyFuncs, typeScriptExportClauseNames(child, content)...)
		case "namespace_export":
			name := typeScriptNamespaceExportName(child, content)
			if name != "" {
				keyFuncs = append(keyFuncs, name)
			}
		}
	}

	return typeInfos, keyTypes, keyFuncs
}

func typeScriptAppendTypeInfo(node *sitter.Node, content []byte, kind string, typeInfos *[]TypeInfo, keyTypes *[]string) {
	name := typeScriptDeclarationName(node, content)
	if name == "" {
		return
	}
	*typeInfos = append(*typeInfos, TypeInfo{Name: name, Kind: kind})
	*keyTypes = append(*keyTypes, name)
}

func typeScriptDeclarationName(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return ""
	}
	return strings.TrimSpace(nodeText(nameNode, content))
}

func typeScriptVariableDeclaratorNames(declaration *sitter.Node, content []byte) []string {
	if declaration == nil {
		return nil
	}
	names := make([]string, 0)
	for i := uint(0); i < declaration.NamedChildCount(); i++ {
		child := declaration.NamedChild(i)
		if child == nil || child.Kind() != "variable_declarator" {
			continue
		}
		nameNode := child.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		names = append(names, typeScriptBindingIdentifiers(nameNode, content)...)
	}
	return names
}

func typeScriptBindingIdentifiers(node *sitter.Node, content []byte) []string {
	if node == nil {
		return nil
	}
	switch node.Kind() {
	case "identifier", "shorthand_property_identifier_pattern":
		name := strings.TrimSpace(nodeText(node, content))
		if name == "" {
			return nil
		}
		return []string{name}
	default:
		out := make([]string, 0, node.NamedChildCount())
		for i := uint(0); i < node.NamedChildCount(); i++ {
			out = append(out, typeScriptBindingIdentifiers(node.NamedChild(i), content)...)
		}
		return out
	}
}

func typeScriptExportClauseNames(clause *sitter.Node, content []byte) []string {
	names := make([]string, 0)
	if clause == nil {
		return names
	}
	for i := uint(0); i < clause.NamedChildCount(); i++ {
		spec := clause.NamedChild(i)
		if spec == nil || spec.Kind() != "export_specifier" {
			continue
		}
		nameNode := spec.ChildByFieldName("alias")
		if nameNode == nil {
			nameNode = spec.ChildByFieldName("name")
		}
		if nameNode == nil {
			continue
		}
		name := strings.TrimSpace(nodeText(nameNode, content))
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func typeScriptNamespaceExportName(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	if node.NamedChildCount() == 0 {
		return ""
	}
	return strings.TrimSpace(nodeText(node.NamedChild(0), content))
}

func typeScriptRelativeSource(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	source := node.ChildByFieldName("source")
	if source == nil {
		return ""
	}
	target := unquoteStringLiteral(nodeText(source, content))
	if strings.HasPrefix(target, ".") {
		return target
	}
	return ""
}

func extractTypeScriptFilePurpose(content []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	inBlockComment := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		switch {
		case strings.HasPrefix(line, "/**"), strings.HasPrefix(line, "/*"):
			inBlockComment = true
			line = strings.TrimSpace(strings.TrimPrefix(line, "/**"))
			line = strings.TrimSpace(strings.TrimPrefix(line, "/*"))
			line = strings.TrimSpace(strings.TrimPrefix(line, "*"))
			line = strings.TrimSuffix(line, "*/")
			line = strings.TrimSpace(line)
			if line != "" {
				return extractFirstSentence(line)
			}
		case inBlockComment:
			if strings.Contains(line, "*/") {
				inBlockComment = false
			}
			line = strings.TrimSpace(strings.TrimPrefix(line, "*"))
			line = strings.TrimSuffix(line, "*/")
			line = strings.TrimSpace(line)
			if line != "" {
				return extractFirstSentence(line)
			}
		case strings.HasPrefix(line, "//"):
			return extractFirstSentence(strings.TrimSpace(strings.TrimPrefix(line, "//")))
		default:
			return ""
		}
	}
	return ""
}

func scoreTypeScriptEntryPoint(relPath string, keyTypes, keyFuncs []string) int {
	score := 0
	lower := strings.ToLower(relPath)

	switch lower {
	case "src/index.ts", "src/index.tsx", "src/index.mts", "src/index.cts":
		score += 120
	case "index.ts", "index.tsx", "index.mts", "index.cts":
		score += 110
	case "src/main.ts", "src/main.tsx":
		score += 100
	}

	if strings.HasPrefix(lower, "src/bin/") {
		score += 80
	}

	if len(keyTypes) > 0 {
		score += 5
	}
	if len(keyFuncs) > 0 {
		score += 5
	}
	return score
}

func lineCountBytesTS(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	return bytes.Count(content, []byte{'\n'}) + 1
}
