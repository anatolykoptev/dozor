---
name: remote-server
description: "Remote server management and troubleshooting via SSH. AUTO-CONSULT when: remote site is down, remote service issues, nginx/PHP/database errors, SSL problems on remote server, or healthcheck reports remote issues."
---

# Remote Server

Manage a remote server via SSH. All operations use server_remote and server_inspect tools.

## Services

| Service | Role | Port |
|---------|------|------|
| nginx | Web server / reverse proxy | 80, 443 |
| php-fpm | PHP application server | 9000 (unix socket) |
| database (mariadb/mysql) | Database | 3306 |
| redis | Cache | 6379 |

## Quick Status

```
server_inspect(mode: "remote")
```
Returns: HTTP status, SSL expiry, all service states, disk/memory/load.

## Troubleshooting Flows

### Site Unreachable (HTTP down)

```
1. server_inspect(mode: "remote") — confirm HTTP fail + check all services
2. If nginx down:
   server_remote(action: "restart", service: "nginx")
3. If PHP down:
   server_remote(action: "restart", service: "php-fpm")
4. If both up but HTTP fails:
   server_remote(action: "logs", service: "nginx", lines: 100)
   -> Look for upstream errors, config issues
5. If DB down:
   Restart in order: database -> redis -> php-fpm -> nginx
6. Verify: server_inspect(mode: "remote")
```

### Service Down

```
1. server_remote(action: "status") — confirm which service
2. server_remote(action: "logs", service: NAME, lines: 50) — check why
3. server_remote(action: "restart", service: NAME) — restart
4. server_remote(action: "status") — verify recovery
5. If still down after 2 attempts: escalate with log excerpt
```

### SSL Certificate Expiring

```
1. server_inspect(mode: "remote") — check SSL expiry
2. If < 7 days: escalate immediately (P0)
3. If < 14 days: report as P1 warning
4. Renewal is handled externally (Let's Encrypt / hosting panel)
```

### High Load on Remote

```
1. Use server_remote_exec tool with "top -bn1 | head -20"
2. Check if PHP-FPM workers (traffic spike) or DB queries
3. If PHP-FPM: check for stuck processes
   server_remote(action: "logs", service: "php-fpm", lines: 100)
4. If database: check slow queries
   server_remote(action: "logs", service: "database", lines: 100)
```

### Disk Full on Remote

```
1. Use server_remote_exec tool with "df -h"
2. Check application debug.log size
3. If debug.log large: truncate or rotate
4. Check nginx access logs
5. Escalate if not enough to free
```

## Restart Order (if multiple services down)

1. **database** — database must be first
2. **redis** — cache before app server
3. **php-fpm** — app server needs DB + cache
4. **nginx** — web server needs PHP upstream

Wait 5-10s between each restart.

## Rules

- Always check status before restarting — do not restart healthy services
- After restart, verify with server_remote(action: "status")
- Never restart database without warning — active queries will be killed
- Log excerpts are key for escalation — always include them
- SSL issues cannot be fixed by Dozor — escalate with expiry date
