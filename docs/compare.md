# Service Discovery: Comparison of Approaches

## Current State (dozor pre-v2)

| Feature | dozor | Pulse | Traefik | Docktail |
|---------|-------|-------|---------|----------|
| Docker discovery | CLI (`docker compose ps`) | Docker SDK + events | Docker SDK + events | Docker SDK |
| Label-based filtering | No | No | Yes (core feature) | No |
| Systemd discovery | Static list only | N/A | N/A | N/A |
| Hot reload | None (re-read on each call) | Event-driven | Event-driven | Polling |
| Non-compose containers | No | Yes | Yes | Yes |
| Resource usage | CLI (`docker stats`) | SDK stats API | N/A | N/A |
| Caching | None | In-memory | In-memory | None |

## Discovery Methods

### CLI-based (`docker ps`, `docker compose ps`)
- **Pros:** No dependencies, works over SSH, simple
- **Cons:** Subprocess per call, JSON parsing, no events, slow

### Docker Go SDK (`github.com/docker/docker/client`)
- **Pros:** Native Go types, event streaming, connection pooling, labels built-in
- **Cons:** New dependency (~5MB), only works with local Docker socket
- **Used by:** Traefik, Portainer, Watchtower, Pulse

### Docker Events (real-time)
- **CLI:** `docker events --format json` — long-running subprocess
- **SDK:** `client.Events()` — native Go channel, clean lifecycle
- **Benefit:** Instant cache invalidation on container start/stop/die

## Label-Based Configuration

Industry standard (Traefik pioneered). Labels on containers replace static config:

```yaml
services:
  myapp:
    labels:
      dozor.enable: "true"        # opt-in/opt-out
      dozor.group: "core"         # grouping in output
      dozor.name: "My App"        # display name override
```

## Decision

**Docker Go SDK** for local monitoring. CLI remains as fallback for SSH/remote mode.
Gives us: native events, labels, typed responses, connection pooling.
