package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

const (
	codemapStateVersion    = 3
	analysisCacheVersionV2 = 2
)

// StateEntry stores per-file metadata for incremental hashing.
type StateEntry struct {
	RelPath         string `json:"relPath"`
	Size            int64  `json:"size"`
	ModTimeUnixNano int64  `json:"modTimeUnixNano"`
	ContentHash     string `json:"contentHash"`
}

// DirStateEntry stores per-directory metadata for fast stale checks.
type DirStateEntry struct {
	RelPath         string `json:"relPath"`
	ModTimeUnixNano int64  `json:"modTimeUnixNano"`
}

// CachedPackage stores package-level analysis output for incremental rebuilds.
type CachedPackage struct {
	RelativePath string   `json:"relativePath"`
	Fingerprint  string   `json:"fingerprint"`
	FileRelPaths []string `json:"fileRelPaths,omitempty"`
	Package      Package  `json:"package"`
}

// AnalysisCache stores cached package analysis metadata.
type AnalysisCache struct {
	Version           int             `json:"version"`
	IncludeTests      bool            `json:"includeTests"`
	LargePackageFiles int             `json:"largePackageFiles"`
	ModulePath        string          `json:"modulePath"`
	Packages          []CachedPackage `json:"packages,omitempty"`
}

// CodemapState stores local cache metadata for staleness checks.
type CodemapState struct {
	Version       int             `json:"version"`
	AggregateHash string          `json:"aggregateHash"`
	RootEntries   []string        `json:"rootEntries,omitempty"`
	Dirs          []DirStateEntry `json:"dirs,omitempty"`
	Entries       []StateEntry    `json:"entries"`
	Analysis      *AnalysisCache  `json:"analysis,omitempty"`
}

// ComputeHash computes a deterministic hash of all Go files in the project.
func ComputeHash(ctx context.Context, root string) (string, error) {
	idx, err := BuildFileIndex(ctx, root)
	if err != nil {
		return "", fmt.Errorf("build file index: %w", err)
	}

	hash, err := computeAggregateHashOnly(ctx, idx, nil)
	if err != nil {
		return "", err
	}
	return hash, nil
}

func computeAggregateHash(ctx context.Context, idx *FileIndex, prev *CodemapState) (string, *CodemapState, error) {
	if aggregate, ok := aggregateHashFromState(idx, prev); ok {
		rootEntries := make([]string, len(prev.RootEntries))
		copy(rootEntries, prev.RootEntries)
		dirs := make([]DirStateEntry, len(prev.Dirs))
		copy(dirs, prev.Dirs)
		entries := make([]StateEntry, len(prev.Entries))
		copy(entries, prev.Entries)
		return aggregate, &CodemapState{
			Version:       codemapStateVersion,
			AggregateHash: aggregate,
			RootEntries:   rootEntries,
			Dirs:          dirs,
			Entries:       entries,
		}, nil
	}

	prevEntries := sortedStateEntries(prev)
	prevPos := 0
	entries := make([]StateEntry, 0, len(idx.Files))
	jobs := make([]hashJob, 0, len(idx.Files))
	for _, rec := range idx.Files {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		default:
		}

		entry := StateEntry{
			RelPath:         rec.RelPath,
			Size:            rec.Size,
			ModTimeUnixNano: rec.ModTimeUnixNano,
		}

		cached, ok := findCachedEntry(prevEntries, rec.RelPath, &prevPos)
		if ok && cached.Size == rec.Size && cached.ModTimeUnixNano == rec.ModTimeUnixNano && cached.ContentHash != "" {
			entry.ContentHash = cached.ContentHash
		} else {
			jobs = append(jobs, hashJob{
				entryIdx: len(entries),
				absPath:  rec.AbsPath,
				relPath:  rec.RelPath,
			})
		}

		entries = append(entries, entry)
	}

	if err := hashMissingEntries(ctx, entries, jobs); err != nil {
		return "", nil, err
	}

	h := sha256.New()
	sep := []byte{0}
	for i := range entries {
		_, _ = io.WriteString(h, entries[i].RelPath)
		_, _ = h.Write(sep)
		_, _ = io.WriteString(h, entries[i].ContentHash)
		_, _ = h.Write(sep)
	}

	aggregate := hex.EncodeToString(h.Sum(nil))
	next := &CodemapState{
		Version:       codemapStateVersion,
		AggregateHash: aggregate,
		RootEntries:   rootEntriesFromIndex(idx),
		Dirs:          dirStateFromIndex(idx),
		Entries:       entries,
	}
	return aggregate, next, nil
}

func computeAggregateHashOnly(ctx context.Context, idx *FileIndex, prev *CodemapState) (string, error) {
	if aggregate, ok := aggregateHashFromState(idx, prev); ok {
		return aggregate, nil
	}

	prevEntries := sortedStateEntries(prev)
	prevPos := 0
	h := sha256.New()
	sep := []byte{0}

	for _, rec := range idx.Files {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		contentHash := ""
		cached, ok := findCachedEntry(prevEntries, rec.RelPath, &prevPos)
		if ok &&
			cached.Size == rec.Size &&
			cached.ModTimeUnixNano == rec.ModTimeUnixNano &&
			cached.ContentHash != "" {
			contentHash = cached.ContentHash
		} else {
			var err error
			contentHash, err = hashFileContents(rec.AbsPath)
			if err != nil {
				return "", fmt.Errorf("hash %s: %w", rec.RelPath, err)
			}
		}

		_, _ = io.WriteString(h, rec.RelPath)
		_, _ = h.Write(sep)
		_, _ = io.WriteString(h, contentHash)
		_, _ = h.Write(sep)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func aggregateHashFromState(idx *FileIndex, prev *CodemapState) (string, bool) {
	if prev == nil || prev.Version != codemapStateVersion || prev.AggregateHash == "" {
		return "", false
	}
	if len(prev.Dirs) == 0 {
		return "", false
	}
	if len(idx.Files) != len(prev.Entries) {
		return "", false
	}

	for i := range idx.Files {
		rec := idx.Files[i]
		entry := prev.Entries[i]
		if rec.RelPath != entry.RelPath ||
			rec.Size != entry.Size ||
			rec.ModTimeUnixNano != entry.ModTimeUnixNano ||
			entry.ContentHash == "" {
			return "", false
		}
	}

	return prev.AggregateHash, true
}

func dirStateFromIndex(idx *FileIndex) []DirStateEntry {
	if idx == nil || len(idx.Dirs) == 0 {
		return nil
	}
	dirs := make([]DirStateEntry, len(idx.Dirs))
	n := 0
	for _, rec := range idx.Dirs {
		if rec.RelPath == "." {
			continue
		}
		dirs[n] = DirStateEntry{
			RelPath:         rec.RelPath,
			ModTimeUnixNano: rec.ModTimeUnixNano,
		}
		n++
	}
	dirs = dirs[:n]
	return dirs
}

func rootEntriesFromIndex(idx *FileIndex) []string {
	if idx == nil || len(idx.RootEntries) == 0 {
		return nil
	}
	entries := make([]string, len(idx.RootEntries))
	copy(entries, idx.RootEntries)
	return entries
}

func aggregateHashFromFilesystemState(ctx context.Context, absRoot string, prev *CodemapState, ignoredRootEntries map[string]struct{}) (string, bool, error) {
	if absRoot == "" {
		return "", false, errors.New("missing root")
	}
	if prev == nil || prev.Version != codemapStateVersion || prev.AggregateHash == "" {
		return "", false, nil
	}
	if len(prev.RootEntries) == 0 {
		return "", false, nil
	}

	currentRootEntries, err := os.ReadDir(absRoot)
	if err != nil {
		return "", false, err
	}
	expectedRootEntries := filterRootEntries(prev.RootEntries, ignoredRootEntries)
	actualRootEntries := make([]string, 0, len(currentRootEntries))
	for _, entry := range currentRootEntries {
		name := entry.Name()
		if _, ignored := ignoredRootEntries[name]; ignored {
			continue
		}
		actualRootEntries = append(actualRootEntries, name)
	}
	if len(actualRootEntries) != len(expectedRootEntries) {
		return "", false, nil
	}
	for i := range actualRootEntries {
		if actualRootEntries[i] != expectedRootEntries[i] {
			return "", false, nil
		}
	}

	for _, dir := range prev.Dirs {
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		default:
		}

		absDir := absRoot
		if dir.RelPath != "." {
			absDir = filepath.Join(absRoot, filepath.FromSlash(dir.RelPath))
		}
		info, err := os.Stat(absDir)
		if err != nil {
			if os.IsNotExist(err) {
				return "", false, nil
			}
			return "", false, err
		}
		if !info.IsDir() || info.ModTime().UnixNano() != dir.ModTimeUnixNano {
			return "", false, nil
		}
	}

	for _, entry := range prev.Entries {
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		default:
		}

		absPath := filepath.Join(absRoot, filepath.FromSlash(entry.RelPath))
		info, err := os.Stat(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				return "", false, nil
			}
			return "", false, err
		}
		if info.IsDir() {
			return "", false, nil
		}
		if info.Size() != entry.Size || info.ModTime().UnixNano() != entry.ModTimeUnixNano {
			return "", false, nil
		}
		if !strings.HasSuffix(entry.RelPath, ".go") || entry.ContentHash == "" {
			return "", false, nil
		}
	}

	return prev.AggregateHash, true, nil
}

func filterRootEntries(entries []string, ignored map[string]struct{}) []string {
	if len(entries) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(entries))
	for _, name := range entries {
		if _, isIgnored := ignored[name]; isIgnored {
			continue
		}
		filtered = append(filtered, name)
	}
	return filtered
}

func ignoredRootEntryNames(root string, opts Options) map[string]struct{} {
	ignored := make(map[string]struct{}, 4)
	maybeAdd := func(path string) {
		if path == "" {
			return
		}
		abs := path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, abs)
		}
		if filepath.Dir(abs) != root {
			return
		}
		ignored[filepath.Base(abs)] = struct{}{}
	}

	maybeAdd(opts.OutputPath)
	if !opts.DisablePaths {
		maybeAdd(opts.PathsOutputPath)
	}
	maybeAdd(resolveStatePath(root, opts))
	maybeAdd(resolveAnalysisStatePath(root, opts))
	return ignored
}

func sortedStateEntries(prev *CodemapState) []StateEntry {
	if prev == nil || prev.Version != codemapStateVersion || len(prev.Entries) == 0 {
		return nil
	}
	return prev.Entries
}

func findCachedEntry(prevEntries []StateEntry, relPath string, pos *int) (StateEntry, bool) {
	for *pos < len(prevEntries) && prevEntries[*pos].RelPath < relPath {
		*pos = *pos + 1
	}
	if *pos < len(prevEntries) && prevEntries[*pos].RelPath == relPath {
		return prevEntries[*pos], true
	}
	return StateEntry{}, false
}

type hashJob struct {
	entryIdx int
	absPath  string
	relPath  string
}

type hashResult struct {
	entryIdx    int
	contentHash string
}

func hashMissingEntries(ctx context.Context, entries []StateEntry, jobs []hashJob) error {
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
			contentHash, err := hashFileContents(job.absPath)
			if err != nil {
				return fmt.Errorf("hash %s: %w", job.relPath, err)
			}
			entries[job.entryIdx].ContentHash = contentHash
		}
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobsCh := make(chan hashJob)
	resultsCh := make(chan hashResult, len(jobs))
	errCh := make(chan error, 1)

	var wg sync.WaitGroup
	worker := func() {
		defer wg.Done()
		for job := range jobsCh {
			select {
			case <-ctx.Done():
				return
			default:
			}

			contentHash, err := hashFileContents(job.absPath)
			if err != nil {
				select {
				case errCh <- fmt.Errorf("hash %s: %w", job.relPath, err):
				default:
				}
				cancel()
				return
			}

			select {
			case resultsCh <- hashResult{entryIdx: job.entryIdx, contentHash: contentHash}:
			case <-ctx.Done():
				return
			}
		}
	}

	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go worker()
	}

dispatchLoop:
	for _, job := range jobs {
		select {
		case jobsCh <- job:
		case <-ctx.Done():
			break dispatchLoop
		}
	}
	close(jobsCh)
	wg.Wait()
	close(resultsCh)

	select {
	case err := <-errCh:
		return err
	default:
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	resultCount := 0
	for result := range resultsCh {
		entries[result.entryIdx].ContentHash = result.contentHash
		resultCount++
	}
	if resultCount != len(jobs) {
		return errors.New("incomplete hash results")
	}

	return nil
}

func hashFileContents(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readState(path string) (*CodemapState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var state CodemapState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, nil
	}
	if state.Version != codemapStateVersion {
		return nil, nil
	}

	sort.Strings(state.RootEntries)
	sort.Slice(state.Dirs, func(i, j int) bool {
		return state.Dirs[i].RelPath < state.Dirs[j].RelPath
	})
	sort.Slice(state.Entries, func(i, j int) bool {
		return state.Entries[i].RelPath < state.Entries[j].RelPath
	})
	if state.Analysis != nil {
		sort.Slice(state.Analysis.Packages, func(i, j int) bool {
			return state.Analysis.Packages[i].RelativePath < state.Analysis.Packages[j].RelativePath
		})
	}
	return &state, nil
}

func writeState(path string, state *CodemapState) error {
	if state == nil {
		return nil
	}

	// Keep analysis cache out of the hot-path state file.
	stateForDisk := *state
	stateForDisk.Analysis = nil

	data, err := json.MarshalIndent(stateForDisk, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func resolveStatePath(root string, opts Options) string {
	statePath := opts.StatePath
	if statePath == "" {
		statePath = ".codemap.state.json"
	}
	if filepath.IsAbs(statePath) {
		return statePath
	}
	return filepath.Join(root, statePath)
}

func resolveAnalysisStatePath(root string, opts Options) string {
	statePath := resolveStatePath(root, opts)
	ext := filepath.Ext(statePath)
	if ext == "" {
		return statePath + ".analysis"
	}
	base := strings.TrimSuffix(statePath, ext)
	return base + ".analysis" + ext
}

func readAnalysisCache(path string) (*AnalysisCache, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var cache AnalysisCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, nil
	}
	if cache.Version != analysisCacheVersionV2 {
		return nil, nil
	}
	sort.Slice(cache.Packages, func(i, j int) bool {
		return cache.Packages[i].RelativePath < cache.Packages[j].RelativePath
	})
	return &cache, nil
}

func writeAnalysisCache(path string, cache *AnalysisCache) error {
	if cache == nil || len(cache.Packages) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// ReadExistingHash reads the hash from an existing codemap output file.
func ReadExistingHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	linesChecked := 0
	for scanner.Scan() {
		linesChecked++
		line := scanner.Text()
		if hash := parseHashLine(line); hash != "" {
			return hash, nil
		}
		if linesChecked >= 20 {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	return "", nil
}

func parseHashLine(line string) string {
	s := strings.TrimSpace(line)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "<!--") {
		s = strings.TrimSpace(strings.TrimPrefix(s, "<!--"))
		s = strings.TrimSpace(strings.TrimSuffix(s, "-->"))
	}
	if strings.HasPrefix(s, "#") {
		s = strings.TrimSpace(strings.TrimPrefix(s, "#"))
	}

	const prefix = "codemap-hash:"
	if !strings.HasPrefix(s, prefix) {
		return ""
	}
	value := strings.TrimSpace(s[len(prefix):])
	if value == "" {
		return ""
	}

	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	hash := fields[0]
	for _, r := range hash {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return hash
}

// IsStale checks if codemap outputs are stale.
func IsStale(ctx context.Context, opts Options) (bool, error) {
	root, err := filepath.Abs(opts.ProjectRoot)
	if err != nil {
		return false, fmt.Errorf("resolve root: %w", err)
	}

	if opts.OutputPath == "" {
		opts.OutputPath = "CODEMAP.md"
	}
	if opts.PathsOutputPath == "" {
		opts.PathsOutputPath = "CODEMAP.paths"
	}

	outputPath := filepath.Join(root, opts.OutputPath)
	existingHash, err := ReadExistingHash(outputPath)
	if err != nil {
		return false, fmt.Errorf("read existing hash: %w", err)
	}
	if existingHash == "" {
		return true, nil
	}

	var existingPathsHash string
	if !opts.DisablePaths {
		pathsPath := filepath.Join(root, opts.PathsOutputPath)
		existingPathsHash, err = ReadExistingHash(pathsPath)
		if err != nil {
			return false, fmt.Errorf("read existing paths hash: %w", err)
		}
		if existingPathsHash == "" {
			return true, nil
		}
	}

	state, err := readState(resolveStatePath(root, opts))
	if err != nil {
		return false, fmt.Errorf("read state: %w", err)
	}
	currentHash, matchedFromState, err := aggregateHashFromFilesystemState(ctx, root, state, ignoredRootEntryNames(root, opts))
	if err != nil {
		return false, fmt.Errorf("verify state: %w", err)
	}
	if !matchedFromState {
		idx, err := BuildFileIndex(ctx, root)
		if err != nil {
			return false, fmt.Errorf("build file index: %w", err)
		}
		currentHash, err = computeAggregateHashOnly(ctx, idx, state)
		if err != nil {
			return false, fmt.Errorf("compute hash: %w", err)
		}
	}

	if existingHash != currentHash {
		return true, nil
	}
	if !opts.DisablePaths && existingPathsHash != currentHash {
		return true, nil
	}

	return false, nil
}
