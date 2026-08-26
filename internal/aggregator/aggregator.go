// Package aggregator turns Coolify's status strings into stoplight colors.
package aggregator

import "strings"

// Stoplight represents the health color for a resource or project.
type Stoplight int

const (
	StoplightGrey   Stoplight = iota // unknown, or nothing deployed yet
	StoplightGreen                   // running, last deployment finished
	StoplightYellow                  // deploying, starting, restarting
	StoplightRed                     // down, unhealthy, or last deployment failed
)

func (s Stoplight) String() string {
	switch s {
	case StoplightGreen:
		return "🟢"
	case StoplightRed:
		return "🔴"
	case StoplightYellow:
		return "🟡"
	default:
		return "⚪"
	}
}

// DeployStatus mirrors the status values Coolify reports for a deployment.
type DeployStatus string

const (
	DeployQueued     DeployStatus = "queued"
	DeployInProgress DeployStatus = "in_progress"
	DeployFinished   DeployStatus = "finished"
	DeployFailed     DeployStatus = "failed"
	DeployCancelled  DeployStatus = "cancelled-by-user"
)

// InFlight reports whether a deployment is still running or waiting to run.
func (d DeployStatus) InFlight() bool {
	return d == DeployQueued || d == DeployInProgress
}

// DeployStoplight maps a Coolify deployment status to a Stoplight.
func DeployStoplight(status string) Stoplight {
	switch DeployStatus(strings.TrimSpace(status)) {
	case DeployFinished:
		return StoplightGreen
	case DeployQueued, DeployInProgress:
		return StoplightYellow
	case DeployFailed:
		return StoplightRed
	case DeployCancelled:
		return StoplightGrey
	default:
		return StoplightGrey
	}
}

// SplitContainerStatus splits Coolify's "<state>:<health>" resource status into
// its two halves. Services and databases often report a bare state with no
// health suffix, in which case health is empty.
func SplitContainerStatus(status string) (state, health string) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		return "", ""
	}
	state, health, _ = strings.Cut(status, ":")
	return strings.TrimSpace(state), strings.TrimSpace(health)
}

// ContainerStoplight maps a Coolify resource status to a Stoplight.
//
// A running container whose health check is not configured reports
// "running:unknown" — the majority of resources on a typical instance — so
// that has to be green, not a warning. Only an explicit "unhealthy" is red.
func ContainerStoplight(status string) Stoplight {
	state, health := SplitContainerStatus(status)
	switch state {
	case "running":
		switch health {
		case "unhealthy":
			return StoplightRed
		case "starting":
			return StoplightYellow
		default:
			return StoplightGreen
		}
	case "restarting", "starting", "created", "restarting:unhealthy":
		return StoplightYellow
	case "exited", "stopped", "dead", "paused", "removing", "degraded":
		return StoplightRed
	default:
		return StoplightGrey
	}
}

// Aggregate returns the worst-case Stoplight across the provided lights.
// Red > Yellow > Green > Grey.
func Aggregate(lights ...Stoplight) Stoplight {
	worst := StoplightGrey
	for _, l := range lights {
		if l > worst {
			worst = l
		}
	}
	return worst
}

// SortPriority orders stoplights for the dashboard so the most actionable rows
// surface first: in-progress, then broken, then healthy, then unknown.
func SortPriority(s Stoplight) int {
	switch s {
	case StoplightYellow:
		return 0
	case StoplightRed:
		return 1
	case StoplightGreen:
		return 2
	default:
		return 3
	}
}
