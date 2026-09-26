package poller

import (
	"fmt"
	"time"

	"github.com/ericdahl-dev/coolify-green/internal/aggregator"
	"github.com/ericdahl-dev/coolify-green/internal/state"
	"github.com/ericdahl-dev/coolify-green/internal/webhooks"
)

// stuckKey is the identity a wedged resource is tracked under. Project names
// repeat across instances and slice positions shift as resources come and go,
// so every key is instance- and project-qualified.
func stuckKey(ps state.ProjectState, resourceUUID, kind string) string {
	return ps.Key() + "|" + kind + "|" + resourceUUID
}

// evaluateStuck folds freshly polled state into the stuck bookkeeping and
// returns the webhook events to send. A resource that stays stuck keeps its
// original stuck-since timestamp and fires exactly once — on the cycle where
// it crosses the threshold — rather than on every poll. Recovering clears the
// entry, so the next incident alerts again.
//
// Callers must hold p.mu. Dispatch is left to the caller so the HTTP calls
// happen outside the lock.
func (p *Poller) evaluateStuck(projects []state.ProjectState, instances []state.InstanceState) []webhooks.Event {
	threshold := time.Duration(p.cfg.Settings.StuckThresholdMinutes) * time.Minute
	now := p.now()
	seen := make(map[string]struct{}, len(p.stuck))
	var events []webhooks.Event

	// track records the stuck condition for one resource and appends an event
	// if this is the cycle that crosses the threshold.
	track := func(key, reason string, build func(since time.Time) webhooks.Event) {
		seen[key] = struct{}{}
		entry, ok := p.stuck[key]
		// A changed reason — a running deploy turning into a failure — is a
		// new condition, so restart the clock and allow a fresh alert.
		if !ok || entry.reason != reason {
			entry = &stuckEntry{since: now, reason: reason}
			p.stuck[key] = entry
		}
		if entry.alerted || now.Sub(entry.since) < threshold {
			return
		}
		entry.alerted = true
		events = append(events, build(entry.since))
	}

	// A fetch that keeps failing is its own kind of stuck. Every row for the
	// instance is frozen at whatever it showed when the fetch died, and the
	// checks below deliberately skip that stale data, so without this a
	// revoked token or a dead instance alerts nowhere at all.
	for _, instance := range instances {
		inst := instance
		if inst.Err == nil {
			continue
		}
		key := "instance|" + inst.Name + "|fetch"
		track(key, webhooks.ReasonFetchFailed, func(since time.Time) webhooks.Event {
			return webhooks.Event{
				Event:        webhooks.EventFetchFailed,
				Reason:       webhooks.ReasonFetchFailed,
				Instance:     inst.Name,
				ResourceType: "instance",
				Resource:     inst.Name,
				Detail:       inst.Err.Error(),
				URL:          inst.URL,
				StuckSince:   since,
				Timestamp:    now,
			}
		})
	}

	for _, proj := range projects {
		ps := proj
		// Stale data is carried forward from an earlier poll. Alerting on it
		// would report a dead API token as a fleet-wide outage.
		if ps.Err != nil || ps.IsStale() {
			continue
		}

		for _, resource := range ps.Resources {
			r := resource

			if r.Deploy != nil {
				if reason, ok := deployStuckReason(*r.Deploy); ok {
					key := stuckKey(ps, r.UUID, "deployment")
					dep := *r.Deploy
					track(key, reason, func(since time.Time) webhooks.Event {
						return webhooks.Event{
							Event:        webhooks.EventDeploymentStuck,
							Reason:       reason,
							Instance:     ps.Instance,
							Project:      ps.Name,
							ResourceType: string(r.Kind),
							Resource:     r.Name,
							ResourceUUID: r.UUID,
							Server:       r.ServerName,
							Status:       dep.Status,
							Detail:       deployDetail(dep),
							URL:          dep.URL,
							StuckSince:   since,
							Timestamp:    now,
						}
					})
				}
			}

			if reason, ok := containerStuckReason(r); ok {
				key := stuckKey(ps, r.UUID, "resource")
				track(key, reason, func(since time.Time) webhooks.Event {
					return webhooks.Event{
						Event:        webhooks.EventResourceStuck,
						Reason:       reason,
						Instance:     ps.Instance,
						Project:      ps.Name,
						ResourceType: string(r.Kind),
						Resource:     r.Name,
						ResourceUUID: r.UUID,
						Server:       r.ServerName,
						Status:       r.Status,
						StuckSince:   since,
						Timestamp:    now,
					}
				})
			}
		}
	}

	// Drop resources that recovered or left the config, so they can alert
	// again next time they go bad.
	for key := range p.stuck {
		if _, ok := seen[key]; !ok {
			delete(p.stuck, key)
		}
	}

	return events
}

// deployStuckReason reports whether a deployment is wedged: failed, or still
// queued or building.
func deployStuckReason(d state.DeployState) (string, bool) {
	switch {
	case aggregator.DeployStatus(d.Status) == aggregator.DeployFailed:
		return webhooks.ReasonDeployFailed, true
	case d.InFlight():
		return webhooks.ReasonDeployInProgress, true
	}
	return "", false
}

func deployDetail(d state.DeployState) string {
	if d.Commit == "" {
		return d.Trigger
	}
	return fmt.Sprintf("%s (%s, %s)", d.Commit, d.Trigger, d.Subject)
}

// containerStuckReason reports whether a resource's container is down or
// failing its health check.
func containerStuckReason(r state.ResourceState) (string, bool) {
	if r.ContainerLight != aggregator.StoplightRed {
		return "", false
	}
	containerState, health := aggregator.SplitContainerStatus(r.Status)
	if containerState == "running" && health == "unhealthy" {
		return webhooks.ReasonResourceUnhealth, true
	}
	return webhooks.ReasonResourceDown, true
}
