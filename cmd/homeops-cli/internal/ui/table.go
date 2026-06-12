package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/mattn/go-isatty"
)

// isStyledOutput reports whether stdout is a terminal that should get the
// lipgloss-styled rendering (piped/CI output stays plain and parseable).
// A var so tests can exercise the styled renderers without a TTY.
var isStyledOutput = func() bool {
	return !isInteractiveDisabled() && isatty.IsTerminal(os.Stdout.Fd())
}

// Table renders a column-aligned table: lipgloss-styled (rounded border,
// bold headers) on a terminal, plain whitespace-aligned columns when piped.
// This is THE table renderer for the CLI — new tabular output should use it
// instead of hand-rolled fmt.Printf width strings.
func Table(headers []string, rows [][]string) string {
	if isStyledOutput() {
		return styledTable(headers, rows)
	}
	return plainTable(headers, rows)
}

// PrintTable writes Table's output to stdout (with a trailing newline).
func PrintTable(headers []string, rows [][]string) {
	fmt.Println(Table(headers, rows))
}

func plainTable(headers []string, rows [][]string) string {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	var b strings.Builder
	writeRow := func(cells []string) {
		for i, cell := range cells {
			if i >= len(widths) {
				break
			}
			if i == len(cells)-1 || i == len(widths)-1 {
				b.WriteString(cell)
			} else {
				fmt.Fprintf(&b, "%-*s  ", widths[i], cell)
			}
		}
		b.WriteString("\n")
	}
	writeRow(headers)
	for _, row := range rows {
		writeRow(row)
	}
	return strings.TrimRight(b.String(), "\n")
}

func styledTable(headers []string, rows [][]string) string {
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99")).Padding(0, 1)
	cellStyle := lipgloss.NewStyle().Padding(0, 1)
	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("240"))).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle
			}
			return cellStyle
		})
	return t.Render()
}
