package ui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericdahl-dev/coolify-green/internal/aggregator"
	"github.com/ericdahl-dev/coolify-green/internal/coolify"
	"github.com/ericdahl-dev/coolify-green/internal/fix"
	"github.com/ericdahl-dev/coolify-green/internal/state"
)

func key(s string) tea.KeyMsg {
	if s == " " {
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	if s == "enter" {
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func testSnapshot() state.Snapshot {
	return state.NewSnapshot([]state.ProjectState{
		{
			Instance: "studio", InstanceURL: "https://coolify.test",
			UUID: "p1", Name: "Alpha", EnvironmentUUID: "e1",
			Resources: []state.ResourceState{
				{
					Kind: coolify.KindApplication, UUID: "a1", Name: "alpha-web",
					Status: "running:healthy", ContainerLight: aggregator.StoplightGreen,
					FQDN: "https://alpha.example.com",
					Deploy: &state.DeployState{
						UUID: "dep1", Status: "finished", Commit: "abc1234",
						Stoplight: aggregator.StoplightGreen, Subject: "ship it",
						StartedAt: time.Now().Add(-5 * time.Minute),
					},
				},
				{
					Kind: coolify.KindService, UUID: "s1", Name: "n8n",
					Status: "running:healthy", ContainerLight: aggregator.StoplightGreen,
				},
			},
		},
		{
			Instance: "studio", InstanceURL: "https://coolify.test",
			UUID: "p2", Name: "Beta", EnvironmentUUID: "e2",
			Resources: []state.ResourceState{{
				Kind: coolify.KindApplication, UUID: "b1", Name: "beta-web",
				Status: "exited:unhealthy", ContainerLight: aggregator.StoplightRed,
			}},
		},
	}, []state.InstanceState{{Name: "studio", URL: "https://coolify.test"}})
}

func newTestDashboard() Dashboard {
	return NewDashboard(testSnapshot(), nil, nil, context.Background())
}

func TestBodyViewListsProjectsWorstFirst(t *testing.T) {
	out := newTestDashboard().BodyView()
	beta := strings.Index(out, "Beta")
	alpha := strings.Index(out, "Alpha")
	if beta < 0 || alpha < 0 {
		t.Fatalf("both projects should render:\n%s", out)
	}
	if beta > alpha {
		t.Errorf("the broken project should sort first:\n%s", out)
	}
	if !strings.Contains(out, "Apps") {
		t.Errorf("per-kind summary missing:\n%s", out)
	}
}

func TestCollapsedProjectsHideResources(t *testing.T) {
	out := newTestDashboard().BodyView()
	if strings.Contains(out, "alpha-web") {
		t.Errorf("collapsed project leaked a resource row:\n%s", out)
	}
}

func TestExpandProjectShowsResources(t *testing.T) {
	d := newTestDashboard()
	d.cursor = 2 // Alpha: Beta sorts first and is open, being red
	d, _ = d.Update(key("enter"))

	out := d.BodyView()
	if !strings.Contains(out, "alpha-web") || !strings.Contains(out, "n8n") {
		t.Errorf("expanded project should list its resources:\n%s", out)
	}
	if !strings.Contains(out, "applications") || !strings.Contains(out, "services") {
		t.Errorf("resources should be grouped by kind:\n%s", out)
	}
}

func TestNavigationStaysInBounds(t *testing.T) {
	d := newTestDashboard()
	for i := 0; i < 10; i++ {
		d, _ = d.Update(key("k"))
	}
	if d.cursor != 0 {
		t.Errorf("cursor went above the top: %d", d.cursor)
	}
	for i := 0; i < 10; i++ {
		d, _ = d.Update(key("j"))
	}
	if want := len(d.buildNavList()) - 1; d.cursor != want {
		t.Errorf("cursor = %d, want %d", d.cursor, want)
	}
}

func TestCollapsingClampsTheCursor(t *testing.T) {
	d := newTestDashboard()
	d.cursor = 1
	d, _ = d.Update(key("enter")) // expand Alpha
	d.cursor = len(d.buildNavList()) - 1
	d.cursor = 1
	d, _ = d.Update(key("enter")) // collapse again
	if d.cursor >= len(d.buildNavList()) {
		t.Errorf("cursor %d out of range for %d rows", d.cursor, len(d.buildNavList()))
	}
}

func TestSelectedResourceAndURL(t *testing.T) {
	d := newTestDashboard()
	d.cursor = 2 // Alpha, below the open Beta
	d, _ = d.Update(key("enter"))
	d.cursor = 3 // alpha-web

	r := d.SelectedResource()
	if r == nil || r.Name != "alpha-web" {
		t.Fatalf("SelectedResource = %+v", r)
	}
	want := "https://coolify.test/project/p1/environment/e1/application/a1"
	if got := d.SelectedURL(); got != want {
		t.Errorf("SelectedURL = %q, want %q", got, want)
	}
}

func TestSelectedURLForProjectRow(t *testing.T) {
	d := newTestDashboard()
	d.cursor = 0 // Beta project row
	want := "https://coolify.test/project/p2/environment/e2"
	if got := d.SelectedURL(); got != want {
		t.Errorf("SelectedURL = %q, want %q", got, want)
	}
}

func TestExpandingAResourceFetchesItsLog(t *testing.T) {
	var gotInstance, gotDeployment string
	fetcher := func(_ context.Context, instance, deploymentUUID string) ([]coolify.LogLine, error) {
		gotInstance, gotDeployment = instance, deploymentUUID
		return []coolify.LogLine{{Output: "Build finished", Type: "stdout"}}, nil
	}
	d := NewDashboard(testSnapshot(), nil, fetcher, context.Background())
	d.cursor = 2
	d, _ = d.Update(key("enter")) // expand Alpha
	d.cursor = 3                  // alpha-web
	d, cmd := d.Update(key("enter"))

	if cmd == nil {
		t.Fatal("expanding a resource with a deploy should return a fetch command")
	}
	if !strings.Contains(d.BodyView(), "fetching deploy log") {
		t.Errorf("expected a loading line:\n%s", d.BodyView())
	}

	msg := drainForLogs(t, cmd)
	if gotInstance != "studio" || gotDeployment != "dep1" {
		t.Errorf("fetched %q/%q", gotInstance, gotDeployment)
	}
	d, _ = d.Update(msg)
	out := d.BodyView()
	if !strings.Contains(out, "Build finished") {
		t.Errorf("log line not rendered:\n%s", out)
	}
	if !strings.Contains(out, "https://alpha.example.com") {
		t.Errorf("resource detail should show the fqdn:\n%s", out)
	}
}

// drainForLogs runs a command (possibly a batch) and returns its
// logsFetchedMsg. Batched siblings include long tick commands, so each is run
// concurrently and only the log result is waited on.
func drainForLogs(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	msg := cmd()
	if _, ok := msg.(logsFetchedMsg); ok {
		return msg
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("unexpected msg %T", msg)
	}
	found := make(chan tea.Msg, len(batch))
	for _, sub := range batch {
		if sub == nil {
			continue
		}
		go func(sub tea.Cmd) {
			if inner, ok := sub().(logsFetchedMsg); ok {
				found <- inner
			}
		}(sub)
	}
	select {
	case msg := <-found:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("no logsFetchedMsg in batch")
		return nil
	}
}

func TestLogFetchErrorIsShown(t *testing.T) {
	d := newTestDashboard()
	d.resExpanded["studio/p1/a1"] = true
	d, _ = d.Update(logsFetchedMsg{deploymentUUID: "dep1", err: errors.New("403 forbidden")})
	d.cursor = 2
	d, _ = d.Update(key("enter"))
	if !strings.Contains(d.BodyView(), "log unavailable: 403 forbidden") {
		t.Errorf("log error not rendered:\n%s", d.BodyView())
	}
}

func TestFixConfirmFlow(t *testing.T) {
	var executed bool
	factory := func(instance string) (fix.Actioner, error) {
		return &stubActioner{onStart: func() { executed = true }}, nil
	}
	d := NewDashboard(testSnapshot(), factory, nil, context.Background())
	d.cursor = 0 // Beta, whose app is exited

	d, _ = d.Update(key("f"))
	if d.fixStatus != fixConfirming || d.fixPlan == nil {
		t.Fatalf("fix not armed: status=%v plan=%+v", d.fixStatus, d.fixPlan)
	}
	if d.fixPlan.Kind != fix.KindStartResource {
		t.Errorf("plan kind = %v", d.fixPlan.Kind)
	}
	if !strings.Contains(d.BodyView(), "[enter] confirm") {
		t.Errorf("confirm prompt missing:\n%s", d.BodyView())
	}

	// esc cancels without acting.
	cancelled, _ := d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cancelled.fixStatus != fixIdle || executed {
		t.Errorf("esc should cancel the fix (status=%v executed=%v)", cancelled.fixStatus, executed)
	}

	// enter runs it.
	d, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if d.fixStatus != fixExecuting || cmd == nil {
		t.Fatalf("enter should execute: status=%v cmd=%v", d.fixStatus, cmd)
	}
	msg := cmd()
	done, ok := msg.(fixDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("fix result = %+v", msg)
	}
	if !executed {
		t.Error("the actioner was never called")
	}

	d, _ = d.Update(done)
	if d.fixStatus != fixShowResult || d.fixErr {
		t.Errorf("result state = %v err=%v", d.fixStatus, d.fixErr)
	}
	if !strings.Contains(d.BodyView(), "✓ start") {
		t.Errorf("success message missing:\n%s", d.BodyView())
	}
}

func TestFixIsNotOfferedForHealthyProject(t *testing.T) {
	d := newTestDashboard()
	d.cursor = 2 // Alpha, healthy
	d, _ = d.Update(key("f"))
	if d.fixStatus != fixIdle {
		t.Errorf("healthy project armed a fix: %+v", d.fixPlan)
	}
}

func TestFixFailureIsReported(t *testing.T) {
	factory := func(string) (fix.Actioner, error) { return nil, errors.New("token expired") }
	d := NewDashboard(testSnapshot(), factory, nil, context.Background())
	d.cursor = 0
	d, _ = d.Update(key("f"))
	d, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	done, ok := cmd().(fixDoneMsg)
	if !ok || done.err == nil {
		t.Fatalf("expected a failure message, got %+v", done)
	}
	d, _ = d.Update(done)
	if !d.fixErr || !strings.Contains(d.BodyView(), "token expired") {
		t.Errorf("failure not surfaced:\n%s", d.BodyView())
	}
}

func TestInstanceErrorBanner(t *testing.T) {
	snap := testSnapshot()
	snap.Instances[0].Err = coolify.ErrUnauthorized
	d := NewDashboard(snap, nil, nil, context.Background())
	out := d.BodyView()
	if !strings.Contains(out, "unreachable") {
		t.Errorf("instance error banner missing:\n%s", out)
	}
	if !strings.Contains(out, "API token") {
		t.Errorf("auth errors should hint at the token:\n%s", out)
	}
}

func TestStaleProjectIsMarked(t *testing.T) {
	snap := testSnapshot()
	staleAt := time.Now().Add(-90 * time.Second)
	snap.Projects[0].StaleAt = &staleAt
	d := NewDashboard(snap, nil, nil, context.Background())
	if !strings.Contains(d.BodyView(), "last seen") {
		t.Errorf("stale marker missing:\n%s", d.BodyView())
	}
}

func TestActiveDeployBadge(t *testing.T) {
	snap := testSnapshot()
	snap.Projects[0].Resources[0].Deploy = &state.DeployState{
		UUID: "dep2", Status: "in_progress", Stoplight: aggregator.StoplightYellow,
		StartedAt: time.Now().Add(-134 * time.Second),
	}
	d := NewDashboard(snap, nil, nil, context.Background())
	out := d.BodyView()
	if !strings.Contains(out, "in_progress") || !strings.Contains(out, "2m14s") {
		t.Errorf("active deploy badge missing or unformatted:\n%s", out)
	}
}

func TestEmptySnapshotRenders(t *testing.T) {
	d := NewDashboard(state.NewSnapshot(nil, nil), nil, nil, context.Background())
	if !strings.Contains(d.BodyView(), "No projects discovered") {
		t.Errorf("empty state missing:\n%s", d.BodyView())
	}
	if d.SelectedProject() != nil || d.SelectedURL() != "" {
		t.Error("empty dashboard should have no selection")
	}
}

func TestSnapshotUpdateKeepsCursorInRange(t *testing.T) {
	d := newTestDashboard()
	d.cursor = 1
	d, _ = d.Update(state.NewSnapshot(testSnapshot().Projects[:1], nil))
	if d.cursor >= len(d.buildNavList()) {
		t.Errorf("cursor %d out of range after a smaller snapshot", d.cursor)
	}
}

func TestInFlightDeployLogCacheIsDropped(t *testing.T) {
	d := newTestDashboard()
	d.resExpanded["studio/p1/a1"] = true
	d.logs["dep1"] = logEntry{lines: []coolify.LogLine{{Output: "old"}}}

	snap := testSnapshot()
	snap.Projects[0].Resources[0].Deploy.Status = "in_progress"
	d, _ = d.Update(snap)
	if _, ok := d.logs["dep1"]; ok {
		t.Error("a running deploy's cached log should be dropped so it refreshes")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{5 * time.Second, "5s"},
		{134 * time.Second, "2m14s"},
		{3*time.Hour + 4*time.Minute + 5*time.Second, "3h04m05s"},
		{-time.Second, "0s"},
	}
	for _, tc := range tests {
		if got := formatDuration(tc.in); got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abcdef", 4); got != "abc…" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("truncate = %q", got)
	}
}

func TestContainerStatusLabels(t *testing.T) {
	tests := []struct{ status, want string }{
		{"running:healthy", "✓ healthy"},
		{"running:unknown", "✓ running"},
		{"running:unhealthy", "✗ unhealthy"},
		{"exited:unhealthy", "✗ exited"},
		{"restarting", "↻ restarting"},
		{"", "— no status"},
	}
	for _, tc := range tests {
		if got, _ := containerStatusLabel(tc.status); got != tc.want {
			t.Errorf("containerStatusLabel(%q) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestDeployStatusLabels(t *testing.T) {
	if got, _ := deployStatusLabel("failed"); got != "✗ deploy failed" {
		t.Errorf("got %q", got)
	}
	if got, _ := deployStatusLabel(""); got != "" {
		t.Errorf("empty status should render nothing, got %q", got)
	}
}

// stubActioner records that a fix ran.
type stubActioner struct{ onStart func() }

func (s *stubActioner) Deploy(context.Context, string, bool) error     { return nil }
func (s *stubActioner) CancelDeployment(context.Context, string) error { return nil }
func (s *stubActioner) Start(context.Context, coolify.Kind, string) error {
	if s.onStart != nil {
		s.onStart()
	}
	return nil
}
func (s *stubActioner) Restart(context.Context, coolify.Kind, string) error { return nil }

func TestDashboardScrollsToKeepTheCursorVisible(t *testing.T) {
	// One project per row, more than fit on a short terminal.
	var projects []state.ProjectState
	for i := 0; i < 40; i++ {
		projects = append(projects, state.ProjectState{
			Instance: "studio", UUID: "p" + strconv.Itoa(i), Name: "project-" + strconv.Itoa(i),
			Resources: []state.ResourceState{{
				Kind: coolify.KindApplication, UUID: "a" + strconv.Itoa(i),
				Name: "app", Status: "running:healthy", ContainerLight: aggregator.StoplightGreen,
			}},
		})
	}
	d := NewDashboard(state.NewSnapshot(projects, nil), nil, nil, context.Background())
	d, _ = d.Update(tea.WindowSizeMsg{Width: 100, Height: 15})

	out := d.BodyView()
	if lines := strings.Count(out, "\n"); lines > 15 {
		t.Errorf("body used %d lines on a 15-line terminal:\n%s", lines, out)
	}

	// Walk to the bottom; the selected row must still be rendered.
	for i := 0; i < 39; i++ {
		d, _ = d.Update(key("j"))
	}
	out = d.BodyView()
	if !strings.Contains(out, "project-39") {
		t.Errorf("cursor row scrolled out of view:\n%s", out)
	}
	if !strings.Contains(out, "↑ ") {
		t.Errorf("expected an overflow marker for the hidden rows:\n%s", out)
	}
}
