package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/ericdahl-dev/coolify-green/internal/aggregator"
	"github.com/ericdahl-dev/coolify-green/internal/coolify"
	"github.com/ericdahl-dev/coolify-green/internal/state"
)

// project builds a one-application project whose container is at the given
// stoplight, so there is a resource row to expand into.
func project(uuid, name string, light aggregator.Stoplight) state.ProjectState {
	status := map[aggregator.Stoplight]string{
		aggregator.StoplightGreen:  "running:healthy",
		aggregator.StoplightYellow: "starting:unknown",
		aggregator.StoplightRed:    "exited:unhealthy",
		aggregator.StoplightGrey:   "",
	}[light]
	return state.ProjectState{
		Instance: "studio", InstanceURL: "https://coolify.test",
		UUID: uuid, Name: name, EnvironmentUUID: "e-" + uuid,
		Resources: []state.ResourceState{{
			Kind: coolify.KindApplication, UUID: "r-" + uuid, Name: name + "-web",
			Status: status, ContainerLight: light,
		}},
	}
}

func snapOf(projects ...state.ProjectState) state.Snapshot {
	return state.NewSnapshot(projects, []state.InstanceState{{Name: "studio", URL: "https://coolify.test"}})
}

func showsResource(d Dashboard, name string) bool {
	return strings.Contains(d.BodyView(), name+"-web")
}

func TestAutoExpandOnFirstSighting(t *testing.T) {
	d := NewDashboard(snapOf(
		project("p1", "red", aggregator.StoplightRed),
		project("p2", "yellow", aggregator.StoplightYellow),
		project("p3", "green", aggregator.StoplightGreen),
	), nil, nil, context.Background())

	if !showsResource(d, "red") || !showsResource(d, "yellow") {
		t.Errorf("red and yellow projects should open on first sighting:\n%s", d.BodyView())
	}
	if showsResource(d, "green") {
		t.Errorf("a green project should stay collapsed:\n%s", d.BodyView())
	}
}

func TestAutoExpandOnStatusChangeAndCollapseOnRecovery(t *testing.T) {
	d := NewDashboard(snapOf(project("p1", "alpha", aggregator.StoplightGreen)), nil, nil, context.Background())
	if showsResource(d, "alpha") {
		t.Fatal("green project should start collapsed")
	}
	d, _ = d.Update(snapOf(project("p1", "alpha", aggregator.StoplightYellow)))
	if !showsResource(d, "alpha") {
		t.Errorf("going yellow should open the row:\n%s", d.BodyView())
	}
	d, _ = d.Update(snapOf(project("p1", "alpha", aggregator.StoplightGreen)))
	if showsResource(d, "alpha") {
		t.Errorf("recovering to green should close the row:\n%s", d.BodyView())
	}
}

// Polling must never fight the user: a hand-collapsed red row stays collapsed
// while its status is unchanged...
func TestManualCollapseSurvivesIdenticalSnapshots(t *testing.T) {
	red := snapOf(project("p1", "alpha", aggregator.StoplightRed))
	d := NewDashboard(red, nil, nil, context.Background())
	d, _ = d.Update(key("enter")) // collapse by hand
	if showsResource(d, "alpha") {
		t.Fatal("enter should collapse the row")
	}
	d, _ = d.Update(red)
	d, _ = d.Update(red)
	if showsResource(d, "alpha") {
		t.Errorf("an unchanged snapshot re-opened a hand-collapsed row:\n%s", d.BodyView())
	}
}

// ...but a genuine status change is a new event, so it opens again.
func TestStatusChangeOverridesManualCollapse(t *testing.T) {
	d := NewDashboard(snapOf(project("p1", "alpha", aggregator.StoplightYellow)), nil, nil, context.Background())
	d, _ = d.Update(key("enter"))
	d, _ = d.Update(snapOf(project("p1", "alpha", aggregator.StoplightRed)))
	if !showsResource(d, "alpha") {
		t.Errorf("yellow turning red should open the row again:\n%s", d.BodyView())
	}
}

// Auto-expansion inserts rows above the cursor, so the selection has to land
// on the same logical row it was on before.
func TestCursorStaysOnSameRowWhenAProjectAboveExpands(t *testing.T) {
	d := NewDashboard(snapOf(
		project("p1", "alpha", aggregator.StoplightGreen),
		project("p2", "beta", aggregator.StoplightGreen),
	), nil, nil, context.Background())
	d, _ = d.Update(key("down"))
	if r := d.SelectedURL(); !strings.HasSuffix(r, "/project/p2/environment/e-p2") {
		t.Fatalf("setup: selected %q, want beta", r)
	}

	// alpha goes red: it sorts first and opens, inserting a row above beta.
	d, _ = d.Update(snapOf(
		project("p1", "alpha", aggregator.StoplightRed),
		project("p2", "beta", aggregator.StoplightGreen),
	))
	if r := d.SelectedURL(); !strings.HasSuffix(r, "/project/p2/environment/e-p2") {
		t.Errorf("cursor moved off beta to %q", r)
	}
}

// A project that disappears and comes back is a first sighting again, not a
// status carried over from before.
func TestRemovedProjectIsForgotten(t *testing.T) {
	red := snapOf(project("p1", "alpha", aggregator.StoplightRed))
	d := NewDashboard(red, nil, nil, context.Background())
	d, _ = d.Update(key("enter")) // collapse by hand
	d, _ = d.Update(snapOf())
	d, _ = d.Update(red)
	if !showsResource(d, "alpha") {
		t.Errorf("a returning red project should open on sighting:\n%s", d.BodyView())
	}
}
