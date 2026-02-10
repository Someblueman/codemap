package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

const codemapStateVersion = 1

// StateEntry stores per-file metadata for incremental hashing.
type StateEntry struct {
	RelPath         string `json:"relPath"`
	Size            int64  `json:"size"`
	ModTimeUnixNano int64  `json:"modTimeUnixNano"`
	ContentHash     string `json:"contentHash"`
}

// CodemapState stores local cache metadata for staleness checks.
type CodemapState struct {
	Version       int          `json:"version"`
	AggregateHash string       `json:"aggregateHash"`
	Entries       []StateEntry `json:"entries"`
}

// ComputeHash computes a deterministic hash of all Go files in the project.
func ComputeHash(ctx context.Context, root string) (string, error) {
	idx, err := BuildFileIndex(ctx, root)
	if err != nil {
		return "", fmt.Errorf("build file index: %w", err)
	}

	hash, _, err := computeAggregateHash(ctx, idx, nil)
	if err != nil {
		return "", err
	}
	return hash, nil
}

func computeAggregateHash(ctx context.Context, idx *FileIndex, prev *CodemapState) (string, *CodemapState, error) {
	prevByPath := make(map[string]StateEntry)
	if prev != nil && prev.Version == codemapStateVersion {
		for _, entry := range prev.Entries {
			prevByPath[entry.RelPath] = entry
		}
	}

	h := sha256.New()
	entries := make([]StateEntry, 0, len(idx.Files))
	for _, rec := range idx.Files {
		if !rec.IsGo {
			continue
		}

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

		if cached, ok := prevByPath[rec.RelPath]; ok && cached.Size == rec.Size && cached.ModTimeUnixNano == rec.ModTimeUnixNano && cached.ContentHash != "" {
			entry.ContentHash = cached.ContentHash
		} else {
			contentHash, err := hashFileContents(rec.AbsPath)
			if err != nil {
				return "", nil, fmt.Errorf("hash %s: %w", rec.RelPath, err)
			}
			entry.ContentHash = contentHash
		}

		entries = append(entries, entry)
		h.Write([]byte(entry.RelPath))
		h.Write([]byte{0})
		h.Write([]byte(entry.ContentHash))
		h.Write([]byte{0})
	}

	aggregate := hex.EncodeToString(h.Sum(nil))
	next := &CodemapState{
		Version:       codemapStateVersion,
		AggregateHash: aggregate,
		Entries:       entries,
	}
	return aggregate, next, nil
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

	sort.Slice(state.Entries, func(i, j int) bool {
		return state.Entries[i].RelPath < state.Entries[j].RelPath
	})
	return &state, nil
}

func writeState(path string, state *CodemapState) error {
	if state == nil {
		return nil
	}

	data, err := json.MarshalIndent(state, "", "  ")
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

var hashPattern = regexp.MustCompile(`^(?:<!--\s*|#\s*)?codemap-hash:\s*([a-f0-9]+)\s*(?:-->)?\s*$`)

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
		if matches := hashPattern.FindStringSubmatch(line); len(matches) > 1 {
			return matches[1], nil
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

	idx, err := BuildFileIndex(ctx, root)
	if err != nil {
		return false, fmt.Errorf("build file index: %w", err)
	}

	state, err := readState(resolveStatePath(root, opts))
	if err != nil {
		return false, fmt.Errorf("read state: %w", err)
	}
	currentHash, _, err := computeAggregateHash(ctx, idx, state)
	if err != nil {
		return false, fmt.Errorf("compute hash: %w", err)
	}

	if existingHash != currentHash {
		return true, nil
	}
	if !opts.DisablePaths && existingPathsHash != currentHash {
		return true, nil
	}

	return false, nil
}
