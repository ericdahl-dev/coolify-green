// Package poller fetches state from every configured Coolify instance on an
// interval and publishes immutable snapshots to the dashboard.
package poller

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ericdahl-dev/coolify-green/internal/config"
	"github.com/ericdahl-dev/coolify-green/internal/coolify"
	"github.com/ericdahl-dev/coolify-green/internal/logx"
	"github.com/ericdahl-dev/coolify-green/internal/state"
	"github.com/ericdahl-dev/coolify-green/internal/webhooks"
)

// Fetcher is the slice of the Coolify API the poller needs. *coolify.Client
// satisfies it; tests supply a fake.
type Fetcher interface {
	Topology(ctx context.Context) (coolify.Topology, error)
	Applications(ctx context.Context) ([]coolify.Resource, error)
	Services(ctx context.Context) ([]coolify.Resource, error)
	Databases(ctx context.Context) ([]coolify.Resource, error)
	ActiveDeployments(ctx context.Context) ([]coolify.Deployment, error)
	LatestDeployment(ctx context.Context, appUUID string) (*coolify.Deployment, error)
	Deployment(ctx context.Context, deploymentUUID string) (coolify.Deployment, error)
	BaseURL() string
}

// ClientFactory builds a Fetcher for one configured instance.
type ClientFactory func(config.Instance) (Fetcher, error)

// unassignedProject collects resources whose environment is not in any project
// the API returned — a project created between topology refreshes, usually.
const unassignedProject = "(unassigned)"

// latestDeployConcurrency bounds the per-application deployment reads. Coolify
// inlines the whole build log in that payload, so these are the expensive
// calls in a cycle.
const latestDeployConcurrency = 4

// stuckEntry records when one resource was first seen in a bad state and
// whether its webhook already fired, so a wedged resource alerts once rather
// than on every poll.
type stuckEntry struct {
	since   time.Time
	reason  string
	alerted bool
}

// deployCacheEntry is the last known deployment for an application. dep is nil
// when the application has never been deployed.
type deployCacheEntry struct {
	dep         *coolify.Deployment
	fetchedAt   time.Time
	wasInFlight bool
}

type instanceCache struct {
	client       Fetcher
	topology     coolify.Topology
	topologyAt   time.Time
	topologyOK   bool
	deployByUUID map[string]*deployCacheEntry
}

// Poller orchestrates periodic fetches across all configured instances.
type Poller struct {
	factory ClientFactory

	mu         sync.Mutex
	cfg        *config.Config
	current    []state.ProjectState
	instances  []state.InstanceState
	inventory  []state.InventoryProject
	caches     map[string]*instanceCache
	dispatcher *webhooks.Dispatcher
	stuck      map[string]*stuckEntry

	// now is swappable in tests so threshold crossings can be exercised
	// without waiting on the wall clock.
	now func() time.Time
}

// New creates a Poller for the given config.
func New(cfg *config.Config, factory ClientFactory) *Poller {
	return &Poller{
		factory:    factory,
		cfg:        cfg,
		caches:     make(map[string]*instanceCache),
		dispatcher: webhooks.New(cfg.Webhooks),
		stuck:      make(map[string]*stuckEntry),
		now:        time.Now,
	}
}

// Inventory returns everything discovery found on the last cycle, including
// resources the user has muted, so the manage screen can offer them back.
func (p *Poller) Inventory() state.Inventory {
	p.mu.Lock()
	defer p.mu.Unlock()
	projects := make([]state.InventoryProject, len(p.inventory))
	copy(projects, p.inventory)
	return state.Inventory{Projects: projects, UpdatedAt: time.Now()}
}

// Snapshot returns an immutable view of the current state.
func (p *Poller) Snapshot() state.Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return state.NewSnapshot(p.current, p.instances)
}

// Start begins polling on the configured interval, sending Snapshots to the
// returned channel. Call the returned cancel func to stop.
func (p *Poller) Start(ctx context.Context) (<-chan state.Snapshot, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	ch := make(chan state.Snapshot, 4)

	go func() {
		defer close(ch)
		p.Poll(ctx, ch)
		ticker := time.NewTicker(p.interval())
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.Poll(ctx, ch)
			}
		}
	}()

	return ch, cancel
}

func (p *Poller) interval() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Duration(p.cfg.Settings.PollInterval) * time.Second
}

// ForceRefresh triggers an immediate poll outside the normal interval.
func (p *Poller) ForceRefresh(ctx context.Context, ch chan<- state.Snapshot) {
	go p.Poll(ctx, ch)
}

// ReloadConfig replaces the config after an edit and re-polls immediately.
// Cached clients are dropped so changed URLs or tokens take effect.
func (p *Poller) ReloadConfig(ctx context.Context, cfg *config.Config, ch chan<- state.Snapshot) {
	p.mu.Lock()
	p.cfg = cfg
	p.dispatcher = webhooks.New(cfg.Webhooks)
	p.caches = make(map[string]*instanceCache)
	p.mu.Unlock()
	go p.Poll(ctx, ch)
}

// InvalidateTopology forces the next poll to rebuild the project map.
func (p *Poller) InvalidateTopology() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.caches {
		c.topologyOK = false
	}
}

// FetchDeploymentLogs reads one deployment in full, for the log drill-down.
// It is deliberately off the polling path: the payload carries the entire
// build log.
func (p *Poller) FetchDeploymentLogs(ctx context.Context, instance, deploymentUUID string) ([]coolify.LogLine, error) {
	p.mu.Lock()
	cache := p.caches[instance]
	p.mu.Unlock()
	if cache == nil || cache.client == nil {
		return nil, fmt.Errorf("no connection to instance %q", instance)
	}
	dep, err := cache.client.Deployment(ctx, deploymentUUID)
	if err != nil {
		return nil, err
	}
	return coolify.VisibleLogs(coolify.ParseLogs(dep.Logs)), nil
}

// Poll runs one full cycle across every enabled instance and publishes a
// snapshot. It is exported so the initial fetch and forced refreshes share one
// path with the ticker.
func (p *Poller) Poll(ctx context.Context, ch chan<- state.Snapshot) {
	p.mu.Lock()
	cfg := p.cfg
	prev := make([]state.ProjectState, len(p.current))
	copy(prev, p.current)
	p.mu.Unlock()

	var (
		projects  []state.ProjectState
		instances []state.InstanceState
		inventory []state.InventoryProject
	)
	for _, inst := range cfg.EnabledInstances() {
		instState, instProjects, instInventory := p.pollInstance(ctx, cfg, inst, prev)
		instances = append(instances, instState)
		projects = append(projects, instProjects...)
		inventory = append(inventory, instInventory...)
	}

	p.mu.Lock()
	p.current = projects
	p.instances = instances
	if len(inventory) > 0 || len(instances) > 0 {
		p.inventory = inventory
	}
	events := p.evaluateStuck(projects)
	p.mu.Unlock()

	// Dispatch outside the lock: a slow webhook endpoint must not stall
	// Snapshot() and freeze the dashboard.
	for _, evt := range events {
		p.dispatcher.Dispatch(evt)
	}

	snap := state.NewSnapshot(projects, instances)
	select {
	case ch <- snap:
	case <-ctx.Done():
	}
}

// cacheFor returns the per-instance cache, creating it on first use.
func (p *Poller) cacheFor(name string) *instanceCache {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.caches[name]
	if !ok {
		c = &instanceCache{deployByUUID: make(map[string]*deployCacheEntry)}
		p.caches[name] = c
	}
	return c
}

// carryForward returns the previous cycle's projects for one instance, marked
// stale, so a transient API failure blanks nothing.
func carryForward(prev []state.ProjectState, instance string, err error, at time.Time) []state.ProjectState {
	var out []state.ProjectState
	for _, ps := range prev {
		if ps.Instance != instance {
			continue
		}
		stale := at
		ps.StaleAt = &stale
		ps.Err = err
		out = append(out, ps)
	}
	return out
}

func (p *Poller) pollInstance(ctx context.Context, cfg *config.Config, inst config.Instance, prev []state.ProjectState) (state.InstanceState, []state.ProjectState, []state.InventoryProject) {
	now := p.now()
	instState := state.InstanceState{Name: inst.Name, URL: inst.URL}
	cache := p.cacheFor(inst.Name)

	if cache.client == nil {
		client, err := p.factory(inst)
		if err != nil {
			logx.Debug("client build failed", "instance", inst.Name, "err", err)
			instState.Err = err
			instState.StaleAt = &now
			return instState, carryForward(prev, inst.Name, err, now), p.lastInventoryFor(inst.Name)
		}
		cache.client = client
	}
	client := cache.client

	// Topology changes only when projects are created or renamed, so it is
	// refreshed on its own slower interval.
	topologyTTL := time.Duration(cfg.Settings.TopologyRefreshSeconds) * time.Second
	if !cache.topologyOK || now.Sub(cache.topologyAt) >= topologyTTL {
		top, err := client.Topology(ctx)
		if err != nil {
			if !cache.topologyOK {
				logx.Debug("topology fetch failed", "instance", inst.Name, "err", err)
				instState.Err = err
				instState.StaleAt = &now
				return instState, carryForward(prev, inst.Name, err, now), p.lastInventoryFor(inst.Name)
			}
			logx.Debug("topology refresh failed, keeping cached map", "instance", inst.Name, "err", err)
		} else {
			cache.topology = top
			cache.topologyAt = now
			cache.topologyOK = true
		}
	}

	resources, active, err := fetchInstanceData(ctx, cfg, client)
	if err != nil {
		logx.Debug("resource fetch failed", "instance", inst.Name, "err", err)
		instState.Err = err
		instState.StaleAt = &now
		return instState, carryForward(prev, inst.Name, err, now), p.lastInventoryFor(inst.Name)
	}

	// Index in-flight deployments by application uuid.
	activeByApp := make(map[string]coolify.Deployment, len(active))
	for _, d := range active {
		if uuid := d.ApplicationUUID(); uuid != "" {
			activeByApp[uuid] = d
		}
	}

	inventory := buildInventory(cfg, inst.Name, cache.topology, resources)

	kept := make([]coolify.Resource, 0, len(resources))
	for _, r := range resources {
		if !cfg.WatchesKind(r.Kind) || !cfg.IsEnabled(r.UUID) {
			continue
		}
		// A project the user muted takes its resources with it.
		if ref, ok := cache.topology.Lookup(r.EnvironmentID); ok && !cfg.IsEnabled(ref.ProjectUUID) {
			continue
		}
		kept = append(kept, r)
	}

	deploys := p.resolveDeployments(ctx, cfg, cache, client, kept, activeByApp, now)

	return instState, buildProjects(inst.Name, client.BaseURL(), cache.topology, kept, deploys), inventory
}

// lastInventoryFor returns the previous cycle's inventory for one instance, so
// a failed poll does not empty the manage screen.
func (p *Poller) lastInventoryFor(instance string) []state.InventoryProject {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []state.InventoryProject
	for _, ip := range p.inventory {
		if ip.Instance == instance {
			out = append(out, ip)
		}
	}
	return out
}

// buildInventory lists every discovered resource of a watched kind, muted or
// not, grouped by project.
func buildInventory(cfg *config.Config, instance string, top coolify.Topology, resources []coolify.Resource) []state.InventoryProject {
	byProject := make(map[string]*state.InventoryProject)
	var order []string

	for _, r := range resources {
		if !cfg.WatchesKind(r.Kind) {
			continue
		}
		ref, ok := top.Lookup(r.EnvironmentID)
		key := ref.ProjectUUID
		if !ok {
			ref = coolify.EnvRef{ProjectUUID: unassignedProject, ProjectName: unassignedProject}
			key = unassignedProject
		}
		ip, seen := byProject[key]
		if !seen {
			ip = &state.InventoryProject{
				Instance: instance,
				UUID:     ref.ProjectUUID,
				Name:     ref.ProjectName,
				Enabled:  cfg.IsEnabled(ref.ProjectUUID),
			}
			byProject[key] = ip
			order = append(order, key)
		}
		ip.Resources = append(ip.Resources, state.InventoryResource{
			Kind:    r.Kind,
			UUID:    r.UUID,
			Name:    r.Name,
			Status:  r.Status,
			Server:  r.ServerName(),
			Enabled: cfg.IsEnabled(r.UUID),
		})
	}

	rank := make(map[string]int, len(top.Order))
	for i, uuid := range top.Order {
		rank[uuid] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ra, aok := rank[order[a]]
		rb, bok := rank[order[b]]
		if aok && bok {
			return ra < rb
		}
		if aok != bok {
			return aok
		}
		return order[a] < order[b]
	})

	out := make([]state.InventoryProject, 0, len(order))
	for _, key := range order {
		ip := byProject[key]
		sort.SliceStable(ip.Resources, func(i, j int) bool {
			if ip.Resources[i].Kind != ip.Resources[j].Kind {
				return kindRank(ip.Resources[i].Kind) < kindRank(ip.Resources[j].Kind)
			}
			return ip.Resources[i].Name < ip.Resources[j].Name
		})
		out = append(out, *ip)
	}
	return out
}

func kindRank(k coolify.Kind) int {
	switch k {
	case coolify.KindApplication:
		return 0
	case coolify.KindService:
		return 1
	default:
		return 2
	}
}

// fetchInstanceData reads every watched resource kind plus the instance-wide
// active deployments, concurrently. Any failure fails the cycle for this
// instance so the dashboard shows stale-but-consistent data rather than a
// half-empty project list.
func fetchInstanceData(ctx context.Context, cfg *config.Config, client Fetcher) ([]coolify.Resource, []coolify.Deployment, error) {
	type result struct {
		resources []coolify.Resource
		err       error
	}

	calls := make([]func(context.Context) ([]coolify.Resource, error), 0, 3)
	if cfg.WatchesKind(coolify.KindApplication) {
		calls = append(calls, client.Applications)
	}
	if cfg.WatchesKind(coolify.KindService) {
		calls = append(calls, client.Services)
	}
	if cfg.WatchesKind(coolify.KindDatabase) {
		calls = append(calls, client.Databases)
	}

	results := make([]result, len(calls))
	var (
		wg         sync.WaitGroup
		active     []coolify.Deployment
		activeErr  error
		wantDeploy = cfg.WatchesKind(coolify.KindApplication)
	)
	for i, call := range calls {
		wg.Add(1)
		go func(i int, call func(context.Context) ([]coolify.Resource, error)) {
			defer wg.Done()
			rs, err := call(ctx)
			results[i] = result{resources: rs, err: err}
		}(i, call)
	}
	if wantDeploy {
		wg.Add(1)
		go func() {
			defer wg.Done()
			active, activeErr = client.ActiveDeployments(ctx)
		}()
	}
	wg.Wait()

	var all []coolify.Resource
	for _, r := range results {
		if r.err != nil {
			return nil, nil, r.err
		}
		all = append(all, r.resources...)
	}
	if activeErr != nil {
		return nil, nil, activeErr
	}
	return all, active, nil
}

// resolveDeployments returns the deployment to display for each application.
// An in-flight deploy is always current. A finished one is re-read only when
// its cache entry expires, or on the first cycle after a deploy completes, so
// the expensive per-application call runs rarely.
func (p *Poller) resolveDeployments(
	ctx context.Context,
	cfg *config.Config,
	cache *instanceCache,
	client Fetcher,
	resources []coolify.Resource,
	activeByApp map[string]coolify.Deployment,
	now time.Time,
) map[string]*coolify.Deployment {
	historyTTL := time.Duration(cfg.Settings.DeployHistorySeconds) * time.Second
	out := make(map[string]*coolify.Deployment)

	var toFetch []string
	for _, r := range resources {
		if r.Kind != coolify.KindApplication {
			continue
		}
		if dep, ok := activeByApp[r.UUID]; ok {
			d := dep
			cache.deployByUUID[r.UUID] = &deployCacheEntry{dep: &d, fetchedAt: now, wasInFlight: true}
			out[r.UUID] = &d
			continue
		}
		entry := cache.deployByUUID[r.UUID]
		switch {
		case entry == nil, entry.wasInFlight, now.Sub(entry.fetchedAt) >= historyTTL:
			toFetch = append(toFetch, r.UUID)
		default:
			out[r.UUID] = entry.dep
		}
	}

	if len(toFetch) > 0 {
		fetched := fetchLatestDeployments(ctx, client, toFetch)
		for uuid, res := range fetched {
			if res.err != nil {
				logx.Debug("latest deployment fetch failed", "app", uuid, "err", res.err)
				// Keep whatever was cached rather than dropping the row's
				// deploy status on a single failed call.
				if entry := cache.deployByUUID[uuid]; entry != nil {
					out[uuid] = entry.dep
				}
				continue
			}
			cache.deployByUUID[uuid] = &deployCacheEntry{dep: res.dep, fetchedAt: now}
			out[uuid] = res.dep
		}
	}

	return out
}

type latestResult struct {
	dep *coolify.Deployment
	err error
}

func fetchLatestDeployments(ctx context.Context, client Fetcher, uuids []string) map[string]latestResult {
	out := make(map[string]latestResult, len(uuids))
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, latestDeployConcurrency)
	)
	for _, uuid := range uuids {
		wg.Add(1)
		go func(uuid string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			dep, err := client.LatestDeployment(ctx, uuid)
			mu.Lock()
			out[uuid] = latestResult{dep: dep, err: err}
			mu.Unlock()
		}(uuid)
	}
	wg.Wait()
	return out
}

// buildProjects groups resources under the project that owns their
// environment. Projects with no watched resources are dropped — an empty row
// carries no signal.
func buildProjects(
	instance, baseURL string,
	top coolify.Topology,
	resources []coolify.Resource,
	deploys map[string]*coolify.Deployment,
) []state.ProjectState {
	byProject := make(map[string]*state.ProjectState)
	var order []string

	for _, r := range resources {
		ref, ok := top.Lookup(r.EnvironmentID)
		key := ref.ProjectUUID
		if !ok {
			ref = coolify.EnvRef{ProjectUUID: unassignedProject, ProjectName: unassignedProject}
			key = unassignedProject
		}
		ps, seen := byProject[key]
		if !seen {
			ps = &state.ProjectState{
				Instance:        instance,
				InstanceURL:     baseURL,
				UUID:            ref.ProjectUUID,
				Name:            ref.ProjectName,
				EnvironmentUUID: ref.EnvironmentUUID,
			}
			byProject[key] = ps
			order = append(order, key)
		}
		rs := state.ResourceStateFromData(r)
		if dep := deploys[r.UUID]; dep != nil {
			rs.Deploy = state.DeployStateFromData(*dep, baseURL)
		}
		ps.Resources = append(ps.Resources, rs)
	}

	// Order projects the way the API listed them; the dashboard re-sorts by
	// stoplight, and a stable base order keeps equal rows from jittering.
	rank := make(map[string]int, len(top.Order))
	for i, uuid := range top.Order {
		rank[uuid] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ra, aok := rank[order[a]]
		rb, bok := rank[order[b]]
		if aok && bok {
			return ra < rb
		}
		if aok != bok {
			return aok // unknown projects (including "(unassigned)") sort last
		}
		return order[a] < order[b]
	})

	out := make([]state.ProjectState, 0, len(order))
	for _, key := range order {
		ps := byProject[key]
		state.SortResources(ps.Resources)
		out = append(out, *ps)
	}
	return out
}
