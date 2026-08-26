package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var titleBarStyle = lipgloss.NewStyle().Bold(true)

// TitleLine renders the app title and, while a fetch is in flight, a spinner.
func TitleLine(fetching bool, spinView string) string {
	s := strings.TrimSpace(spinView)
	if fetching && s != "" {
		return titleBarStyle.Render("coolify-green "+s) + "\n"
	}
	return titleBarStyle.Render("coolify-green") + "\n"
}
