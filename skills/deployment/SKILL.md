---
name: deployment
description: "Deployment procedures with pre-checks, verification, and rollback awareness. AUTO-CONSULT when: deploying services, running server_deploy, updating Docker stack, or user asks to deploy/update."
---

# Deployment

Safe deployment flow: pre-check, deploy, verify.

## Deploy Flow

```
1. PRE-CHECK
   - server_inspect(mode: "health") — are all services healthy?
   - server_inspect(mode: "overview") — enough disk/memory for build?
   - If issues exist: fix first, then deploy

2. DEPLOY
   - server_deploy(project_path: PATH, build: true, pull: true)
   - Returns deploy_id for status polling

3. MONITOR
   - server_deploy(deploy_id: ID) — poll until complete
   - Watch for build errors, timeout (5 min max)

4. VERIFY
   - server_inspect(mode: "health") — all services came back?
   - server_triage() — any new issues?
   - If service is down after deploy: check logs immediately

5. NOTIFY
   - Report success/failure to user
   - If failed: include error details and what was rolled back
```

## Pre-Deploy Checklist

| Check | Tool | Threshold |
|-------|------|-----------|
| All services healthy | server_inspect(mode: "health") | No exited/unhealthy |
| Disk space | server_inspect(mode: "overview") | < 80% used |
| Memory available | server_inspect(mode: "overview") | > 1GB free |
| No active incidents | server_triage() | No P0 issues |

If any check fails, warn user before proceeding.

## Docker Compose Deploy

Standard deployment for a Docker Compose project:
```
server_deploy(project_path: "/path/to/your/project")
```

Deploy specific services only:
```
server_deploy(project_path: "/path/to/your/project", services: ["api-service", "worker-service"])
```

Skip pull (local changes only):
```
server_deploy(project_path: "/path/to/your/project", pull: false)
```

## Post-Deploy Issues

### Service Won't Start After Deploy
1. server_inspect(mode: "logs", service: NAME, lines: 50) to check startup errors
2. Common causes: config mismatch, missing env vars, port conflict
3. If code issue: escalate to devops

### Performance Degradation After Deploy
1. server_inspect(mode: "overview") to check resource usage
2. Compare with pre-deploy baseline
3. May need container resource limit adjustment

### Rollback
Dozor does not have a built-in rollback mechanism. If deploy breaks things:
1. Check git log for previous working commit
2. Escalate to devops for code rollback
3. If Docker images: previous images may still exist locally

## Rules

- Always pre-check before deploying
- Never deploy during an active P0 incident
- After deploy, always verify health
- If deploy fails, report the exact error, do not retry blindly
- Record deploy outcomes in memory for patterns
