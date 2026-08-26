package ui

import (
	"fmt"
	"os"
	"strings"
)

// windowLines trims a rendered list to `budget` terminal rows, keeping the
// cursor line in view and marking how many rows were cut off each end. A
// non-positive or generous budget renders everything, which is what tests and
// a terminal whose size is not yet known get.
func windowLines(lines []string, cursorLine, budget int) []string {
	if budget < 3 || len(lines) <= budget {
		return lines
	}

	// Up to two of the rows are spent on the more-above/more-below markers,
	// so the visible slice is smaller than the budget.
	visible := budget - 2
	start := 0
	if cursorLine >= 0 {
		start = cursorLine - visible/2
	}
	if start+visible > len(lines) {
		start = len(lines) - visible
	}
	if start < 0 {
		start = 0
	}
	end := start + visible
	if end > len(lines) {
		end = len(lines)
	}

	out := make([]string, 0, budget)
	if start > 0 {
		out = append(out, staleStyle.Render(fmt.Sprintf("  ↑ %d more", start)))
	} else {
		out = append(out, "")
	}
	out = append(out, lines[start:end]...)
	if end < len(lines) {
		out = append(out, staleStyle.Render(fmt.Sprintf("  ↓ %d more", len(lines)-end)))
	}
	return out
}

// abbreviateHome shortens a path under the user's home directory to a ~ form,
// so the config path fits on one line.
func abbreviateHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}
