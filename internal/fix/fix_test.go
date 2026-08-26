package fix

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ericdahl-dev/coolify-green/internal/aggregator"
	"github.com/ericdahl-dev/coolify-green/internal/coolify"
	"github.com/ericdahl-dev/coolify-green/internal/state"
)

func project(resources ...state.ResourceState) state.ProjectState {
	return state.ProjectState{Instance: "studio", UUID: "p1", Name: "Alpha", Resources: resources}
}

func app(name, status string, deploy *state.DeployState) state.ResourceState {
	return state.ResourceState{
		Kind: coolify.KindApplication, UUID: "uuid-" + name, Name: name,
		Status: status, ContainerLight: aggregator.ContainerStoplight(status), Deploy: deploy,
	}
}

func TestPlanNilWhenHealthy(t *testing.T) {
	p := project(app("web", "running:healthy", &state.DeployState{Status: "finished"}))
	if plan := PlanFor(p, 0); plan != nil {
		t.Errorf("healthy project got a plan: %+v", plan)
	}
	if plan := PlanFor(state.ProjectState{}, 0); plan != nil {
		t.Errorf("empty project got a plan: %+v", plan)
	}
}

func TestRunningDeployBlocksAnyFix(t *testing.T) {
	p := project(
		app("web", "running:healthy", &state.DeployState{
			Status: "in_progress", StartedAt: time.Now().Add(-2 * time.Minute), UUID: "dep1",
		}),
		app("worker", "exited", nil),
	)
	if plan := PlanFor(p, 0); plan != nil {
		t.Errorf("a deploy in flight should suppress fixes, got %+v", plan)
	}
}

func TestStalledDeployOffersCancel(t *testing.T) {
	p := project(app("web", "running:healthy", &state.DeployState{
		Status: "in_progress", UUID: "dep1", StartedAt: time.Now().Add(-90 * time.Minute),
	}))
	plan := PlanFor(p, 0)
	if plan == nil || plan.Kind != KindCancelDeploy {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.DeploymentUUID != "dep1" {
		t.Errorf("deployment uuid = %q", plan.DeploymentUUID)
	}
	if !strings.Contains(plan.Description, "1h30m00s") {
		t.Errorf("description should carry the elapsed time: %q", plan.Description)
	}
}

func TestFailedDeployOffersRedeploy(t *testing.T) {
	p := project(app("web", "running:healthy", &state.DeployState{
		Status: "failed", Commit: "abc1234", UUID: "dep1",
	}))
	plan := PlanFor(p, 0)
	if plan == nil || plan.Kind != KindRedeploy {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.ResourceUUID != "uuid-web" || plan.Instance != "studio" {
		t.Errorf("plan target = %+v", plan)
	}
	if !strings.Contains(plan.Description, "abc1234") {
		t.Errorf("description should name the commit: %q", plan.Description)
	}
}

func TestFailedDeployOutranksStoppedContainer(t *testing.T) {
	p := project(
		state.ResourceState{Kind: coolify.KindService, UUID: "s1", Name: "n8n", Status: "exited"},
		app("web", "running:healthy", &state.DeployState{Status: "failed", Commit: "abc1234"}),
	)
	plan := PlanFor(p, 0)
	if plan == nil || plan.Kind != KindRedeploy {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestStoppedResourceOffersStart(t *testing.T) {
	p := project(state.ResourceState{
		Kind: coolify.KindDatabase, UUID: "d1", Name: "postgresql", Status: "exited:unhealthy",
	})
	plan := PlanFor(p, 0)
	if plan == nil || plan.Kind != KindStartResource {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.ResourceKind != coolify.KindDatabase {
		t.Errorf("resource kind = %q", plan.ResourceKind)
	}
	if !strings.Contains(plan.Description, "database postgresql") {
		t.Errorf("description = %q", plan.Description)
	}
}

func TestUnhealthyResourceOffersRestart(t *testing.T) {
	p := project(state.ResourceState{
		Kind: coolify.KindService, UUID: "s1", Name: "dozzle", Status: "running:unhealthy",
	})
	plan := PlanFor(p, 0)
	if plan == nil || plan.Kind != KindRestartResource {
		t.Fatalf("plan = %+v", plan)
	}
	if !strings.Contains(plan.Description, "health check") {
		t.Errorf("description = %q", plan.Description)
	}
}

// recordingActioner captures which API call Execute made.
type recordingActioner struct {
	deployUUID  string
	deployForce bool
	cancelUUID  string
	startKind   coolify.Kind
	startUUID   string
	restartUUID string
	err         error
}

func (r *recordingActioner) Deploy(_ context.Context, uuid string, force bool) error {
	r.deployUUID, r.deployForce = uuid, force
	return r.err
}

func (r *recordingActioner) CancelDeployment(_ context.Context, uuid string) error {
	r.cancelUUID = uuid
	return r.err
}

func (r *recordingActioner) Start(_ context.Context, kind coolify.Kind, uuid string) error {
	r.startKind, r.startUUID = kind, uuid
	return r.err
}

func (r *recordingActioner) Restart(_ context.Context, _ coolify.Kind, uuid string) error {
	r.restartUUID = uuid
	return r.err
}

func TestExecuteRoutesEachKind(t *testing.T) {
	ctx := context.Background()

	a := &recordingActioner{}
	if err := Execute(ctx, &Plan{Kind: KindRedeploy, ResourceUUID: "app1"}, a); err != nil {
		t.Fatal(err)
	}
	if a.deployUUID != "app1" || !a.deployForce {
		t.Errorf("redeploy: %+v", a)
	}

	a = &recordingActioner{}
	if err := Execute(ctx, &Plan{Kind: KindCancelDeploy, DeploymentUUID: "dep1"}, a); err != nil {
		t.Fatal(err)
	}
	if a.cancelUUID != "dep1" {
		t.Errorf("cancel: %+v", a)
	}

	a = &recordingActioner{}
	if err := Execute(ctx, &Plan{Kind: KindStartResource, ResourceKind: coolify.KindService, ResourceUUID: "s1"}, a); err != nil {
		t.Fatal(err)
	}
	if a.startUUID != "s1" || a.startKind != coolify.KindService {
		t.Errorf("start: %+v", a)
	}

	a = &recordingActioner{}
	if err := Execute(ctx, &Plan{Kind: KindRestartResource, ResourceUUID: "r1"}, a); err != nil {
		t.Fatal(err)
	}
	if a.restartUUID != "r1" {
		t.Errorf("restart: %+v", a)
	}
}

func TestExecuteSurfacesErrors(t *testing.T) {
	a := &recordingActioner{err: errors.New("403")}
	if err := Execute(context.Background(), &Plan{Kind: KindRedeploy}, a); err == nil {
		t.Error("want error from the actioner")
	}
	if err := Execute(context.Background(), nil, a); err == nil {
		t.Error("want error for a nil plan")
	}
	if err := Execute(context.Background(), &Plan{Kind: Kind(99)}, a); err == nil {
		t.Error("want error for an unknown kind")
	}
}

func TestKindStrings(t *testing.T) {
	for _, k := range []Kind{KindRedeploy, KindCancelDeploy, KindStartResource, KindRestartResource} {
		if k.String() == "unknown" {
			t.Errorf("kind %d has no label", k)
		}
	}
}
