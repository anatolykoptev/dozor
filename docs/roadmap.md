# Service Discovery Roadmap

## Phase 1: Docker Go SDK (current)

Replace CLI-based discovery with Docker Go SDK for local mode.

### Changes
- **discovery.go** — SDK-based `DiscoverContainers()` with caching (30s TTL)
- **watcher.go** — `ContainerWatcher` listens to Docker events, invalidates cache
- **status.go** — SDK-based `GetContainerStatus()` with label parsing
- **agent.go** — Initialize SDK client + watcher in `NewAgent()`
- **systemd.go** — `DiscoverUserServices()` auto-scans active user units
- **tools/services.go** — Use discovery when `DOZOR_USER_SERVICES` is empty

### Label support
- `dozor.enable` — `true`/`false` opt-in/opt-out
- `dozor.group` — grouping in health output
- `dozor.name` — display name override

### Fallback chain
1. `DOZOR_SERVICES` env var (explicit override)
2. Docker SDK discovery (local) / `docker compose ps` CLI (remote/SSH)
3. All running containers via SDK (if no compose project)

## Phase 2: Enhanced Labels

- `dozor.healthcheck.url` — custom HTTP health endpoint
- `dozor.logs.pattern` — custom error pattern for log analysis
- `dozor.alert.channel` — per-service alert routing

## Phase 3: Service Groups & Dependencies

- `dozor.depends_on` — dependency graph for restart ordering
- `dozor.group` — aggregate health by group
- Dashboard view per group
