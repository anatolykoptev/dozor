---
name: capacity-planning
description: "Capacity trend analysis and projections: disk growth rates, resource consumption patterns, and time-to-exhaustion estimates. AUTO-CONSULT when: disk usage exceeds 60%, memory usage is consistently high, user asks about server capacity or growth trends, or during periodic maintenance reviews."
---

# Capacity Planning

Predict resource exhaustion before it becomes an incident.

## Quick Assessment

Run these in sequence:

```
1. server_inspect(mode: "overview")    — current snapshot
2. server_cleanup(report: true)        — reclaimable space
3. server_inspect(mode: "health")      — service resource usage
```

## Disk Capacity

### Measuring Growth

Use the overview data to estimate daily growth:

| Source | Typical growth | Check with |
|--------|---------------|------------|
| Docker images/cache | 500MB-2GB/week | server_prune dry-run |
| Container logs | 50-500MB/day | server_inspect(mode: "overview") |
| Journal logs | 10-100MB/day | server_cleanup(targets: ["journal"], report: true) |
| Application data (postgres, qdrant) | Varies | server_container_exec |
| Build artifacts (go, npm) | 200MB-1GB/build | server_cleanup(targets: ["go", "npm"], report: true) |

### Time-to-Exhaustion

```
Days remaining = (Total - Used) / Daily growth rate
```

| Days remaining | Urgency | Action |
|---------------|---------|--------|
| > 90 days | Healthy | No action |
| 30-90 days | Watch | Schedule cleanup, report to user |
| 14-30 days | Warning | Run cleanup, set up regular pruning |
| 7-14 days | Critical | Immediate cleanup + report to user |
| < 7 days | Emergency | Aggressive cleanup + escalate |

### Reporting Format

```
Disk: XX% used (YY GB free of ZZ GB)
Growth estimate: ~A GB/day
Time to 90%: ~B days
Time to full: ~C days
Top consumers: [list with sizes]
Reclaimable now: D GB (cleanup dry-run)
```

## Memory Capacity

### Per-Service Baselines

Track memory usage from server_inspect(mode: "health"):

| Service | Normal range | Concern threshold |
|---------|-------------|-------------------|
| postgres | 200-500MB | > 1GB |
| qdrant | 300-800MB | > 1.5GB |
| memdb-api | 100-300MB | > 500MB |
| memdb-go | 50-150MB | > 300MB |
| searxng | 100-200MB | > 400MB |

If a service consistently exceeds its baseline by 2x+:
- Likely memory leak
- Record trend in memory
- Recommend scheduled restart as temporary fix
- Escalate for code-level investigation

### System Memory

```
Total memory - (sum of container memory) = available headroom
```

If headroom < 1GB: report as P1 warning.
If headroom < 500MB: report as P0, recommend identifying and restarting the largest consumer.

## Capacity Report Template

Use for periodic reviews or when user asks about capacity:

```
Title: Capacity Report — [YYYY-MM-DD]

## Disk
- Usage: XX% (YY GB / ZZ GB)
- Reclaimable: A GB
- Estimated growth: ~B GB/day
- Time to 90%: ~C days
- Recommendation: [action or "healthy"]

## Memory
- System: XX% (YY GB / ZZ GB)
- Top containers: [name: size, name: size, ...]
- Headroom: A GB
- Trend: [stable / growing / shrinking]
- Recommendation: [action or "healthy"]

## Docker
- Images: XX (YY GB, ZZ unused)
- Build cache: A GB
- Volumes: B (C GB)
- Recommendation: [prune schedule]
```

## Proactive Actions

| Trigger | Automatic action |
|---------|-----------------|
| Disk > 80% during triage | Calculate time-to-90%, include in report |
| Memory > 85% during triage | List top 3 consumers with sizes |
| Same service grows 2x in a week | Flag as potential leak in report |
| Post-cleanup: less than 5% freed | Report: cleanup insufficient, disk growth exceeds reclaimable |

## Integration with Other Skills

- **maintenance** — capacity-planning identifies WHEN to clean; maintenance knows HOW
- **incident-response** — if time-to-exhaustion < 7 days, escalate as P1
- **post-mortem** — disk-full incidents should include growth rate analysis
- **reporting** — capacity data enhances periodic status reports

## Rules

- Never guess growth rates — calculate from actual data or state it is a rough estimate
- Always show both absolute numbers and percentages
- Include time-to-exhaustion in any disk warning
- Track baselines over time using update_memory — past data makes predictions better
- When reporting capacity, always include what is reclaimable (cleanup dry-run)
