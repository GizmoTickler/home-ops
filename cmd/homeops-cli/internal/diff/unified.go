// Package diff provides shared text-diff rendering for command previews.
package diff

import (
	"fmt"
	"strings"
)

type operation struct {
	prefix byte
	line   string
}

// Unified renders a complete LCS-based unified diff for before and after.
func Unified(path string, before, after []byte) string {
	oldLines := strings.Split(strings.TrimSuffix(string(before), "\n"), "\n")
	newLines := strings.Split(strings.TrimSuffix(string(after), "\n"), "\n")
	operations := lcsOperations(oldLines, newLines)
	return renderUnified(path, operations, 1, len(oldLines), 1, len(newLines))
}

// UnifiedContext renders the changed hunk with up to context unchanged lines
// on either side, using the same LCS operation stream as Unified.
func UnifiedContext(path string, before, after []byte, context int) string {
	oldLines := strings.Split(strings.TrimSuffix(string(before), "\n"), "\n")
	newLines := strings.Split(strings.TrimSuffix(string(after), "\n"), "\n")
	operations := lcsOperations(oldLines, newLines)
	first, last := changedRange(operations)
	if first < 0 {
		return renderUnified(path, operations, 1, len(oldLines), 1, len(newLines))
	}
	start, end := contextualRange(operations, first, last, context)
	oldStart, newStart := lineStarts(operations[:start])
	oldCount, newCount := lineCounts(operations[start:end])
	return renderUnified(path, operations[start:end], oldStart, oldCount, newStart, newCount)
}

func lcsOperations(oldLines, newLines []string) []operation {
	lcs := make([][]int, len(oldLines)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(newLines)+1)
	}
	for i := len(oldLines) - 1; i >= 0; i-- {
		for j := len(newLines) - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	operations := make([]operation, 0, len(oldLines)+len(newLines))
	for i, j := 0, 0; i < len(oldLines) || j < len(newLines); {
		switch {
		case i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j]:
			operations = append(operations, operation{prefix: ' ', line: oldLines[i]})
			i++
			j++
		case i < len(oldLines) && (j == len(newLines) || lcs[i+1][j] >= lcs[i][j+1]):
			operations = append(operations, operation{prefix: '-', line: oldLines[i]})
			i++
		default:
			operations = append(operations, operation{prefix: '+', line: newLines[j]})
			j++
		}
	}
	return operations
}

func changedRange(operations []operation) (int, int) {
	first, last := -1, -1
	for i, operation := range operations {
		if operation.prefix != ' ' {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	return first, last
}

func contextualRange(operations []operation, first, last, context int) (int, int) {
	start := first
	for remaining := context; start > 0 && remaining > 0; remaining-- {
		start--
	}
	end := last + 1
	for remaining := context; end < len(operations) && remaining > 0; remaining-- {
		end++
	}
	return start, end
}

func lineStarts(operations []operation) (int, int) {
	oldCount, newCount := lineCounts(operations)
	return oldCount + 1, newCount + 1
}

func lineCounts(operations []operation) (int, int) {
	var oldCount, newCount int
	for _, operation := range operations {
		if operation.prefix != '+' {
			oldCount++
		}
		if operation.prefix != '-' {
			newCount++
		}
	}
	return oldCount, newCount
}

func renderUnified(path string, operations []operation, oldStart, oldCount, newStart, newCount int) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "--- a/%s\n+++ b/%s\n@@ -%d,%d +%d,%d @@\n", path, path, oldStart, oldCount, newStart, newCount)
	for _, operation := range operations {
		builder.WriteByte(operation.prefix)
		builder.WriteString(strings.TrimSuffix(operation.line, "\r"))
		builder.WriteByte('\n')
	}
	return builder.String()
}
