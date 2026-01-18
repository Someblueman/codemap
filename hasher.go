package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ComputeHash computes a SHA256 hash of all Go files in the project.
func ComputeHash(ctx context.Context, root string) (string, error) {
	var files []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		name := info.Name()
		if info.IsDir() {
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "workspace" {
				return filepath.SkipDir
			}
			return nil
		}

		if strings.HasSuffix(name, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk directory: %w", err)
	}

	// Sort for determinism
	sort.Strings(files)

	h := sha256.New()

	for _, file := range files {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		// Include relative path in hash
		rel, err := filepath.Rel(root, file)
		if err != nil {
			rel = file
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})

		// Include file contents
		f, err := os.Open(file)
		if err != nil {
			return "", fmt.Errorf("open %s: %w", file, err)
		}

		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", fmt.Errorf("read %s: %w", file, err)
		}
		f.Close()

		h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil)), nil
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
		// Only check first 20 lines
		if linesChecked >= 20 {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	return "", nil
}

// IsStale checks if the CODEMAP.md file is stale.
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

	// Read existing hash
	existingHash, err := ReadExistingHash(outputPath)
	if err != nil {
		return false, fmt.Errorf("read existing hash: %w", err)
	}

	// No existing file or no hash found
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

	// Compute current hash
	currentHash, err := ComputeHash(ctx, root)
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
