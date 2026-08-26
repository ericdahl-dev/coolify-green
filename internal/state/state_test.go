package state

import (
	"testing"
	"time"

	"github.com/ericdahl-dev/coolify-green/internal/aggregator"
	"github.com/ericdahl-dev/coolify-green/internal/coolify"
)

func TestResourceStoplightCombinesContainerAndDeploy(t *testing.T) {
	r := ResourceState{ContainerLight: aggregator.StoplightGreen}
	if got := r.Stoplight(); got != aggregator.StoplightGreen {
		t.Errorf("no deploy: got %v", got)
	}

	r.Deploy = &DeployState{Stoplight: aggregator.StoplightRed, Status: "failed"}
	if got := r.Stoplight(); got != aggregator.StoplightRed {
		t.Errorf("running container with failed deploy should be red, got %v", got)
	}

	r.Deploy = &DeployState{Stoplight: aggregator.StoplightYellow, Status: "in_progress"}
	if got := r.Stoplight(); got != aggregator.StoplightYellow {
		t.Errorf("deploying should be yellow, got %v", got)
	}
}

func TestProjectStoplightIsWorstResource(t *testing.T) {
	p := ProjectState{Resources: []ResourceState{
		{ContainerLight: aggregator.StoplightGreen},
		{ContainerLight: aggregator.StoplightRed},
		{ContainerLight: aggregator.StoplightGreen},
	}}
	if got := p.Stoplight(); got != aggregator.StoplightRed {
		t.Errorf("got %v", got)
	}
	if got := (ProjectState{}).Stoplight(); got != aggregator.StoplightGrey {
		t.Errorf("empty project should be grey, got %v", got)
	}
}

func TestKindSummary(t *testing.T) {
	p := ProjectState{Resources: []ResourceState{
		{Kind: coolify.KindApplication, ContainerLight: aggregator.StoplightGreen},
		{Kind: coolify.KindApplication, ContainerLight: aggregator.StoplightRed},
		{Kind: coolify.KindService, ContainerLight: aggregator.StoplightGreen},
	}}
	light, count := p.KindSummary(coolify.KindApplication)
	if light != aggregator.StoplightRed || count != 2 {
		t.Errorf("apps: %v %d", light, count)
	}
	light, count = p.KindSummary(coolify.KindDatabase)
	if light != aggregator.StoplightGrey || count != 0 {
		t.Errorf("databases: %v %d", light, count)
	}
}

func TestKeyIsInstanceQualified(t *testing.T) {
	a := ProjectState{Instance: "studio", UUID: "p1"}
	b := ProjectState{Instance: "backup", UUID: "p1"}
	if a.Key() == b.Key() {
		t.Error("same project uuid on two instances must not share a key")
	}
	if got := (ProjectState{UUID: "p1"}).Key(); got != "p1" {
		t.Errorf("unqualified key = %q", got)
	}
}

func TestSortedProjectsPutsDeployingFirst(t *testing.T) {
	projects := []ProjectState{
		{Name: "green", Resources: []ResourceState{{ContainerLight: aggregator.StoplightGreen}}},
		{Name: "grey"},
		{Name: "red", Resources: []ResourceState{{ContainerLight: aggregator.StoplightRed}}},
		{Name: "yellow", Resources: []ResourceState{{ContainerLight: aggregator.StoplightYellow}}},
	}
	order := SortedProjects(projects)
	want := []string{"yellow", "red", "green", "grey"}
	for i, idx := range order {
		if projects[idx].Name != want[i] {
			t.Fatalf("position %d = %q, want %q", i, projects[idx].Name, want[i])
		}
	}
}

func TestSortResourcesByKindThenName(t *testing.T) {
	rs := []ResourceState{
		{Kind: coolify.KindDatabase, Name: "pg"},
		{Kind: coolify.KindApplication, Name: "zeta"},
		{Kind: coolify.KindService, Name: "n8n"},
		{Kind: coolify.KindApplication, Name: "alpha"},
	}
	SortResources(rs)
	want := []string{"alpha", "zeta", "n8n", "pg"}
	for i, r := range rs {
		if r.Name != want[i] {
			t.Fatalf("position %d = %q, want %q", i, r.Name, want[i])
		}
	}
}

func TestDeployElapsed(t *testing.T) {
	start := time.Now().Add(-2 * time.Minute)
	end := start.Add(90 * time.Second)
	d := DeployState{StartedAt: start, FinishedAt: &end}
	if got := d.Elapsed(); got != 90*time.Second {
		t.Errorf("finished elapsed = %v", got)
	}

	running := DeployState{StartedAt: start}
	if got := running.Elapsed(); got < 2*time.Minute {
		t.Errorf("running elapsed = %v, want >= 2m", got)
	}

	if got := (DeployState{}).Elapsed(); got != 0 {
		t.Errorf("zero start elapsed = %v", got)
	}
}

func TestDeployStateFromDataClosesTimerOnFinished(t *testing.T) {
	created := time.Now().Add(-5 * time.Minute)
	updated := created.Add(time.Minute)
	d := coolify.Deployment{
		UUID:          "dep1",
		Status:        "finished",
		Commit:        "abcdef1234567",
		CommitMessage: "subject\n\nbody",
		DeploymentURL: "/project/p/environment/e/application/a/deployment/dep1",
		CreatedAt:     created,
		UpdatedAt:     updated,
	}
	ds := DeployStateFromData(d, "https://coolify.example.com")
	if ds.Stoplight != aggregator.StoplightGreen {
		t.Errorf("stoplight = %v", ds.Stoplight)
	}
	if ds.FinishedAt == nil || !ds.FinishedAt.Equal(updated) {
		t.Errorf("finished_at not backfilled: %+v", ds.FinishedAt)
	}
	if ds.URL != "https://coolify.example.com/project/p/environment/e/application/a/deployment/dep1" {
		t.Errorf("url = %q", ds.URL)
	}
	if ds.Subject != "subject" || ds.Commit != "abcdef1" {
		t.Errorf("commit display = %q / %q", ds.Commit, ds.Subject)
	}
}

func TestDeployStateFromDataLeavesRunningTimerOpen(t *testing.T) {
	d := coolify.Deployment{Status: "in_progress", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	ds := DeployStateFromData(d, "https://x")
	if ds.FinishedAt != nil {
		t.Error("in-flight deploy must not have a finish time")
	}
	if !ds.InFlight() {
		t.Error("in_progress should report InFlight")
	}
}

func TestActiveDeploy(t *testing.T) {
	p := ProjectState{Resources: []ResourceState{
		{Deploy: &DeployState{Status: "finished"}},
		{Deploy: &DeployState{Status: "in_progress", UUID: "live"}},
	}}
	got := p.ActiveDeploy()
	if got == nil || got.UUID != "live" {
		t.Errorf("ActiveDeploy = %+v", got)
	}
	if (ProjectState{}).ActiveDeploy() != nil {
		t.Error("empty project should have no active deploy")
	}
}

func TestNewSnapshotCopies(t *testing.T) {
	projects := []ProjectState{{Name: "one"}}
	snap := NewSnapshot(projects, []InstanceState{{Name: "studio"}})
	projects[0].Name = "mutated"
	if snap.Projects[0].Name != "one" {
		t.Error("snapshot must not alias the caller's slice")
	}
	if snap.UpdatedAt.IsZero() {
		t.Error("UpdatedAt not set")
	}
}
