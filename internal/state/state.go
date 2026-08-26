// Package state holds the immutable display state the dashboard renders.
package state

import (
	"sort"
	"time"

	"github.com/ericdahl-dev/coolify-green/internal/aggregator"
	"github.com/ericdahl-dev/coolify-green/internal/coolify"
)

// DeployState is the display state of an application's most recent deployment.
type DeployState struct {
	UUID       string
	Status     string
	Stoplight  aggregator.Stoplight
	Commit     string
	Subject    string
	Trigger    string
	StartedAt  time.Time
	FinishedAt *time.Time
	URL        string

	// Logs are fetched on demand when a resource row is expanded, never on
	// the polling path — Coolify inlines the whole build log in the
	// deployment payload and it runs to tens of kilobytes.
	Logs        []coolify.LogLine
	LogsFetched bool
	LogsErr     error
}

// InFlight reports whether the deployment is still queued or running.
func (d DeployState) InFlight() bool {
	return aggregator.DeployStatus(d.Status).InFlight()
}

// Elapsed returns how long the deployment ran, or has been running.
func (d DeployState) Elapsed() time.Duration {
	if d.StartedAt.IsZero() {
		return 0
	}
	if d.FinishedAt != nil {
		return d.FinishedAt.Sub(d.StartedAt)
	}
	return time.Since(d.StartedAt)
}

// DeployStateFromData converts an API deployment into display state.
func DeployStateFromData(d coolify.Deployment, baseURL string) *DeployState {
	ds := &DeployState{
		UUID:       d.UUID,
		Status:     d.Status,
		Stoplight:  aggregator.DeployStoplight(d.Status),
		Commit:     d.ShortCommit(),
		Subject:    d.CommitSubject(),
		Trigger:    d.Trigger(),
		StartedAt:  d.CreatedAt,
		FinishedAt: d.FinishedAt,
	}
	if d.DeploymentURL != "" {
		ds.URL = baseURL + d.DeploymentURL
	}
	// A finished deploy with no finished_at still has a last-updated stamp,
	// which is close enough to end the timer rather than let it run forever.
	if ds.FinishedAt == nil && !ds.InFlight() && !d.UpdatedAt.IsZero() {
		t := d.UpdatedAt
		ds.FinishedAt = &t
	}
	return ds
}

// ResourceState is the display state of one application, service, or database.
type ResourceState struct {
	Kind           coolify.Kind
	UUID           string
	Name           string
	Status         string
	FQDN           string
	ServerName     string
	GitRepository  string
	GitBranch      string
	ContainerLight aggregator.Stoplight
	Deploy         *DeployState
}

// ResourceStateFromData converts an API resource into display state.
func ResourceStateFromData(r coolify.Resource) ResourceState {
	return ResourceState{
		Kind:           r.Kind,
		UUID:           r.UUID,
		Name:           r.Name,
		Status:         r.Status,
		FQDN:           r.FQDN,
		ServerName:     r.ServerName(),
		GitRepository:  r.GitRepository,
		GitBranch:      r.GitBranch,
		ContainerLight: aggregator.ContainerStoplight(r.Status),
	}
}

// Stoplight combines the container's state with its most recent deployment, so
// a running app whose last deploy failed still reads red.
func (r ResourceState) Stoplight() aggregator.Stoplight {
	if r.Deploy == nil {
		return r.ContainerLight
	}
	return aggregator.Aggregate(r.ContainerLight, r.Deploy.Stoplight)
}

// ProjectState is the display state of one Coolify project — the dashboard's
// top-level row.
type ProjectState struct {
	Instance        string
	InstanceURL     string
	UUID            string
	Name            string
	EnvironmentUUID string
	Resources       []ResourceState

	// StaleAt and Err are set when the instance fetch failed and the
	// displayed data is carried forward from the last good poll.
	StaleAt *time.Time
	Err     error
}

// Key is the stable identity of a project row. Project names repeat across
// instances, so anything that tells two rows apart — expansion state, cursor
// resolution, stuck bookkeeping — must key on this, not on Name.
func (p ProjectState) Key() string {
	if p.Instance == "" {
		return p.UUID
	}
	return p.Instance + "/" + p.UUID
}

// FullName renders the row label, qualified by instance when more than one is
// configured.
func (p ProjectState) FullName(qualify bool) string {
	if qualify && p.Instance != "" {
		return p.Instance + " / " + p.Name
	}
	return p.Name
}

// IsStale reports whether the project's data is carried forward from an
// earlier poll because the latest fetch failed.
func (p ProjectState) IsStale() bool { return p.StaleAt != nil }

// Stoplight returns the worst-case stoplight across the project's resources.
func (p ProjectState) Stoplight() aggregator.Stoplight {
	lights := make([]aggregator.Stoplight, 0, len(p.Resources))
	for _, r := range p.Resources {
		lights = append(lights, r.Stoplight())
	}
	return aggregator.Aggregate(lights...)
}

// KindSummary returns the worst stoplight and count for one resource kind.
func (p ProjectState) KindSummary(kind coolify.Kind) (aggregator.Stoplight, int) {
	worst := aggregator.StoplightGrey
	count := 0
	for _, r := range p.Resources {
		if r.Kind != kind {
			continue
		}
		count++
		if l := r.Stoplight(); l > worst {
			worst = l
		}
	}
	return worst, count
}

// ActiveDeploy returns the first in-flight deployment in the project, if any.
func (p ProjectState) ActiveDeploy() *DeployState {
	for _, r := range p.Resources {
		if r.Deploy != nil && r.Deploy.InFlight() {
			return r.Deploy
		}
	}
	return nil
}

// InstanceState records the health of the connection to one Coolify instance.
type InstanceState struct {
	Name    string
	URL     string
	Err     error
	StaleAt *time.Time
}

// Snapshot is an immutable view of every project at a point in time.
type Snapshot struct {
	Projects  []ProjectState
	Instances []InstanceState
	UpdatedAt time.Time
}

// NewSnapshot copies the given projects into a fresh Snapshot.
func NewSnapshot(projects []ProjectState, instances []InstanceState) Snapshot {
	p := make([]ProjectState, len(projects))
	copy(p, projects)
	i := make([]InstanceState, len(instances))
	copy(i, instances)
	return Snapshot{Projects: p, Instances: i, UpdatedAt: time.Now()}
}

// SortedProjects returns project indices ordered so the most actionable rows
// come first: deploying, then broken, then healthy, then unknown. Order is
// stable within a tier so rows do not jitter between polls.
func SortedProjects(projects []ProjectState) []int {
	order := make([]int, len(projects))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		pa := aggregator.SortPriority(projects[order[a]].Stoplight())
		pb := aggregator.SortPriority(projects[order[b]].Stoplight())
		return pa < pb
	})
	return order
}

// SortResources orders a project's resources by kind (applications, services,
// databases) and then by name, so an expanded row reads the same every poll.
func SortResources(resources []ResourceState) {
	rank := map[coolify.Kind]int{
		coolify.KindApplication: 0,
		coolify.KindService:     1,
		coolify.KindDatabase:    2,
	}
	sort.SliceStable(resources, func(i, j int) bool {
		ri, rj := rank[resources[i].Kind], rank[resources[j].Kind]
		if ri != rj {
			return ri < rj
		}
		return resources[i].Name < resources[j].Name
	})
}

// InventoryResource is one discovered resource, whether or not it is currently
// monitored.
type InventoryResource struct {
	Kind    coolify.Kind
	UUID    string
	Name    string
	Status  string
	Server  string
	Enabled bool
}

// InventoryProject groups discovered resources under their project.
type InventoryProject struct {
	Instance  string
	UUID      string
	Name      string
	Enabled   bool
	Resources []InventoryResource
}

// Inventory is everything discovery found, including resources the user has
// muted. The dashboard shows only what is monitored; the manage screen needs
// the full list so a muted resource can be found and switched back on.
type Inventory struct {
	Projects  []InventoryProject
	UpdatedAt time.Time
}
