package poller

import (
	"testing"
	"time"

	"github.com/ericdahl-dev/coolify-green/internal/aggregator"
	"github.com/ericdahl-dev/coolify-green/internal/state"
	"github.com/ericdahl-dev/coolify-green/internal/webhooks"
)

func stuckPoller(t *testing.T) (*Poller, *time.Time) {
	t.Helper()
	cfg := testConfig(t, `
[settings]
stuck_threshold_minutes = 30

[[instances]]
name = "studio"
url = "https://coolify.test"
token = "x"
`)
	p := New(cfg, nil)
	clock := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return clock }
	return p, &clock
}

func failedDeployProject() []state.ProjectState {
	return []state.ProjectState{{
		Instance: "studio",
		UUID:     "p1",
		Name:     "Alpha",
		Resources: []state.ResourceState{{
			Kind:           "application",
			UUID:           "a1",
			Name:           "alpha-web",
			Status:         "running:healthy",
			ServerName:     "ger3",
			ContainerLight: aggregator.StoplightGreen,
			Deploy: &state.DeployState{
				UUID: "dep1", Status: "failed", Commit: "abc1234",
				Subject: "broke the build", Trigger: "webhook",
				Stoplight: aggregator.StoplightRed,
			},
		}},
	}}
}

func TestStuckFiresOnceAfterThreshold(t *testing.T) {
	p, clock := stuckPoller(t)
	projects := failedDeployProject()

	if events := p.evaluateStuck(projects); len(events) != 0 {
		t.Fatalf("alerted before the threshold: %+v", events)
	}

	*clock = clock.Add(29 * time.Minute)
	if events := p.evaluateStuck(projects); len(events) != 0 {
		t.Fatalf("alerted one minute early: %+v", events)
	}

	*clock = clock.Add(2 * time.Minute)
	events := p.evaluateStuck(projects)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	evt := events[0]
	if evt.Event != webhooks.EventDeploymentStuck || evt.Reason != webhooks.ReasonDeployFailed {
		t.Errorf("event = %+v", evt)
	}
	if evt.Instance != "studio" || evt.Project != "Alpha" || evt.Resource != "alpha-web" {
		t.Errorf("event identity = %+v", evt)
	}
	if evt.ResourceUUID != "a1" || evt.Server != "ger3" {
		t.Errorf("event detail = %+v", evt)
	}
	if !evt.StuckSince.Before(evt.Timestamp) {
		t.Errorf("stuck_since %v should precede timestamp %v", evt.StuckSince, evt.Timestamp)
	}

	*clock = clock.Add(time.Hour)
	if events := p.evaluateStuck(projects); len(events) != 0 {
		t.Fatalf("re-alerted while still stuck: %+v", events)
	}
}

func TestRecoveryRearmsTheAlert(t *testing.T) {
	p, clock := stuckPoller(t)
	projects := failedDeployProject()

	p.evaluateStuck(projects) // first sighting starts the clock
	*clock = clock.Add(31 * time.Minute)
	if len(p.evaluateStuck(projects)) != 1 {
		t.Fatal("first alert did not fire")
	}

	healthy := failedDeployProject()
	healthy[0].Resources[0].Deploy.Status = "finished"
	healthy[0].Resources[0].Deploy.Stoplight = aggregator.StoplightGreen
	*clock = clock.Add(time.Minute)
	if events := p.evaluateStuck(healthy); len(events) != 0 {
		t.Fatalf("recovery should not alert: %+v", events)
	}

	*clock = clock.Add(time.Minute)
	if events := p.evaluateStuck(projects); len(events) != 0 {
		t.Fatalf("clock should restart on the new incident: %+v", events)
	}
	*clock = clock.Add(31 * time.Minute)
	if events := p.evaluateStuck(projects); len(events) != 1 {
		t.Fatalf("a fresh incident must alert again, got %d", len(events))
	}
}

func TestChangedReasonRestartsTheClock(t *testing.T) {
	p, clock := stuckPoller(t)
	building := failedDeployProject()
	building[0].Resources[0].Deploy.Status = "in_progress"
	building[0].Resources[0].Deploy.Stoplight = aggregator.StoplightYellow

	p.evaluateStuck(building) // first sighting starts the clock
	*clock = clock.Add(31 * time.Minute)
	events := p.evaluateStuck(building)
	if len(events) != 1 || events[0].Reason != webhooks.ReasonDeployInProgress {
		t.Fatalf("events = %+v", events)
	}

	// The same deploy now fails: a different condition, so it gets its own
	// threshold rather than firing instantly off the old timer.
	failed := failedDeployProject()
	*clock = clock.Add(time.Minute)
	if events := p.evaluateStuck(failed); len(events) != 0 {
		t.Fatalf("new reason should restart the clock: %+v", events)
	}
	*clock = clock.Add(31 * time.Minute)
	if events := p.evaluateStuck(failed); len(events) != 1 {
		t.Fatalf("want one event after the new threshold, got %d", len(events))
	}
}

func TestStaleProjectsDoNotAlert(t *testing.T) {
	p, clock := stuckPoller(t)
	projects := failedDeployProject()
	staleAt := *clock
	projects[0].StaleAt = &staleAt
	projects[0].Err = errContext

	*clock = clock.Add(2 * time.Hour)
	if events := p.evaluateStuck(projects); len(events) != 0 {
		t.Fatalf("stale carried-forward data must not alert: %+v", events)
	}
}

var errContext = &staticErr{"unauthorized: check the Coolify API token"}

type staticErr struct{ msg string }

func (e *staticErr) Error() string { return e.msg }

func TestDownContainerAlerts(t *testing.T) {
	p, clock := stuckPoller(t)
	projects := []state.ProjectState{{
		Instance: "studio", UUID: "p1", Name: "Alpha",
		Resources: []state.ResourceState{{
			Kind: "service", UUID: "s1", Name: "listmonk",
			Status: "exited", ContainerLight: aggregator.StoplightRed,
		}},
	}}
	p.evaluateStuck(projects) // first sighting starts the clock
	*clock = clock.Add(31 * time.Minute)
	events := p.evaluateStuck(projects)
	if len(events) != 1 {
		t.Fatalf("got %d events", len(events))
	}
	if events[0].Event != webhooks.EventResourceStuck || events[0].Reason != webhooks.ReasonResourceDown {
		t.Errorf("event = %+v", events[0])
	}
	if events[0].ResourceType != "service" {
		t.Errorf("resource type = %q", events[0].ResourceType)
	}
}

func TestUnhealthyContainerHasItsOwnReason(t *testing.T) {
	p, clock := stuckPoller(t)
	projects := []state.ProjectState{{
		Instance: "studio", UUID: "p1", Name: "Alpha",
		Resources: []state.ResourceState{{
			Kind: "application", UUID: "a1", Name: "web",
			Status: "running:unhealthy", ContainerLight: aggregator.StoplightRed,
		}},
	}}
	p.evaluateStuck(projects) // first sighting starts the clock
	*clock = clock.Add(31 * time.Minute)
	events := p.evaluateStuck(projects)
	if len(events) != 1 || events[0].Reason != webhooks.ReasonResourceUnhealth {
		t.Fatalf("events = %+v", events)
	}
}

func TestHealthyStateNeverAlerts(t *testing.T) {
	p, clock := stuckPoller(t)
	projects := []state.ProjectState{{
		Instance: "studio", UUID: "p1", Name: "Alpha",
		Resources: []state.ResourceState{{
			Kind: "application", UUID: "a1", Name: "web",
			Status: "running:healthy", ContainerLight: aggregator.StoplightGreen,
			Deploy: &state.DeployState{Status: "finished", Stoplight: aggregator.StoplightGreen},
		}},
	}}
	*clock = clock.Add(24 * time.Hour)
	if events := p.evaluateStuck(projects); len(events) != 0 {
		t.Fatalf("healthy state alerted: %+v", events)
	}
}

func TestStuckKeysAreInstanceQualified(t *testing.T) {
	a := state.ProjectState{Instance: "studio", UUID: "p1"}
	b := state.ProjectState{Instance: "backup", UUID: "p1"}
	if stuckKey(a, "r1", "resource") == stuckKey(b, "r1", "resource") {
		t.Error("same resource on two instances must not share a stuck key")
	}
}
