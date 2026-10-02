# ADR-0001: Discover resources from the API instead of listing them in config

**Status**: accepted
**Date**: 2026-08-26

## Context

coolify-green is modeled on aws-green, where the config file names every pipeline, stack, and ECS
service to watch. That works for AWS because there is no single call that answers "what does this
account deploy?" — you have to know what you are looking for.

Coolify is the opposite. Four calls (`/projects`, `/applications`, `/services`, `/databases`) return
the entire instance, and every resource already carries the grouping the user thinks in: the Coolify
project. Transcribing 70 resources into TOML by hand would produce a config that is wrong the day
after a new app is created.

## Decision

Discovery is automatic and opt-out. The config file holds instances and exceptions, nothing else.

- Projects, applications, services, and databases come from the API every cycle.
- Resources are grouped under the project that owns their environment.
- `[[overrides]]` mutes a project or resource by uuid; anything without an override is monitored.
- The manage screen writes overrides, so muting never means editing TOML.

## Consequences

**A resource appears the moment it is created.** No config change, no restart. The project map is
cached on a slow interval, so a brand-new project shows under `(unassigned)` until the next topology
refresh or a manual `r`.

**Muted resources must survive filtering.** The dashboard only sees monitored resources, so the
manage screen reads a separate inventory — the unfiltered discovery result — otherwise a muted
resource would vanish and could never be switched back on.

**The first run is loud.** Coolify has no desired-state field: an app you stopped on purpose reports
`exited` exactly like one that crashed, so both read red. This is accepted rather than papered over
— guessing intent would hide real outages — and muting is the documented escape hatch.

**Resources have to be matched by uuid, not name.** Names repeat across an instance (two
`southbend.tech` applications exist on the studio instance today), and the instance-wide deployments
endpoint identifies an application only by numeric id and name — so the application uuid is parsed
out of `deployment_url`, which is the one field that carries it.
