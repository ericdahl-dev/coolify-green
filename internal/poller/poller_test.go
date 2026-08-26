package poller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ericdahl-dev/coolify-green/internal/config"
	"github.com/ericdahl-dev/coolify-green/internal/coolify"
	"github.com/ericdahl-dev/coolify-green/internal/state"
)

// fakeFetcher is a scriptable stand-in for a Coolify instance.
type fakeFetcher struct {
	mu sync.Mutex

	topology   coolify.Topology
	apps       []coolify.Resource
	services   []coolify.Resource
	databases  []coolify.Resource
	active     []coolify.Deployment
	latest     map[string]*coolify.Deployment
	deployment coolify.Deployment

	topologyErr error
	appsErr     error
	activeErr   error
	latestErr   error

	topologyCalls int
	latestCalls   map[string]int
}

func newFake() *fakeFetcher {
	return &fakeFetcher{
		latest:      map[string]*coolify.Deployment{},
		latestCalls: map[string]int{},
	}
}

func (f *fakeFetcher) Topology(context.Context) (coolify.Topology, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.topologyCalls++
	if f.topologyErr != nil {
		return coolify.Topology{}, f.topologyErr
	}
	return f.topology, nil
}

func (f *fakeFetcher) Applications(context.Context) ([]coolify.Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.apps, f.appsErr
}

func (f *fakeFetcher) Services(context.Context) ([]coolify.Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.services, nil
}

func (f *fakeFetcher) Databases(context.Context) ([]coolify.Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.databases, nil
}

func (f *fakeFetcher) ActiveDeployments(context.Context) ([]coolify.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active, f.activeErr
}

func (f *fakeFetcher) LatestDeployment(_ context.Context, appUUID string) (*coolify.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latestCalls[appUUID]++
	if f.latestErr != nil {
		return nil, f.latestErr
	}
	return f.latest[appUUID], nil
}

func (f *fakeFetcher) Deployment(_ context.Context, _ string) (coolify.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deployment, nil
}

func (f *fakeFetcher) BaseURL() string { return "https://coolify.test" }

func (f *fakeFetcher) calls(appUUID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.latestCalls[appUUID]
}

func testTopology() coolify.Topology {
	return coolify.Topology{
		Projects: map[string]coolify.Project{
			"p1": {UUID: "p1", Name: "Alpha"},
			"p2": {UUID: "p2", Name: "Beta"},
		},
		Order: []string{"p1", "p2"},
		Envs: map[int]coolify.EnvRef{
			1: {ProjectUUID: "p1", ProjectName: "Alpha", EnvironmentUUID: "e1"},
			2: {ProjectUUID: "p2", ProjectName: "Beta", EnvironmentUUID: "e2"},
		},
	}
}

func testConfig(t *testing.T, body string) *config.Config {
	t.Helper()
	if body == "" {
		body = "[[instances]]\nname = \"studio\"\nurl = \"https://coolify.test\"\ntoken = \"x\"\n"
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func newPoller(t *testing.T, cfg *config.Config, f *fakeFetcher) *Poller {
	t.Helper()
	return New(cfg, func(config.Instance) (Fetcher, error) { return f, nil })
}

func pollOnce(t *testing.T, p *Poller) state.Snapshot {
	t.Helper()
	ch := make(chan state.Snapshot, 1)
	p.Poll(context.Background(), ch)
	select {
	case snap := <-ch:
		return snap
	default:
		t.Fatal("poll published no snapshot")
		return state.Snapshot{}
	}
}

func findProject(t *testing.T, snap state.Snapshot, name string) state.ProjectState {
	t.Helper()
	for _, p := range snap.Projects {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("project %q not in snapshot %+v", name, snap.Projects)
	return state.ProjectState{}
}

func TestPollGroupsResourcesByProject(t *testing.T) {
	f := newFake()
	f.topology = testTopology()
	f.apps = []coolify.Resource{
		{Kind: coolify.KindApplication, UUID: "a1", Name: "alpha-web", Status: "running:healthy", EnvironmentID: 1},
		{Kind: coolify.KindApplication, UUID: "a2", Name: "beta-web", Status: "exited:unhealthy", EnvironmentID: 2},
	}
	f.services = []coolify.Resource{
		{Kind: coolify.KindService, UUID: "s1", Name: "n8n", Status: "running:healthy", EnvironmentID: 1},
	}
	f.databases = []coolify.Resource{
		{Kind: coolify.KindDatabase, UUID: "d1", Name: "pg", Status: "running:healthy", EnvironmentID: 1},
	}

	snap := pollOnce(t, newPoller(t, testConfig(t, ""), f))
	if len(snap.Projects) != 2 {
		t.Fatalf("got %d projects: %+v", len(snap.Projects), snap.Projects)
	}
	alpha := findProject(t, snap, "Alpha")
	if len(alpha.Resources) != 3 {
		t.Errorf("Alpha resources = %d", len(alpha.Resources))
	}
	// Sorted: applications, services, databases.
	if alpha.Resources[0].Kind != coolify.KindApplication || alpha.Resources[2].Kind != coolify.KindDatabase {
		t.Errorf("resources not ordered by kind: %+v", alpha.Resources)
	}
	if alpha.EnvironmentUUID != "e1" {
		t.Errorf("environment uuid = %q", alpha.EnvironmentUUID)
	}
	beta := findProject(t, snap, "Beta")
	if beta.Stoplight().String() != "🔴" {
		t.Errorf("Beta stoplight = %v", beta.Stoplight())
	}
}

func TestPollDropsUnwatchedKindsAndDisabledResources(t *testing.T) {
	f := newFake()
	f.topology = testTopology()
	f.apps = []coolify.Resource{
		{Kind: coolify.KindApplication, UUID: "a1", Name: "keep", Status: "running:healthy", EnvironmentID: 1},
		{Kind: coolify.KindApplication, UUID: "a2", Name: "muted", Status: "exited", EnvironmentID: 1},
	}
	f.services = []coolify.Resource{
		{Kind: coolify.KindService, UUID: "s1", Name: "unwatched", Status: "running:healthy", EnvironmentID: 1},
	}
	cfg := testConfig(t, `
[settings]
watch = ["applications"]

[[instances]]
name = "studio"
url = "https://coolify.test"
token = "x"

[[overrides]]
uuid = "a2"
enabled = false
`)
	snap := pollOnce(t, newPoller(t, cfg, f))
	alpha := findProject(t, snap, "Alpha")
	if len(alpha.Resources) != 1 || alpha.Resources[0].Name != "keep" {
		t.Errorf("resources = %+v", alpha.Resources)
	}
}

func TestPollDropsResourcesOfMutedProject(t *testing.T) {
	f := newFake()
	f.topology = testTopology()
	f.apps = []coolify.Resource{
		{Kind: coolify.KindApplication, UUID: "a1", Name: "alpha-web", Status: "running:healthy", EnvironmentID: 1},
		{Kind: coolify.KindApplication, UUID: "a2", Name: "beta-web", Status: "running:healthy", EnvironmentID: 2},
	}
	cfg := testConfig(t, `
[[instances]]
name = "studio"
url = "https://coolify.test"
token = "x"

[[overrides]]
uuid = "p1"
name = "Alpha"
enabled = false
`)
	snap := pollOnce(t, newPoller(t, cfg, f))
	if len(snap.Projects) != 1 || snap.Projects[0].Name != "Beta" {
		t.Errorf("projects = %+v", snap.Projects)
	}
}

func TestPollAttachesActiveDeployment(t *testing.T) {
	f := newFake()
	f.topology = testTopology()
	f.apps = []coolify.Resource{
		{Kind: coolify.KindApplication, UUID: "a1", Name: "alpha-web", Status: "running:healthy", EnvironmentID: 1},
	}
	f.active = []coolify.Deployment{{
		UUID:          "dep-live",
		Status:        "in_progress",
		DeploymentURL: "/project/p1/environment/e1/application/a1/deployment/dep-live",
		CreatedAt:     time.Now(),
	}}

	snap := pollOnce(t, newPoller(t, testConfig(t, ""), f))
	alpha := findProject(t, snap, "Alpha")
	dep := alpha.Resources[0].Deploy
	if dep == nil || dep.UUID != "dep-live" || !dep.InFlight() {
		t.Fatalf("deploy = %+v", dep)
	}
	if alpha.Stoplight().String() != "🟡" {
		t.Errorf("deploying project should be yellow, got %v", alpha.Stoplight())
	}
	// An in-flight deploy is already in hand, so no per-app history call.
	if f.calls("a1") != 0 {
		t.Errorf("latest deployment fetched %d times, want 0", f.calls("a1"))
	}
}

func TestLatestDeploymentIsCachedThenRefetchedAfterCompletion(t *testing.T) {
	f := newFake()
	f.topology = testTopology()
	f.apps = []coolify.Resource{
		{Kind: coolify.KindApplication, UUID: "a1", Name: "alpha-web", Status: "running:healthy", EnvironmentID: 1},
	}
	f.latest["a1"] = &coolify.Deployment{UUID: "dep-old", Status: "finished", CreatedAt: time.Now().Add(-time.Hour)}

	p := newPoller(t, testConfig(t, ""), f)
	pollOnce(t, p)
	if f.calls("a1") != 1 {
		t.Fatalf("first poll made %d history calls, want 1", f.calls("a1"))
	}

	// Second poll inside the TTL must reuse the cache.
	pollOnce(t, p)
	if f.calls("a1") != 1 {
		t.Errorf("cached poll made %d history calls, want 1", f.calls("a1"))
	}

	// A deploy starts, then finishes: the next cycle must re-read the result
	// immediately rather than wait out the history TTL.
	f.mu.Lock()
	f.active = []coolify.Deployment{{
		UUID:          "dep-new",
		Status:        "in_progress",
		DeploymentURL: "/project/p1/environment/e1/application/a1/deployment/dep-new",
		CreatedAt:     time.Now(),
	}}
	f.mu.Unlock()
	pollOnce(t, p)
	if f.calls("a1") != 1 {
		t.Errorf("in-flight poll made %d history calls, want 1", f.calls("a1"))
	}

	f.mu.Lock()
	f.active = nil
	f.latest["a1"] = &coolify.Deployment{UUID: "dep-new", Status: "failed", CreatedAt: time.Now()}
	f.mu.Unlock()
	snap := pollOnce(t, p)
	if f.calls("a1") != 2 {
		t.Errorf("post-deploy poll made %d history calls, want 2", f.calls("a1"))
	}
	alpha := findProject(t, snap, "Alpha")
	if got := alpha.Resources[0].Deploy; got == nil || got.Status != "failed" {
		t.Fatalf("deploy = %+v", got)
	}
	if alpha.Stoplight().String() != "🔴" {
		t.Errorf("failed deploy should turn the project red, got %v", alpha.Stoplight())
	}
}

func TestTopologyIsCachedAcrossPolls(t *testing.T) {
	f := newFake()
	f.topology = testTopology()
	f.apps = []coolify.Resource{{Kind: coolify.KindApplication, UUID: "a1", Name: "x", Status: "running", EnvironmentID: 1}}

	p := newPoller(t, testConfig(t, ""), f)
	pollOnce(t, p)
	pollOnce(t, p)
	if f.topologyCalls != 1 {
		t.Errorf("topology fetched %d times, want 1", f.topologyCalls)
	}

	p.InvalidateTopology()
	pollOnce(t, p)
	if f.topologyCalls != 2 {
		t.Errorf("topology fetched %d times after invalidation, want 2", f.topologyCalls)
	}
}

func TestFetchFailureCarriesForwardStaleProjects(t *testing.T) {
	f := newFake()
	f.topology = testTopology()
	f.apps = []coolify.Resource{{Kind: coolify.KindApplication, UUID: "a1", Name: "alpha-web", Status: "running:healthy", EnvironmentID: 1}}

	p := newPoller(t, testConfig(t, ""), f)
	pollOnce(t, p)

	f.mu.Lock()
	f.appsErr = errors.New("boom")
	f.mu.Unlock()

	snap := pollOnce(t, p)
	if len(snap.Projects) != 1 {
		t.Fatalf("stale projects dropped: %+v", snap.Projects)
	}
	alpha := findProject(t, snap, "Alpha")
	if !alpha.IsStale() || alpha.Err == nil {
		t.Errorf("project not marked stale: %+v", alpha)
	}
	if alpha.Resources[0].Name != "alpha-web" {
		t.Errorf("stale data not carried forward: %+v", alpha.Resources)
	}
	if len(snap.Instances) != 1 || snap.Instances[0].Err == nil {
		t.Errorf("instance error not reported: %+v", snap.Instances)
	}
}

func TestClientFactoryFailureIsReported(t *testing.T) {
	cfg := testConfig(t, "")
	p := New(cfg, func(config.Instance) (Fetcher, error) {
		return nil, fmt.Errorf("token_command failed")
	})
	snap := pollOnce(t, p)
	if len(snap.Instances) != 1 || snap.Instances[0].Err == nil {
		t.Fatalf("instances = %+v", snap.Instances)
	}
	if len(snap.Projects) != 0 {
		t.Errorf("no prior state, so no projects expected: %+v", snap.Projects)
	}
}

func TestUnknownEnvironmentLandsInUnassignedProject(t *testing.T) {
	f := newFake()
	f.topology = testTopology()
	f.apps = []coolify.Resource{{Kind: coolify.KindApplication, UUID: "a9", Name: "orphan", Status: "running", EnvironmentID: 99}}

	snap := pollOnce(t, newPoller(t, testConfig(t, ""), f))
	if len(snap.Projects) != 1 || snap.Projects[0].Name != unassignedProject {
		t.Fatalf("projects = %+v", snap.Projects)
	}
}

func TestSnapshotIsAvailableBeforeFirstPoll(t *testing.T) {
	p := newPoller(t, testConfig(t, ""), newFake())
	snap := p.Snapshot()
	if len(snap.Projects) != 0 {
		t.Errorf("expected empty snapshot, got %+v", snap.Projects)
	}
}

func TestStartPublishesAndStops(t *testing.T) {
	f := newFake()
	f.topology = testTopology()
	f.apps = []coolify.Resource{{Kind: coolify.KindApplication, UUID: "a1", Name: "x", Status: "running", EnvironmentID: 1}}

	p := newPoller(t, testConfig(t, ""), f)
	ch, stop := p.Start(context.Background())
	select {
	case snap := <-ch:
		if len(snap.Projects) != 1 {
			t.Errorf("projects = %+v", snap.Projects)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no snapshot published")
	}
	stop()
	for range ch { // drain until the poller closes the channel
	}
}
