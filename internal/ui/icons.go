package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ericdahl-dev/coolify-green/internal/aggregator"
)

// containerStatusLabel renders a Coolify resource status the way a human reads
// it: "running", "unhealthy", "stopped" — not "exited:unhealthy".
func containerStatusLabel(status string) (string, lipgloss.Style) {
	state, health := aggregator.SplitContainerStatus(status)
	switch state {
	case "running":
		switch health {
		case "unhealthy":
			return "✗ unhealthy", errorStyle
		case "starting":
			return "↻ starting", confirmStyle
		case "healthy":
			return "✓ healthy", successStyle
		default:
			return "✓ running", successStyle
		}
	case "restarting":
		return "↻ restarting", confirmStyle
	case "starting", "created":
		return "↻ starting", confirmStyle
	case "exited", "stopped", "dead":
		return "✗ " + state, errorStyle
	case "degraded":
		return "✗ degraded", errorStyle
	case "":
		return "— no status", staleStyle
	default:
		return strings.ReplaceAll(status, ":", " "), staleStyle
	}
}

// deployStatusLabel renders a deployment status with its own icon.
func deployStatusLabel(status string) (string, lipgloss.Style) {
	switch aggregator.DeployStatus(status) {
	case aggregator.DeployFinished:
		return "✓ deployed", successStyle
	case aggregator.DeployInProgress:
		return "↻ deploying", confirmStyle
	case aggregator.DeployQueued:
		return "↻ queued", confirmStyle
	case aggregator.DeployFailed:
		return "✗ deploy failed", errorStyle
	case aggregator.DeployCancelled:
		return "⊘ deploy cancelled", staleStyle
	case "":
		return "", staleStyle
	default:
		return strings.ReplaceAll(status, "_", " "), staleStyle
	}
}
