# coolify-green

A terminal dashboard that shows live Coolify deploy and resource health across every project on an
instance, updating automatically via polling.

## Language

**Instance**: A Coolify installation being monitored, identified by a base URL and an API token.
_Avoid_: server (a Server is a machine *inside* an Instance), host

**Project**: A Coolify project — the dashboard's top-level row. Discovered from the API, never
hand-listed. Contains an Environment, which contains Resources.
_Avoid_: app, stack, group

**Environment**: A named environment inside a Project (usually `production`). Resources reference it
by integer id; only the per-project detail endpoint reveals which Project owns it.

**Resource**: An Application, Service, or Database. The three come from three endpoints with
overlapping shapes and are modeled as one type carrying a Kind.
_Avoid_: container, workload

**Deployment**: One deploy of an Application. Has a Status (`queued`, `in_progress`, `finished`,
`failed`, `cancelled-by-user`) and inlines its whole build log. <!-- spelling: ok (Coolify API status value) -->
_Avoid_: build, release, run

**Container status**: Coolify's `"<state>:<health>"` string on a Resource, e.g. `running:healthy`,
`running:unknown`, `exited:unhealthy`. Services and databases often report a bare state with no
health half.

**Topology**: The environment-id → Project map. Rebuilt on its own slow interval because it changes
only when Projects are created or renamed.

**Inventory**: Everything discovery found, including Resources the user has muted. The dashboard
shows only what is monitored; the manage screen needs the full list so a muted Resource can be
found and switched back on.

**Override**: A config entry muting one Project or Resource by uuid. Discovery is opt-out, so the
config records only the exceptions.

**Config file**: The user-managed file at `~/.config/coolify-green/config.toml`. It lists Instances
and Overrides — never the Resources to watch.

## Relationships

- An **Instance** has many **Projects**; a **Project** has one or more **Environments**
- An **Environment** has many **Resources**
- An **Application** has many **Deployments**; the dashboard shows only the latest
- A **Resource** belongs to exactly one **Server** inside the Instance

## Stoplight

The visual health indicator. A Resource combines its container status with its latest Deployment,
so a running app whose last deploy failed still reads red.

| Color | Meaning | Sources |
|---|---|---|
| 🟢 Green | Healthy | `running:healthy`, `running:unknown`, deployment `finished` |
| 🔴 Red | Broken | `running:unhealthy`, `exited`, `stopped`, `degraded`, deployment `failed` |
| 🟡 Yellow | In flight | deployment `queued` / `in_progress`, `restarting`, `running:starting` |
| ⚪ Gray | No signal | empty or unrecognized status, deployment `cancelled-by-user` <!-- spelling: ok (Coolify API status value) --> |

`running:unknown` is green, not a warning: it is what Coolify reports for every resource with no
health check configured, which is most of them.

_Avoid_: badge, indicator, light

## Active-first sorting

Projects sort by Stoplight priority so the most actionable rows are at the top: 🟡 deploying →
🔴 broken → 🟢 healthy → ⚪ unknown. Order is stable within a tier so rows do not jitter between polls.
_Avoid_: bubbling, floating

## Auto-expansion

A project row opens by itself when its Stoplight needs attention (🔴 or 🟡) and closes when it returns
to 🟢/⚪. This is **edge-triggered**: expansion changes only when a project's Stoplight changes, or the
first time the project is seen. Level-triggering would re-open a row on every poll, fighting a user
who collapsed it deliberately, so a hand-collapsed row stays collapsed until something actually
happens to it.

Only the Project row auto-expands; Resource rows are always expanded by hand, since expanding one
fetches its deploy log. Because expansion inserts rows, the cursor is resolved back to the same
logical row after each snapshot rather than kept at the same index.
_Avoid_: auto-open, smart expand

## Dashboard tree

```
▶ 🔴  clawproxy                Apps 🔴 1
▼ 🟡  Brandywine Coins         Apps 🟡 1  Services 🟢 1  DBs 🟢 1   ↻ in_progress 2m14s
      applications
        ▼ 🟡  brandywinecoins    ✓ running  ↻ deploying cdecd75 2m14s
              https://bwcoins.ericdahl.dev
              ericdahl-dev/brandywinecoins@main
              The tabs lead the page; the modifiers follow their tab
              Preparing container with helper image…
      services
          🟢  n8n                ✓ healthy
```

- **Project row**: expand/collapse with `enter`/`space`. Rows that need attention (🔴 🟡) expand on
  their own and collapse again on recovery; see **Auto-expansion**.
- **Resource row**: expand to see the fqdn, repo, and the tail of the latest Deployment's log.
- **Log lines**: not navigable; fetched on expand, never during a poll.

## Polling

Each cycle makes a fixed number of calls per Instance regardless of how many resources exist:

- 1 × `GET /applications`, `GET /services`, `GET /databases` (only for watched kinds)
- 1 × `GET /deployments` — every queued or running deploy on the instance, in one call
- 0..n × `GET /deployments/applications/{uuid}?take=1` — the *last* deploy of an application, only
  when its cache entry has expired or a deploy just finished. This payload inlines the whole build
  log, which is why it is rationed rather than polled.
- 1 × `GET /projects` plus one `GET /projects/{uuid}` per project, on the Topology interval only

On failure the last known state is retained and marked stale.
_Avoid_: refresh, sync, watch

## Keybindings

| Key | Action |
|---|---|
| `↑` / `k` | Navigate up |
| `↓` / `j` | Navigate down |
| `enter` / `space` | Expand/collapse a Project row, or a Resource's deploy log |
| `f` | Smart fix (redeploy / start / restart / cancel) |
| `o` | Open the selected row in Coolify |
| `r` | Force refresh (also rebuilds Topology) |
| `m` | Manage what is monitored |
| `q` | Quit |
| `?` | Toggle help overlay |

## Flagged ambiguities

- "exited" vs "intentionally stopped" — open: Coolify exposes no desired-state field, so a resource
  you stopped on purpose is indistinguishable from one that crashed. Both read red; muting it in the
  manage screen is the escape hatch.
- Whether a *failed* Deployment should stop counting as red once a later container start succeeds —
  open. Today a failed last deploy stays red until the next deploy.
