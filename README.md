# coolify-green

### get your projects green

A terminal dashboard for live [Coolify](https://coolify.io) deploy and resource health — every
project, application, service, and database on your instance, without a browser.

```
  🔴  clawproxy                Apps 🔴 1
▼ 🟡  Brandywine Coins         Apps 🟡 1  Services 🟢 1  DBs 🟢 1   ↻ in_progress 2m14s
      applications
        ▼ 🟡  brandywinecoins    ✓ running  ↻ deploying cdecd75 2m14s
              https://bwcoins.ericdahl.dev
              ericdahl-dev/brandywinecoins@main
              Preparing container with helper image: coolify-helper:1.0.16
      services
          🟢  n8n                ✓ healthy
  🟢  tuner.social              Apps 🟢 1  Services 🟢 1  DBs 🟢 2

↑/↓ navigate  enter/space expand  f fix  o open  r refresh  m manage  q quit  ? help
```

## Features

- **Zero-config discovery** — projects, applications, services, and databases come from the Coolify
  API. There is no resource list to write or keep in sync.
- **Stoplight per project** — 🟢 🔴 🟡 ⚪ from the worst case across every resource it owns
- **Deploy-aware** — a running app whose last deploy failed reads red; an in-flight deploy shows
  its status, commit, and a live elapsed timer
- **Deploy log drill-down** — expand a resource to see the tail of its build log, fetched on
  demand, never during a poll
- **Active-first sorting** — deploying and broken projects surface to the top automatically
- **Auto-polling** — every 30 seconds by default; last-known state is retained and marked stale on
  API errors
- **Smart fix** — one confirmed key press redeploys a failed build, starts a stopped resource,
  restarts an unhealthy one, or cancels a stalled deploy
- **Stuck alerts** — POSTs a signed JSON event to your webhooks when a deploy or a resource stays
  wedged past a threshold, once per incident
- **Mute what you don't care about** — the manage screen (`m`) toggles any project or resource and
  writes the decision to the config
- **Multi-instance** — watch several Coolify installations at once
- **Tokens stay out of the config** — read them from an environment variable or a command
- **Single binary** — no runtime, no dependencies

## Install

### Homebrew

```bash
brew install --cask ericdahl-dev/tap/coolify-green
```

### Go

```bash
go install github.com/ericdahl-dev/coolify-green@latest
```

## Usage

```bash
coolify-green            # launch the dashboard
coolify-green init       # write a starter config
coolify-green --version  # print the version
coolify-green --help     # show usage
```

## First-time config

Run the interactive wizard. It asks for the instance URL, where the API token should come from, and
then verifies the connection before you ever reach the dashboard:

```bash
coolify-green init
```

It writes `~/.config/coolify-green/config.toml`. Use `--force` to overwrite an existing file.

Create the token in Coolify under **Settings → API Tokens**. Read access is enough for the
dashboard; the `f` fix key additionally needs the **deploy** ability.

## Config

```toml
[settings]
poll_interval_seconds              = 30
stuck_threshold_minutes            = 30   # how long something stays broken before it alerts
deploy_history_interval_seconds    = 300  # how often finished deploys are re-read
topology_refresh_interval_seconds  = 600  # how often the project map is rebuilt
watch = ["applications", "services", "databases"]

[[instances]]
name      = "studio"
url       = "https://coolify.example.com"
token_env = "COOLIFY_API_TOKEN"
# enabled = false                  # optional; stop polling without deleting

[[webhooks]]
url    = "https://hooks.example.com/coolify-green"
secret = "shared-secret"           # optional; enables request signing

# Written by the manage screen. Discovery is opt-out, so only exceptions appear here.
[[overrides]]
uuid    = "uc68mz9ty3x8mzlb3bkcygoo"
name    = "clawproxy.io"
enabled = false
```

### Where the token comes from

Pick whichever fits your setup — they are tried in this order:

```toml
[[instances]]
name = "studio"
url  = "https://coolify.example.com"

# 1. a command, so the secret never lands in a file
token_command = "doppler secrets get COOLIFY_API_KEY -p my-project -c prd --plain"

# 2. an environment variable
token_env = "COOLIFY_API_TOKEN"

# 3. written into the config (which is created 0600)
token = "1|abc123..."
```

With none of the three set, `COOLIFY_API_TOKEN` from the environment is used, so the common
single-instance case needs no token config at all.

### What gets watched

Everything, by default. `settings.watch` turns whole resource kinds off:

```toml
[settings]
watch = ["applications"]   # ignore services and databases entirely
```

and `m` in the dashboard mutes individual projects and resources. A muted project takes its
resources with it. Nothing muted is polled, and nothing muted alerts.

On a busy instance the first run is loud: a resource you stopped on purpose is indistinguishable
from one that crashed — Coolify reports both as `exited` and exposes no desired-state field — so
both read red. Muting is the escape hatch.

### Stuck alerts

A dashboard only helps when someone is looking at it. Configure one or more `[[webhooks]]` and
coolify-green POSTs a JSON event once a resource has been wedged for longer than
`stuck_threshold_minutes` (default `30`):

- **Deployment** — the latest deploy of an application is `failed`, or still `queued`/`in_progress`
- **Resource** — a container is `exited`/`stopped`/`degraded`, or running but `unhealthy`

Each wedged resource fires **once**, on the cycle it crosses the threshold — not every poll. Once it
recovers it is re-armed, so the next incident alerts again. Projects whose fetch failed are skipped,
so an expired token is not reported as a fleet-wide outage.

```json
{
  "event": "deployment_stuck",
  "reason": "deploy_failed",
  "instance": "studio",
  "project": "Brandywine Coins",
  "resource_type": "application",
  "resource": "brandywinecoins",
  "resource_uuid": "g1451pdxe7zdld5hsqgmeqja",
  "server": "ger3",
  "status": "failed",
  "detail": "cdecd75 (webhook, The tabs lead the page)",
  "url": "https://coolify.example.com/project/.../deployment/wuxzatl2ki6xfd9dlspepvs7",
  "stuck_since": "2026-01-02T03:04:05Z",
  "timestamp": "2026-01-02T03:35:05Z"
}
```

`event` is `deployment_stuck` or `resource_stuck`; `reason` is one of `deploy_failed`,
`deploy_in_progress`, `resource_down`, `resource_unhealthy`.

When a webhook has a `secret`, the request is signed with HMAC-SHA256 over the raw body and sent as
`X-Coolify-Green-Signature: sha256=<hex>` — verify it before trusting the payload. Delivery failures
are never retried and never interrupt polling; run with `COOLIFY_GREEN_DEBUG=1` to see them on
stderr.

## Keybindings

### Dashboard

| Key | Action |
|---|---|
| `↑` / `k` | Navigate up |
| `↓` / `j` | Navigate down |
| `enter` / `space` | Expand / collapse a project, or a resource's deploy log |
| `g` / `G` | Jump to first / last row |
| `f` | Smart fix (redeploy / start / restart / cancel) |
| `o` | Open the selected row in Coolify |
| `r` | Force refresh, and rebuild the project map |
| `m` | Manage what is monitored |
| `q` | Quit |
| `?` | Toggle help overlay |
| `esc` | Close help overlay |

### Smart fix (`f`)

`f` proposes exactly one action for the selected project and waits for `enter` to confirm:

| Situation | Action |
|---|---|
| A deploy is running normally | nothing — it is not a fault |
| A deploy has been running past the stall threshold | cancel the deployment |
| The latest deploy failed | redeploy, cache-busting |
| A resource is `exited` / `stopped` | start it |
| A resource is running but `unhealthy` | restart it |

A running deploy outranks everything else: no other action can succeed while Coolify holds the
build lock.

### Manage (`m`)

| Key | Action |
|---|---|
| `↑` / `k` | Navigate up |
| `↓` / `j` | Navigate down |
| `t` / `space` / `enter` | Mute / unmute the project or resource |
| `g` / `G` | Jump to first / last row |
| `esc` / `m` / `q` | Back to the dashboard |

Changes are written to `config.toml` immediately and the poller reloads automatically.

## How it polls

Per cycle, per instance, the cost is fixed no matter how many resources you have: one call each for
applications, services, and databases, plus one for every queued or running deploy on the instance.

The expensive call — the *last* deploy of an application, which Coolify returns with its entire
build log inlined — is rationed: it runs when a cache entry expires
(`deploy_history_interval_seconds`) or on the first cycle after a deploy finishes, so a completed
build shows its result immediately without paying for it every 30 seconds.

The project map is rebuilt on its own slow interval (`topology_refresh_interval_seconds`), or
whenever you press `r`.
