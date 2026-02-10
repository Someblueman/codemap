package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileRecord describes a discovered file in the project tree.
type FileRecord struct {
	AbsPath         string
	RelPath         string
	Size            int64
	ModTimeUnixNano int64
	IsGo            bool
	IsTest          bool
}

// FileIndex is a deterministic snapshot of files under a project root.
type FileIndex struct {
	Root  string
	Files []FileRecord
}

// BuildFileIndex walks root once and captures all files needed by codemap.
func BuildFileIndex(ctx context.Context, root string) (*FileIndex, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}

	idx := &FileIndex{Root: absRoot}
	err = filepath.Walk(absRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if info.IsDir() {
			if path != absRoot && isExcludedDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, err := filepath.Rel(absRoot, path)
		if err != nil {
			relPath = path
		}
		relPath = filepath.ToSlash(relPath)

		name := info.Name()
		isGo := strings.HasSuffix(name, ".go")
		if !isGo {
			return nil
		}

		idx.Files = append(idx.Files, FileRecord{
			AbsPath:         path,
			RelPath:         relPath,
			Size:            info.Size(),
			ModTimeUnixNano: info.ModTime().UnixNano(),
			IsGo:            isGo,
			IsTest:          strings.HasSuffix(name, "_test.go"),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk directory: %w", err)
	}

	sort.Slice(idx.Files, func(i, j int) bool {
		return idx.Files[i].RelPath < idx.Files[j].RelPath
	})

	return idx, nil
}

func isExcludedDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "workspace"
}
