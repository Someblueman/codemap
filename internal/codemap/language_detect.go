package codemap

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// detectLanguageForFile infers language using suffix rules first, then shell shebangs for extensionless scripts.
func detectLanguageForFile(absPath, candidatePath string, specs []LanguageSpec) (languageMatch, bool, error) {
	if match, ok := matchLanguageForPath(candidatePath, specs); ok {
		return match, true, nil
	}
	if !languageEnabled(specs, languageShell) {
		return languageMatch{}, false, nil
	}

	base := strings.ToLower(filepath.Base(candidatePath))
	if strings.Contains(base, ".") {
		return languageMatch{}, false, nil
	}

	program, ok, err := readShebangProgram(absPath)
	if err != nil {
		return languageMatch{}, false, err
	}
	if !ok {
		return languageMatch{}, false, nil
	}
	switch program {
	case "sh", "bash":
		return languageMatch{
			ID:     languageShell,
			IsTest: isShellTestPathLike(base),
		}, true, nil
	default:
		return languageMatch{}, false, nil
	}
}

func readShebangProgram(path string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 256)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF && !errors.Is(err, bufio.ErrBufferFull) {
		return "", false, err
	}

	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "#!") {
		return "", false, nil
	}

	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "#!")))
	if len(fields) == 0 {
		return "", false, nil
	}

	program := filepath.Base(fields[0])
	if program == "env" {
		program = ""
		for _, field := range fields[1:] {
			if strings.HasPrefix(field, "-") {
				continue
			}
			program = filepath.Base(field)
			break
		}
	}

	program = strings.ToLower(strings.TrimSpace(program))
	if program == "" {
		return "", false, nil
	}
	return program, true, nil
}
