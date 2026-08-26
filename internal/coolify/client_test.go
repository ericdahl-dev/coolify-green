package coolify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "https://coolify.example.com/", want: "https://coolify.example.com"},
		{in: "coolify.example.com", want: "https://coolify.example.com"},
		{in: "http://localhost:8000", want: "http://localhost:8000"},
		{in: "https://host/sub/", want: "https://host/sub"},
		{in: "", wantErr: true},
		{in: "ftp://host", wantErr: true},
	}
	for _, tc := range tests {
		got, err := NormalizeBaseURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeBaseURL(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeBaseURL(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestApplicationsSetsKindAndServer(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Path != "/api/v1/applications" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"uuid":"abc","name":"app","status":"running:healthy",
			"environment_id":23,"destination":{"server":{"uuid":"s1","name":"ger3"}}}]`))
	})

	apps, err := c.Applications(context.Background())
	if err != nil {
		t.Fatalf("Applications: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("got %d apps", len(apps))
	}
	if apps[0].Kind != KindApplication {
		t.Errorf("kind = %q", apps[0].Kind)
	}
	if apps[0].ServerName() != "ger3" {
		t.Errorf("server = %q", apps[0].ServerName())
	}
	if apps[0].EnvironmentID != 23 {
		t.Errorf("environment = %d", apps[0].EnvironmentID)
	}
}

func TestServiceServerFallsBackToTopLevel(t *testing.T) {
	r := Resource{}
	r.Server = Server{Name: "hetzner"}
	if r.ServerName() != "hetzner" {
		t.Errorf("ServerName = %q", r.ServerName())
	}
}

func TestLatestDeploymentEmptyIsNil(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("take") != "1" {
			t.Errorf("take = %q", r.URL.Query().Get("take"))
		}
		_, _ = w.Write([]byte(`{"count":0,"deployments":[]}`))
	})
	d, err := c.LatestDeployment(context.Background(), "abc")
	if err != nil {
		t.Fatalf("LatestDeployment: %v", err)
	}
	if d != nil {
		t.Errorf("want nil deployment, got %+v", d)
	}
}

func TestLatestDeploymentDecodes(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"count":165,"deployments":[{"deployment_uuid":"dep1",
			"status":"in_progress","commit":"cdecd756a9f5a2de654f5737a6ffec0a0d207d52",
			"commit_message":"subject line\n\nbody","is_webhook":true,
			"created_at":"2026-08-26T19:18:13.000000Z"}]}`))
	})
	d, err := c.LatestDeployment(context.Background(), "abc")
	if err != nil {
		t.Fatalf("LatestDeployment: %v", err)
	}
	if d.UUID != "dep1" || d.Status != "in_progress" {
		t.Fatalf("got %+v", d)
	}
	if d.ShortCommit() != "cdecd75" {
		t.Errorf("ShortCommit = %q", d.ShortCommit())
	}
	if d.CommitSubject() != "subject line" {
		t.Errorf("CommitSubject = %q", d.CommitSubject())
	}
	if d.Trigger() != "webhook" {
		t.Errorf("Trigger = %q", d.Trigger())
	}
	if d.CreatedAt.IsZero() {
		t.Error("CreatedAt not parsed")
	}
}

func TestUnauthorized(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := c.Applications(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestRateLimitCarriesRetryAfter(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := c.Applications(context.Background())
	if !errors.Is(err, ErrRateLimit) {
		t.Fatalf("err = %v, want ErrRateLimit", err)
	}
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter.Seconds() != 12 {
		t.Errorf("retry-after not parsed: %v", err)
	}
}

func TestDeploySendsForce(t *testing.T) {
	var gotMethod, gotPath, gotForce, gotUUID string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotForce = r.URL.Query().Get("force")
		gotUUID = r.URL.Query().Get("uuid")
		_, _ = w.Write([]byte(`{"deployments":[]}`))
	})
	if err := c.Deploy(context.Background(), "app1", true); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/deploy" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	if gotForce != "true" || gotUUID != "app1" {
		t.Errorf("force=%q uuid=%q", gotForce, gotUUID)
	}
}

func TestLifecyclePaths(t *testing.T) {
	tests := []struct {
		kind Kind
		want string
	}{
		{KindApplication, "/api/v1/applications/x/restart"},
		{KindService, "/api/v1/services/x/restart"},
		{KindDatabase, "/api/v1/databases/x/restart"},
	}
	for _, tc := range tests {
		var got string
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Path
			_, _ = w.Write([]byte(`{}`))
		})
		if err := c.Restart(context.Background(), tc.kind, "x"); err != nil {
			t.Fatalf("Restart(%s): %v", tc.kind, err)
		}
		if got != tc.want {
			t.Errorf("path = %q, want %q", got, tc.want)
		}
	}
}

func TestResourceURL(t *testing.T) {
	c, err := New("https://coolify.example.com", "t")
	if err != nil {
		t.Fatal(err)
	}
	got := c.ResourceURL(KindApplication, "proj", "env", "app")
	want := "https://coolify.example.com/project/proj/environment/env/application/app"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := c.ResourceURL(KindDatabase, "proj", "env", "db"); !contains(got, "/database/db") {
		t.Errorf("database url = %q", got)
	}
	if got := c.ResourceURL(KindApplication, "", "", "app"); got != "https://coolify.example.com/dashboard" {
		t.Errorf("fallback url = %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestTopologyMapsEnvironmentsToProjects(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/projects":
			_, _ = w.Write([]byte(`[{"uuid":"p1","name":"Alpha"},{"uuid":"p2","name":"Beta"}]`))
		case "/api/v1/projects/p1":
			_, _ = w.Write([]byte(`{"uuid":"p1","name":"Alpha","environments":[{"id":1,"uuid":"e1","name":"production"}]}`))
		case "/api/v1/projects/p2":
			_, _ = w.Write([]byte(`{"uuid":"p2","name":"Beta","environments":[{"id":2,"uuid":"e2","name":"production"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	top, err := c.Topology(context.Background())
	if err != nil {
		t.Fatalf("Topology: %v", err)
	}
	ref, ok := top.Lookup(2)
	if !ok || ref.ProjectName != "Beta" || ref.EnvironmentUUID != "e2" {
		t.Errorf("Lookup(2) = %+v ok=%v", ref, ok)
	}
	if len(top.Order) != 2 || top.Order[0] != "p1" {
		t.Errorf("Order = %v", top.Order)
	}
}

func TestParseLogsAndVisible(t *testing.T) {
	raw := `[{"output":"one","type":"stdout","hidden":false},{"output":"two","type":"stderr","hidden":true}]`
	lines := ParseLogs(raw)
	if len(lines) != 2 {
		t.Fatalf("got %d lines", len(lines))
	}
	vis := VisibleLogs(lines)
	if len(vis) != 1 || vis[0].Output != "one" {
		t.Errorf("VisibleLogs = %+v", vis)
	}
	if !lines[1].IsError() {
		t.Error("stderr line should report IsError")
	}
	if ParseLogs("") != nil || ParseLogs("not json") != nil {
		t.Error("malformed logs should yield no lines")
	}
}

func TestParseLooseTime(t *testing.T) {
	if _, ok := ParseLooseTime("2026-08-26 19:20:47"); !ok {
		t.Error("space-separated timestamp should parse")
	}
	if _, ok := ParseLooseTime("2026-08-26T19:18:13.000000Z"); !ok {
		t.Error("RFC3339 timestamp should parse")
	}
	if _, ok := ParseLooseTime(""); ok {
		t.Error("empty timestamp should not parse")
	}
}

func TestApplicationUUIDFromDeploymentURL(t *testing.T) {
	d := Deployment{DeploymentURL: "/project/rksw/environment/gduq/application/g1451pdxe7zdld5hsqgmeqja/deployment/wuxz"}
	if got := d.ApplicationUUID(); got != "g1451pdxe7zdld5hsqgmeqja" {
		t.Errorf("ApplicationUUID = %q", got)
	}
	if got := (Deployment{}).ApplicationUUID(); got != "" {
		t.Errorf("missing url should yield empty uuid, got %q", got)
	}
	if got := (Deployment{DeploymentURL: "/nope"}).ApplicationUUID(); got != "" {
		t.Errorf("unexpected url shape should yield empty uuid, got %q", got)
	}
}
