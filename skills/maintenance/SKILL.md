---
name: maintenance
description: "System maintenance procedures: disk cleanup, Docker pruning, log rotation, resource management. AUTO-CONSULT when: disk usage is high, user asks about cleanup, periodic maintenance needed, or server_cleanup/server_prune results need interpretation."
---

# Maintenance

Proactive resource management to prevent incidents.

## Disk Management

### Thresholds
| Usage | Status | Action |
|-------|--------|--------|
| < 60% | Healthy | No action needed |
| 60-80% | Watch | Report in next status check |
| 80-90% | Warning | Run targeted cleanup |
| 90-95% | Critical | Immediate cleanup, escalate if insufficient |
| > 95% | Emergency | Aggressive cleanup + escalate |

### Cleanup Priority (safest first)

1. **Journal logs** — `server_cleanup(targets: ["journal"], report: false, min_age: "3d")`
   - Typically frees 200MB-2GB
   - Zero risk

2. **Temp files** — `server_cleanup(targets: ["tmp"], report: false, min_age: "7d")`
   - Frees build artifacts, old downloads
   - Low risk

3. **Docker build cache** — `server_prune(build_cache: true, images: false, age: "48h")`
   - Frees 1-10GB typically
   - Safe: only removes unused cache layers

4. **Docker images** — `server_prune(images: true, age: "48h")`
   - Frees dangling/unused images
   - Safe: only removes images not used by any container

5. **Package caches** — `server_cleanup(targets: ["go", "npm", "pip", "uv"], report: false)`
   - Frees build caches for various package managers
   - Moderate risk: may slow next build

6. **Docker volumes** — `server_prune(volumes: true)`
   - DANGEROUS: may delete persistent data
   - Only with user approval

### Always Scan First

Before any cleanup, run dry-run:
```
server_cleanup(targets: ["all"], report: true)
```
This shows reclaimable sizes without deleting anything.

## Docker Maintenance

### Regular Pruning (weekly)
```
server_prune(images: true, build_cache: true, age: "48h")
```

### After Failed Deploys
```
server_prune(build_cache: true, images: true, age: "1h")
```
Failed builds leave large intermediate layers.

### Container Log Growth
Docker container logs can grow unbounded. Check with:
```
server_inspect(mode: "overview")
```

## System Resource Monitoring

### Memory Pressure
If memory > 90%:
1. `server_inspect(mode: "overview")` to identify top consumers
2. Check for container memory leaks: is any container using 2x+ its normal?
3. Restart the leaky container
4. If no obvious leak: report to user

### CPU Load
If load > 2x cores for extended period:
1. `server_inspect(mode: "overview")` to check top processes
2. Usually a runaway container or build process
3. Check recent deployments or cron jobs

## Rules

- Always dry-run (report: true) before cleanup unless responding to > 90% disk emergency
- Never prune volumes without user approval
- After cleanup, verify with server_inspect(mode: "overview") to confirm space freed
- Record large cleanups in memory for trending
