package coolify

import (
	"context"
	"sync"
)

// EnvRef locates a resource's environment and the project that owns it.
type EnvRef struct {
	ProjectUUID     string
	ProjectName     string
	EnvironmentUUID string
	EnvironmentName string
}

// Topology maps environment ids to their project. Resources reference their
// environment by integer id only, and no list endpoint returns the project, so
// this has to be assembled from the per-project detail calls. It changes only
// when projects are created or renamed, so the poller caches it.
type Topology struct {
	Projects map[string]Project // keyed by project uuid, in API order
	Order    []string           // project uuids in the order the API returned
	Envs     map[int]EnvRef     // environment id -> owning project
}

// Lookup returns the project reference for an environment id.
func (t Topology) Lookup(environmentID int) (EnvRef, bool) {
	ref, ok := t.Envs[environmentID]
	return ref, ok
}

// topologyConcurrency bounds the per-project detail calls so refreshing
// topology on a large instance does not burst the API.
const topologyConcurrency = 4

// Topology fetches every project and its environments.
func (c *Client) Topology(ctx context.Context) (Topology, error) {
	projects, err := c.Projects(ctx)
	if err != nil {
		return Topology{}, err
	}

	top := Topology{
		Projects: make(map[string]Project, len(projects)),
		Order:    make([]string, 0, len(projects)),
		Envs:     make(map[int]EnvRef),
	}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, topologyConcurrency)
	)
	for _, p := range projects {
		top.Order = append(top.Order, p.UUID)
		wg.Add(1)
		go func(p Project) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			full, err := c.Project(ctx, p.UUID)
			if err != nil {
				// A project that fails to expand still belongs in the map;
				// its resources simply stay unassigned this cycle.
				full = p
			}
			mu.Lock()
			defer mu.Unlock()
			top.Projects[p.UUID] = full
			for _, env := range full.Environments {
				top.Envs[env.ID] = EnvRef{
					ProjectUUID:     full.UUID,
					ProjectName:     full.Name,
					EnvironmentUUID: env.UUID,
					EnvironmentName: env.Name,
				}
			}
		}(p)
	}
	wg.Wait()

	return top, nil
}
