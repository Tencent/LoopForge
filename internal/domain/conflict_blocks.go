package domain

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Extraction limits keep blockers and prompts bounded on nasty merges.
const (
	maxBlocksPerFile  = 4
	maxTotalBlocks    = 8
	maxBlockSideRunes = 1600
)

// ExtractConflictBlocks reads conflicted worktree files (they carry merge
// markers during the failed merge) and returns clipped hunks. Binary files
// are skipped.
func ExtractConflictBlocks(dir string, files []string) []ConflictBlock {
	var out []ConflictBlock
	for _, f := range files {
		if len(out) >= maxTotalBlocks {
			break
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil || len(data) == 0 || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		out = append(out, ParseConflictMarkers(f, data, maxBlocksPerFile)...)
	}
	if len(out) > maxTotalBlocks {
		out = out[:maxTotalBlocks]
	}
	return out
}

// ParseConflictMarkers extracts up to max hunks from `<<<<<<< / ======= /
// >>>>>>>` sections. ours = integrate side (HEAD), theirs = default side.
func ParseConflictMarkers(file string, data []byte, max int) []ConflictBlock {
	lines := strings.Split(string(data), "\n")
	var out []ConflictBlock
	var ours, theirs []string
	state := 0 // 0 normal, 1 ours, 2 theirs
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "<<<<<<<"):
			if state == 2 { // malformed nesting: flush what we have
				out = append(out, newConflictBlock(file, ours, theirs))
				if len(out) >= max {
					return out
				}
			}
			ours, theirs = nil, nil
			state = 1
		case strings.HasPrefix(ln, "=======") && state == 1:
			state = 2
		case strings.HasPrefix(ln, ">>>>>>>") && state == 2:
			out = append(out, newConflictBlock(file, ours, theirs))
			ours, theirs = nil, nil
			state = 0
			if len(out) >= max {
				return out
			}
		default:
			switch state {
			case 1:
				ours = append(ours, ln)
			case 2:
				theirs = append(theirs, ln)
			}
		}
	}
	return out
}

func newConflictBlock(file string, ours, theirs []string) ConflictBlock {
	return ConflictBlock{File: file, Ours: clipConflictLines(ours), Theirs: clipConflictLines(theirs)}
}

func clipConflictLines(ls []string) string {
	const maxLines = 40
	if len(ls) > maxLines {
		ls = append(append([]string{}, ls[:maxLines]...), "…（截断）")
	}
	s := strings.Join(ls, "\n")
	if len([]rune(s)) > maxBlockSideRunes {
		r := []rune(s)
		s = string(r[:maxBlockSideRunes]) + "…（截断）"
	}
	return s
}
