package main

import (
	"context"
	"fmt"
	"io/fs"
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
	Language        string
	IsGo            bool
	IsTest          bool
}

// DirRecord describes a discovered directory in the project tree.
type DirRecord struct {
	RelPath         string
	ModTimeUnixNano int64
}

// FileIndex is a deterministic snapshot of files under a project root.
type FileIndex struct {
	Root        string
	RootEntries []string
	Dirs        []DirRecord
	Files       []FileRecord
}

// BuildFileIndex walks root once and captures all files needed by codemap.
func BuildFileIndex(ctx context.Context, root string) (*FileIndex, error) {
	return BuildFileIndexWithLanguages(ctx, root, defaultLanguageSpecs())
}

// BuildFileIndexWithLanguages walks root once and captures files matching configured languages.
func BuildFileIndexWithLanguages(ctx context.Context, root string, languageSpecs []LanguageSpec) (*FileIndex, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	rootPrefix := absRoot + string(os.PathSeparator)

	idx := &FileIndex{Root: absRoot}
	err = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if path != absRoot && filepath.Dir(path) == absRoot {
			idx.RootEntries = append(idx.RootEntries, d.Name())
		}

		if d.IsDir() {
			if path != absRoot && isExcludedDir(d.Name()) {
				return filepath.SkipDir
			}
			info, err := d.Info()
			if err != nil {
				return err
			}

			relPath := "."
			if path != absRoot {
				relPath = path
				if strings.HasPrefix(path, rootPrefix) {
					relPath = path[len(rootPrefix):]
					if os.PathSeparator != '/' {
						relPath = filepath.ToSlash(relPath)
					}
				} else {
					relPath, err = filepath.Rel(absRoot, path)
					if err != nil {
						relPath = filepath.ToSlash(path)
					} else {
						relPath = filepath.ToSlash(relPath)
					}
				}
			}

			idx.Dirs = append(idx.Dirs, DirRecord{
				RelPath:         relPath,
				ModTimeUnixNano: info.ModTime().UnixNano(),
			})
			return nil
		}

		name := d.Name()
		langMatch, ok := matchLanguageForPath(name, languageSpecs)
		if !ok {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		relPath := path
		if strings.HasPrefix(path, rootPrefix) {
			relPath = path[len(rootPrefix):]
			if os.PathSeparator != '/' {
				relPath = filepath.ToSlash(relPath)
			}
		} else {
			relPath, err = filepath.Rel(absRoot, path)
			if err != nil {
				relPath = filepath.ToSlash(path)
			} else {
				relPath = filepath.ToSlash(relPath)
			}
		}

		idx.Files = append(idx.Files, FileRecord{
			AbsPath:         path,
			RelPath:         relPath,
			Size:            info.Size(),
			ModTimeUnixNano: info.ModTime().UnixNano(),
			Language:        langMatch.ID,
			IsGo:            langMatch.ID == languageGo,
			IsTest:          langMatch.IsTest,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk directory: %w", err)
	}
	sort.Strings(idx.RootEntries)

	return idx, nil
}

func isExcludedDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "workspace"
}
