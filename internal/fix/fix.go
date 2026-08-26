// Package fix decides what single action would most likely unstick a project,
// and runs it against the Coolify API once the user confirms.
package fix

import (
	"context"
	"fmt"
	"time"

	"github.com/ericdahl-dev/coolify-green/internal/aggregator"
	"github.com/ericdahl-dev/coolify-green/internal/coolify"
	"github.com/ericdahl-dev/coolify-green/internal/state"
)

// Kind identifies what action a Plan will take.
type Kind int

const (
	KindRedeploy        Kind = iota // POST /deploy?force=true
	KindCancelDeploy                // POST /deployments/{uuid}/cancel
	KindStartResource               // POST /{kind}/{uuid}/start
	KindRestartResource             // POST /{kind}/{uuid}/restart
)

func (k Kind) String() string {
	switch k {
	case KindRedeploy:
		return "redeploy"
	case KindCancelDeploy:
		return "cancel deployment"
	case KindStartResource:
		return "start"
	case KindRestartResource:
		return "restart"
	default:
		return "unknown"
	}
}

// StalledDeployThreshold is how long a deploy has to run before the fix key
// offers to cancel it rather than wait.
const StalledDeployThreshold = 30 * time.Minute

// Plan describes the action and carries everything needed to execute it.
type Plan struct {
	Kind        Kind
	Description string // plain-English confirmation text

	Instance       string
	ResourceKind   coolify.Kind
	ResourceUUID   string
	ResourceName   string
	DeploymentUUID string
}

// PlanFor returns the highest-priority action for a project, or nil when
// nothing needs fixing.
//
// A deploy that is still running outranks everything: no other action can
// succeed while Coolify holds the build lock, and a healthy in-flight deploy
// is not a problem at all, so it yields no plan until it stalls.
func PlanFor(proj state.ProjectState, stalledAfter time.Duration) *Plan {
	if stalledAfter <= 0 {
		stalledAfter = StalledDeployThreshold
	}

	for _, r := range proj.Resources {
		if r.Deploy == nil || !r.Deploy.InFlight() {
			continue
		}
		if r.Deploy.Elapsed() < stalledAfter {
			// A deploy that is simply still running is not a fault.
			return nil
		}
		return &Plan{
			Kind:           KindCancelDeploy,
			Instance:       proj.Instance,
			ResourceKind:   r.Kind,
			ResourceUUID:   r.UUID,
			ResourceName:   r.Name,
			DeploymentUUID: r.Deploy.UUID,
			Description: fmt.Sprintf("cancel stalled deployment of %s (%s, running %s)",
				r.Name, r.Deploy.Status, formatDuration(r.Deploy.Elapsed())),
		}
	}

	// A failed deploy is the most common cause of a red row, and redeploying
	// is what a human would do next.
	for _, r := range proj.Resources {
		if r.Deploy == nil || aggregator.DeployStatus(r.Deploy.Status) != aggregator.DeployFailed {
			continue
		}
		return &Plan{
			Kind:         KindRedeploy,
			Instance:     proj.Instance,
			ResourceKind: r.Kind,
			ResourceUUID: r.UUID,
			ResourceName: r.Name,
			Description: fmt.Sprintf("redeploy %s (last deploy %s failed)",
				r.Name, deployLabel(*r.Deploy)),
		}
	}

	for _, r := range proj.Resources {
		containerState, health := aggregator.SplitContainerStatus(r.Status)
		switch {
		case containerState == "exited", containerState == "stopped", containerState == "dead":
			return &Plan{
				Kind:         KindStartResource,
				Instance:     proj.Instance,
				ResourceKind: r.Kind,
				ResourceUUID: r.UUID,
				ResourceName: r.Name,
				Description:  fmt.Sprintf("start %s %s (%s)", r.Kind.Label(), r.Name, r.Status),
			}
		case containerState == "running" && health == "unhealthy":
			return &Plan{
				Kind:         KindRestartResource,
				Instance:     proj.Instance,
				ResourceKind: r.Kind,
				ResourceUUID: r.UUID,
				ResourceName: r.Name,
				Description:  fmt.Sprintf("restart %s %s (failing its health check)", r.Kind.Label(), r.Name),
			}
		}
	}

	return nil
}

func deployLabel(d state.DeployState) string {
	if d.Commit == "" {
		return "(unknown commit)"
	}
	return d.Commit
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// Actioner executes a Plan against a Coolify instance. *coolify.Client
// satisfies it.
type Actioner interface {
	Deploy(ctx context.Context, uuid string, force bool) error
	CancelDeployment(ctx context.Context, deploymentUUID string) error
	Start(ctx context.Context, kind coolify.Kind, uuid string) error
	Restart(ctx context.Context, kind coolify.Kind, uuid string) error
}

// Execute runs the plan.
func Execute(ctx context.Context, plan *Plan, a Actioner) error {
	if plan == nil {
		return fmt.Errorf("no fix to apply")
	}
	switch plan.Kind {
	case KindRedeploy:
		return a.Deploy(ctx, plan.ResourceUUID, true)
	case KindCancelDeploy:
		return a.CancelDeployment(ctx, plan.DeploymentUUID)
	case KindStartResource:
		return a.Start(ctx, plan.ResourceKind, plan.ResourceUUID)
	case KindRestartResource:
		return a.Restart(ctx, plan.ResourceKind, plan.ResourceUUID)
	default:
		return fmt.Errorf("unknown fix kind: %v", plan.Kind)
	}
}
