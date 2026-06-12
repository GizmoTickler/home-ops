package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// bannerArt is the homeops wordmark (ANSI Shadow-ish, hand-tuned to stay
// narrow enough for an 80-column terminal).
const bannerArt = `
 ██╗  ██╗ ██████╗ ███╗   ███╗███████╗ ██████╗ ██████╗ ███████╗
 ██║  ██║██╔═══██╗████╗ ████║██╔════╝██╔═══██╗██╔══██╗██╔════╝
 ███████║██║   ██║██╔████╔██║█████╗  ██║   ██║██████╔╝███████╗
 ██╔══██║██║   ██║██║╚██╔╝██║██╔══╝  ██║   ██║██╔═══╝ ╚════██║
 ██║  ██║╚██████╔╝██║ ╚═╝ ██║███████╗╚██████╔╝██║     ███████║
 ╚═╝  ╚═╝ ╚═════╝ ╚═╝     ╚═╝╚══════╝ ╚═════╝ ╚═╝     ╚══════╝`

// Banner renders the homeops wordmark with a vertical color gradient plus a
// tagline. Returns "" off-terminal (scripts/CI never see it).
func Banner(tagline string) string {
	if !isStyledOutput() {
		return ""
	}
	// magenta -> violet -> blue gradient, one shade per art line
	shades := []string{"201", "200", "171", "135", "99", "63"}
	lines := strings.Split(strings.Trim(bannerArt, "\n"), "\n")
	var b strings.Builder
	b.WriteByte('\n')
	for i, line := range lines {
		shade := shades[len(shades)-1]
		if i < len(shades) {
			shade = shades[i]
		}
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(shade)).Render(line))
		b.WriteByte('\n')
	}
	if tagline != "" {
		b.WriteString(lipgloss.NewStyle().Faint(true).Render(" " + tagline))
		b.WriteByte('\n')
	}
	return b.String()
}

// PrintBanner writes the banner to stdout (no-op off-terminal).
func PrintBanner(tagline string) {
	if banner := Banner(tagline); banner != "" {
		fmt.Print(banner + "\n")
	}
}

// SuccessBox renders a celebratory bordered box for milestone completions
// (e.g. bootstrap). Returns "" off-terminal — callers keep their plain
// logger line as the CI/pipe fallback.
func SuccessBox(title string, lines ...string) string {
	if !isStyledOutput() {
		return ""
	}
	body := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42")).Render(title)
	if len(lines) > 0 {
		body += "\n" + lipgloss.NewStyle().Faint(true).Render(strings.Join(lines, "\n"))
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("42")).
		Padding(0, 2).
		Render(body)
	return box
}

// PrintSuccessBox writes SuccessBox to stdout (no-op off-terminal).
func PrintSuccessBox(title string, lines ...string) {
	if box := SuccessBox(title, lines...); box != "" {
		fmt.Println(box)
	}
}

// InfoBox renders a neutral bordered panel for section headers (e.g. the
// bootstrap plan). Returns "" off-terminal — callers keep their plain logger
// lines as the CI/pipe fallback.
func InfoBox(title string, lines ...string) string {
	if !isStyledOutput() {
		return ""
	}
	body := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("75")).Render(title)
	if len(lines) > 0 {
		body += "\n" + strings.Join(lines, "\n")
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("75")).
		Padding(0, 2).
		Render(body)
}

// PrintInfoBox writes InfoBox to stdout (no-op off-terminal).
func PrintInfoBox(title string, lines ...string) {
	if box := InfoBox(title, lines...); box != "" {
		fmt.Println(box)
	}
}
