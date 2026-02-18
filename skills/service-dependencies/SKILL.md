---
name: service-dependencies
description: "Container dependency graph, restart order, and cascading failure handling. AUTO-CONSULT when: restarting multiple services, dealing with cascading failures, MemDB unreachable, or planning a full stack restart."
---

# Service Dependencies

Know the dependency graph before restarting anything. Wrong order causes cascading failures.

## Docker Compose (krolik-server)

```
             postgres (:5432)
             qdrant (:6333)
             redis (:6379)
             rabbitmq (:5672)
                   ↓
           memdb-api (:8000)    ← needs postgres, qdrant, redis, rabbitmq
                   ↓
           memdb-go (:8080)     ← needs memdb-api
           memdb-mcp (:8001)    ← needs postgres, qdrant

           searxng (:8888)      ← standalone
           cliproxyapi           ← standalone (LLM proxy, no host port)
           openlist              ← standalone
```

## Restart Order

When restarting multiple services, follow this order:

1. **Infrastructure** (no deps): postgres, qdrant, redis, rabbitmq
2. **Standalone services**: cliproxyapi, searxng, openlist
3. **memdb-api** (needs infra up + healthy)
4. **memdb-go** (needs memdb-api)
5. **memdb-mcp** (needs postgres, qdrant)

Wait 10-15s between tiers for services to become healthy.

## MemDB Dependency Chain

MemDB issues cascade: `postgres → memdb-api → memdb-go`

```
1. Check memdb-go (:8080) — if down, check memdb-api first
2. Check memdb-api (:8000) — if down, check postgres first
3. Check postgres (:5432) — if down, restart postgres, wait 10s
4. Restart memdb-api, wait 10s
5. Restart memdb-go
6. Verify: server_inspect(mode: "health")
```

Never restart memdb-go without checking memdb-api. Never restart memdb-api without checking postgres.

## Systemd Services (user-level)

```
go-search (:8890)              ← needs searxng
vaelor-orchestrator (:18790)   ← needs cliproxyapi (LLM)
vaelor-content (:18791)        ← needs cliproxyapi, memdb-go
vaelor-seo (:18794)            ← needs cliproxyapi
vaelor-devops (:18793)         ← needs cliproxyapi
```

Use `server_services(action: "restart", service: NAME)` for these.

## Piteronline Remote (restart order)

```
1. mariadb     ← database first
2. redis       ← cache
3. php84-php-fpm  ← app server (needs DB + cache)
4. nginx       ← web server (needs PHP upstream)
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
