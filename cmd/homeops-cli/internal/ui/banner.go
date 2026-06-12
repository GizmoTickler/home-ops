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
	if !isInteractive() {
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
