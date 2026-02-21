---
name: service-dependencies
description: "Container dependency graph, restart order, and cascading failure handling. AUTO-CONSULT when: restarting multiple services, dealing with cascading failures, database unreachable, or planning a full stack restart."
---

# Service Dependencies

Know the dependency graph before restarting anything. Wrong order causes cascading failures.

## Docker Compose Stack

Map your service dependencies. Example layout:

```
             database (:5432)
             vector-db (:6333)
             redis (:6379)
             message-queue (:5672)
                   ↓
           api-service (:8000)    ← needs database, vector-db, redis, message-queue
                   ↓
           worker-service (:8080) ← needs api-service
           mcp-service (:8001)    ← needs database, vector-db

           search-engine (:8888)  ← standalone
           llm-proxy              ← standalone
```

## Restart Order

When restarting multiple services, follow this order:

1. **Infrastructure** (no deps): database, vector-db, redis, message-queue
2. **Standalone services**: llm-proxy, search-engine
3. **API services** (need infra up + healthy)
4. **Workers/consumers** (need API services)
5. **MCP/gateway services** (need data stores)

Wait 10-15s between tiers for services to become healthy.

## Dependency Chain Example

Database issues cascade: `database → api-service → worker-service`

```
1. Check worker-service — if down, check api-service first
2. Check api-service — if down, check database first
3. Check database — if down, restart database, wait 10s
4. Restart api-service, wait 10s
5. Restart worker-service
6. Verify: server_inspect(mode: "health")
```

Never restart downstream services without checking their upstream dependencies.

## Systemd Services (user-level)

```
search-service (:8890)      ← may need search-engine
agent-service (:18790)      ← may need llm-proxy
```

Use `server_services(action: "restart", service: NAME)` for these.

## Remote Server (restart order)

```
1. database     ← database first
2. redis        ← cache
3. app-server   ← app server (needs DB + cache)
4. web-server   ← web server (needs app upstream)
```

Use `server_remote(action: "restart", service: NAME)` for these.

## Full Stack Restart

Only when needed (coordinated restart of everything):

```
1. server_restart for each container in order above
2. Wait 30s for all to stabilize
3. server_services(action: "restart-all") for systemd services
4. Wait 15s
5. server_triage() to verify everything
```

## Rules

- Always restart in dependency order (deps first, then dependents)
- Wait between tiers — don't rush
- After cascade restart, always verify with server_triage or server_inspect(mode: "health")
- If a dependency won't start, don't bother restarting dependents — fix the root
