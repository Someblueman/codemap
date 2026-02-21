package codemap

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
	"sync/atomic"
	"time"
)

const (
	codemapStateVersion    = 4
	analysisCacheVersionV2 = 2
)

type cachedStateFile struct {
	state *CodemapState
}

type cachedAnalysisFile struct {
	cache *AnalysisCache
}

type cachedHashFile struct {
	hash string
}

var (
	stateFileCacheMu sync.RWMutex
	stateFileCache   = make(map[string]cachedStateFile)
	stateFlushMu     sync.Mutex
	stateLastFlush   = make(map[string]time.Time)

	analysisFileCacheMu sync.RWMutex
	analysisFileCache   = make(map[string]cachedAnalysisFile)
	analysisFlushMu     sync.Mutex
	analysisLastFlush   = make(map[string]time.Time)

	hashFileCacheMu sync.RWMutex
	hashFileCache   = make(map[string]cachedHashFile)
)

const writeFlushInterval = 100 * time.Millisecond

// StateEntry stores per-file metadata for incremental hashing.
type StateEntry struct {
	RelPath         string `json:"relPath"`
	Size            int64  `json:"size"`
	ModTimeUnixNano int64  `json:"modTimeUnixNano"`
	ContentHash     string `json:"contentHash"`
	Language        string `json:"language,omitempty"`
	IsTest          bool   `json:"isTest,omitempty"`
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

func cloneCodemapState(state *CodemapState) *CodemapState {
	if state == nil {
		return nil
	}
	out := &CodemapState{
		Version:       state.Version,
		AggregateHash: state.AggregateHash,
	}
	if len(state.RootEntries) > 0 {
		out.RootEntries = append([]string(nil), state.RootEntries...)
	}
	if len(state.Dirs) > 0 {
		out.Dirs = append([]DirStateEntry(nil), state.Dirs...)
	}
	if len(state.Entries) > 0 {
		out.Entries = append([]StateEntry(nil), state.Entries...)
	}
	if state.Analysis != nil {
		out.Analysis = cloneAnalysisCache(state.Analysis)
	}
	return out
}

func cloneAnalysisCache(cache *AnalysisCache) *AnalysisCache {
	if cache == nil {
		return nil
	}
	out := &AnalysisCache{
		Version:           cache.Version,
		IncludeTests:      cache.IncludeTests,
		LargePackageFiles: cache.LargePackageFiles,
		ModulePath:        cache.ModulePath,
	}
	if len(cache.Packages) > 0 {
		out.Packages = make([]CachedPackage, len(cache.Packages))
		for i := range cache.Packages {
			out.Packages[i] = cache.Packages[i]
			out.Packages[i].FileRelPaths = append([]string(nil), cache.Packages[i].FileRelPaths...)
		}
	}
	return out
}

// ComputeHash computes a deterministic hash of all tracked source files in the project.
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
		return aggregate, cloneCodemapState(prev), nil
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
			Language:        rec.Language,
			IsTest:          rec.IsTest,
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

func dirRecordsFromState(dirs []DirStateEntry) []DirRecord {
	if len(dirs) == 0 {
		return nil
	}
	out := make([]DirRecord, len(dirs))
	for i, dir := range dirs {
		out[i] = DirRecord{
			RelPath:         dir.RelPath,
			ModTimeUnixNano: dir.ModTimeUnixNano,
		}
	}
	return out
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

	rootEntriesMatch, err := rootEntriesMatchState(absRoot, prev.RootEntries, ignoredRootEntries)
	if err != nil {
		return "", false, err
	}
	if !rootEntriesMatch {
		return "", false, nil
	}

	dirsMatch, err := directoriesMatchState(ctx, absRoot, prev.Dirs)
	if err != nil {
		return "", false, err
	}
	if !dirsMatch {
		return "", false, nil
	}

	filesMatch, err := filesMatchState(ctx, absRoot, prev.Entries)
	if err != nil {
		return "", false, err
	}
	if !filesMatch {
		return "", false, nil
	}

	return prev.AggregateHash, true, nil
}

func rootEntriesMatchState(absRoot string, expected []string, ignoredRootEntries map[string]struct{}) (bool, error) {
	currentRootEntries, err := os.ReadDir(absRoot)
	if err != nil {
		return false, err
	}
	expectedRootEntries := filterRootEntries(expected, ignoredRootEntries)
	actualRootEntries := make([]string, 0, len(currentRootEntries))
	for _, entry := range currentRootEntries {
		name := entry.Name()
		if _, ignored := ignoredRootEntries[name]; ignored {
			continue
		}
		actualRootEntries = append(actualRootEntries, name)
	}
	if len(actualRootEntries) != len(expectedRootEntries) {
		return false, nil
	}
	for i := range actualRootEntries {
		if actualRootEntries[i] != expectedRootEntries[i] {
			return false, nil
		}
	}
	return true, nil
}

func directoriesMatchState(ctx context.Context, absRoot string, dirs []DirStateEntry) (bool, error) {
	if len(dirs) == 0 {
		return true, nil
	}

	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > len(dirs) {
		workers = len(dirs)
	}
	if workers == 1 || len(dirs) < 128 {
		for _, dir := range dirs {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			default:
			}
			matched, err := directoryEntryMatches(absRoot, dir)
			if err != nil {
				return false, err
			}
			if !matched {
				return false, nil
			}
		}
		return true, nil
	}

	var mismatch atomic.Bool
	var firstErr error
	var errOnce sync.Once
	setErr := func(err error) {
		errOnce.Do(func() {
			firstErr = err
		})
		mismatch.Store(true)
	}

	var wg sync.WaitGroup
	wg.Add(workers)
	for workerID := 0; workerID < workers; workerID++ {
		go func(start int) {
			defer wg.Done()
			for idx := start; idx < len(dirs); idx += workers {
				if mismatch.Load() {
					return
				}
				select {
				case <-ctx.Done():
					return
				default:
				}
				matched, err := directoryEntryMatches(absRoot, dirs[idx])
				if err != nil {
					setErr(err)
					return
				}
				if !matched {
					mismatch.Store(true)
					return
				}
			}
		}(workerID)
	}
	wg.Wait()

	if firstErr != nil {
		return false, firstErr
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if mismatch.Load() {
		return false, nil
	}
	return true, nil
}

func directoryEntryMatches(absRoot string, dir DirStateEntry) (bool, error) {
	absDir := absRoot
	if dir.RelPath != "." {
		absDir = filepath.Join(absRoot, filepath.FromSlash(dir.RelPath))
	}
	info, err := os.Lstat(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() || info.ModTime().UnixNano() != dir.ModTimeUnixNano {
		return false, nil
	}
	return true, nil
}

func filesMatchState(ctx context.Context, absRoot string, entries []StateEntry) (bool, error) {
	if len(entries) == 0 {
		return true, nil
	}

	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > len(entries) {
		workers = len(entries)
	}
	if workers == 1 || len(entries) < 64 {
		for _, entry := range entries {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			default:
			}
			matched, err := fileEntryMatches(absRoot, entry)
			if err != nil {
				return false, err
			}
			if !matched {
				return false, nil
			}
		}
		return true, nil
	}

	var mismatch atomic.Bool
	var firstErr error
	var errOnce sync.Once
	setErr := func(err error) {
		errOnce.Do(func() {
			firstErr = err
		})
		mismatch.Store(true)
	}

	var wg sync.WaitGroup
	wg.Add(workers)
	for workerID := 0; workerID < workers; workerID++ {
		go func(start int) {
			defer wg.Done()
			for idx := start; idx < len(entries); idx += workers {
				if mismatch.Load() {
					return
				}
				select {
				case <-ctx.Done():
					return
				default:
				}
				matched, err := fileEntryMatches(absRoot, entries[idx])
				if err != nil {
					setErr(err)
					return
				}
				if !matched {
					mismatch.Store(true)
					return
				}
			}
		}(workerID)
	}
	wg.Wait()

	if firstErr != nil {
		return false, firstErr
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if mismatch.Load() {
		return false, nil
	}
	return true, nil
}

func fileEntryMatches(absRoot string, entry StateEntry) (bool, error) {
	if entry.ContentHash == "" {
		return false, nil
	}

	absPath := filepath.Join(absRoot, filepath.FromSlash(entry.RelPath))
	info, err := os.Lstat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.IsDir() {
		return false, nil
	}

	match, err := resolveStateEntryLanguage(entry, absPath)
	if err != nil {
		return false, err
	}
	if match.ID == "" {
		return false, nil
	}

	return info.Size() == entry.Size && info.ModTime().UnixNano() == entry.ModTimeUnixNano, nil
}

func resolveStateEntryLanguage(entry StateEntry, absPath string) (languageMatch, error) {
	if entry.Language != "" {
		return languageMatch{
			ID:     entry.Language,
			IsTest: entry.IsTest,
		}, nil
	}
	if match, ok := matchBuiltinLanguageForPath(entry.RelPath); ok && match.ID != "" {
		return match, nil
	}
	match, ok, err := detectLanguageForFile(absPath, entry.RelPath, allBuiltinLanguageSpecs())
	if err != nil {
		return languageMatch{}, err
	}
	if !ok || match.ID == "" {
		return languageMatch{}, nil
	}
	return match, nil
}

func buildFileIndexFromState(ctx context.Context, absRoot string, prev *CodemapState, ignoredRootEntries map[string]struct{}) (*FileIndex, bool, error) {
	if prev == nil || prev.Version != codemapStateVersion || len(prev.Entries) == 0 || prev.AggregateHash == "" {
		return nil, false, nil
	}

	rootMatch, err := rootEntriesMatchState(absRoot, prev.RootEntries, ignoredRootEntries)
	if err != nil {
		return nil, false, err
	}
	if !rootMatch {
		return nil, false, nil
	}

	dirsMatch, err := directoriesMatchState(ctx, absRoot, prev.Dirs)
	if err != nil {
		return nil, false, err
	}
	if !dirsMatch {
		return nil, false, nil
	}

	fileRecords := make([]FileRecord, len(prev.Entries))
	unchanged := atomic.Bool{}
	unchanged.Store(true)
	treeInvalid := atomic.Bool{}

	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > len(prev.Entries) {
		workers = len(prev.Entries)
	}

	processEntry := func(idx int) error {
		entry := prev.Entries[idx]
		absPath := filepath.Join(absRoot, filepath.FromSlash(entry.RelPath))
		info, err := os.Lstat(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				treeInvalid.Store(true)
				return nil
			}
			return err
		}
		if info.IsDir() {
			treeInvalid.Store(true)
			return nil
		}

		size := info.Size()
		modTimeUnixNano := info.ModTime().UnixNano()
		if size != entry.Size || modTimeUnixNano != entry.ModTimeUnixNano {
			unchanged.Store(false)
		}

		match, err := resolveStateEntryLanguage(entry, absPath)
		if err != nil {
			return err
		}
		if match.ID == "" {
			treeInvalid.Store(true)
			return nil
		}
		lang := match.ID
		fileRecords[idx] = FileRecord{
			AbsPath:         absPath,
			RelPath:         entry.RelPath,
			Size:            size,
			ModTimeUnixNano: modTimeUnixNano,
			Language:        lang,
			IsGo:            lang == languageGo,
			IsTest:          match.IsTest,
		}
		return nil
	}

	if workers == 1 || len(prev.Entries) < 128 {
		for i := range prev.Entries {
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			default:
			}
			if err := processEntry(i); err != nil {
				return nil, false, err
			}
			if treeInvalid.Load() {
				return nil, false, nil
			}
		}
	} else {
		var firstErr error
		var errOnce sync.Once
		setErr := func(err error) {
			errOnce.Do(func() {
				firstErr = err
			})
		}

		var stop atomic.Bool
		var wg sync.WaitGroup
		wg.Add(workers)
		for workerID := 0; workerID < workers; workerID++ {
			go func(start int) {
				defer wg.Done()
				for idx := start; idx < len(prev.Entries); idx += workers {
					if stop.Load() {
						return
					}
					select {
					case <-ctx.Done():
						stop.Store(true)
						return
					default:
					}
					if err := processEntry(idx); err != nil {
						setErr(err)
						stop.Store(true)
						return
					}
					if treeInvalid.Load() {
						stop.Store(true)
						return
					}
				}
			}(workerID)
		}
		wg.Wait()

		if firstErr != nil {
			return nil, false, firstErr
		}
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if treeInvalid.Load() {
			return nil, false, nil
		}
	}

	return &FileIndex{
		Root:        absRoot,
		RootEntries: append([]string(nil), prev.RootEntries...),
		Dirs:        dirRecordsFromState(prev.Dirs),
		Files:       fileRecords,
	}, unchanged.Load(), nil
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
	stateFileCacheMu.RLock()
	cached, ok := stateFileCache[path]
	stateFileCacheMu.RUnlock()
	if ok {
		return cached.state, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			stateFileCacheMu.Lock()
			delete(stateFileCache, path)
			stateFileCacheMu.Unlock()
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

	cloned := cloneCodemapState(&state)
	stateFileCacheMu.Lock()
	stateFileCache[path] = cachedStateFile{
		state: cloned,
	}
	stateFileCacheMu.Unlock()
	return cloned, nil
}

func writeState(path string, state *CodemapState) error {
	if state == nil {
		return nil
	}

	// Keep analysis cache out of the hot-path state file.
	stateForDisk := *state
	stateForDisk.Analysis = nil
	cachedCopy := cloneCodemapState(&stateForDisk)
	stateFileCacheMu.Lock()
	stateFileCache[path] = cachedStateFile{state: cachedCopy}
	stateFileCacheMu.Unlock()

	now := time.Now()
	stateFlushMu.Lock()
	lastFlush, haveFlush := stateLastFlush[path]
	if haveFlush && now.Sub(lastFlush) < writeFlushInterval {
		stateFlushMu.Unlock()
		return nil
	}
	stateLastFlush[path] = now
	stateFlushMu.Unlock()

	data, err := json.Marshal(stateForDisk)
	if err != nil {
		return err
	}

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
	analysisFileCacheMu.RLock()
	cached, ok := analysisFileCache[path]
	analysisFileCacheMu.RUnlock()
	if ok {
		return cached.cache, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			analysisFileCacheMu.Lock()
			delete(analysisFileCache, path)
			analysisFileCacheMu.Unlock()
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

	cacheCopy := cloneAnalysisCache(&cache)
	analysisFileCacheMu.Lock()
	analysisFileCache[path] = cachedAnalysisFile{
		cache: cacheCopy,
	}
	analysisFileCacheMu.Unlock()
	return cacheCopy, nil
}

func writeAnalysisCache(path string, cache *AnalysisCache) error {
	if cache == nil || len(cache.Packages) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		analysisFileCacheMu.Lock()
		delete(analysisFileCache, path)
		analysisFileCacheMu.Unlock()
		analysisFlushMu.Lock()
		delete(analysisLastFlush, path)
		analysisFlushMu.Unlock()
		return nil
	}

	cacheCopy := cloneAnalysisCache(cache)
	analysisFileCacheMu.Lock()
	analysisFileCache[path] = cachedAnalysisFile{cache: cacheCopy}
	analysisFileCacheMu.Unlock()

	now := time.Now()
	analysisFlushMu.Lock()
	lastFlush, haveFlush := analysisLastFlush[path]
	if haveFlush && now.Sub(lastFlush) < writeFlushInterval {
		analysisFlushMu.Unlock()
		return nil
	}
	analysisLastFlush[path] = now
	analysisFlushMu.Unlock()

	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}

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
	hashFileCacheMu.RLock()
	cached, ok := hashFileCache[path]
	hashFileCacheMu.RUnlock()
	if ok {
		return cached.hash, nil
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			hashFileCacheMu.Lock()
			delete(hashFileCache, path)
			hashFileCacheMu.Unlock()
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
			hashFileCacheMu.Lock()
			hashFileCache[path] = cachedHashFile{
				hash: hash,
			}
			hashFileCacheMu.Unlock()
			return hash, nil
		}
		if linesChecked >= 20 {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	hashFileCacheMu.Lock()
	hashFileCache[path] = cachedHashFile{
		hash: "",
	}
	hashFileCacheMu.Unlock()
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

func cacheExistingHash(path, hash string) {
	hashFileCacheMu.Lock()
	hashFileCache[path] = cachedHashFile{
		hash: hash,
	}
	hashFileCacheMu.Unlock()
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
	ignoredRootEntries := ignoredRootEntryNames(root, opts)
	idx, unchangedFromState, err := buildFileIndexFromState(ctx, root, state, ignoredRootEntries)
	if err != nil {
		return false, fmt.Errorf("build file index from state: %w", err)
	}

	var currentHash string
	if idx != nil {
		currentHash = state.AggregateHash
		if !unchangedFromState {
			currentHash, err = computeAggregateHashOnly(ctx, idx, state)
			if err != nil {
				return false, fmt.Errorf("compute hash: %w", err)
			}
		}
	} else {
		var matchedFromState bool
		currentHash, matchedFromState, err = aggregateHashFromFilesystemState(ctx, root, state, ignoredRootEntries)
		if err != nil {
			return false, fmt.Errorf("verify state: %w", err)
		}
		if !matchedFromState {
			idx, err = BuildFileIndex(ctx, root)
			if err != nil {
				return false, fmt.Errorf("build file index: %w", err)
			}
			currentHash, err = computeAggregateHashOnly(ctx, idx, state)
			if err != nil {
				return false, fmt.Errorf("compute hash: %w", err)
			}
		}
		if matchedFromState {
			currentHash = state.AggregateHash
		}
	}

	if currentHash == "" {
		idx, err = BuildFileIndex(ctx, root)
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
