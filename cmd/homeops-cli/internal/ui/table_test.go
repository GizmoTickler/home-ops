package ui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlainTableAlignsColumns(t *testing.T) {
	out := plainTable(
		[]string{"NAME", "ID", "STATUS"},
		[][]string{
			{"web0", "7", "RUNNING"},
			{"a-much-longer-name", "104", "STOPPED"},
		},
	)
	lines := strings.Split(out, "\n")
	require.Len(t, lines, 3)
	assert.Equal(t, "NAME                ID   STATUS", lines[0])
	assert.Equal(t, "web0                7    RUNNING", lines[1])
	assert.Equal(t, "a-much-longer-name  104  STOPPED", lines[2])
}

func TestPlainTableHeadersOnly(t *testing.T) {
	out := plainTable([]string{"NAME"}, nil)
	assert.Equal(t, "NAME", out)
}

func TestTableIsPlainOffTerminal(t *testing.T) {
	// Tests run without a TTY on stdout, so Table must take the plain path.
	out := Table([]string{"A", "B"}, [][]string{{"1", "2"}})
	assert.NotContains(t, out, "╭", "no borders when piped")
	assert.Contains(t, out, "A")
	assert.Contains(t, out, "1")
}

func TestStyledTableRendersBordersAndCells(t *testing.T) {
	out := styledTable([]string{"NAME", "ID"}, [][]string{{"web0", "7"}})
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "web0")
	assert.Contains(t, out, "╭", "styled mode draws a border")
}
