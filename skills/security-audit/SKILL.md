---
name: security-audit
description: "Security review methodology for server infrastructure. AUTO-CONSULT when: user asks for security check, audit, or hardening review, or when investigating suspicious activity."
---

# Security Audit

Systematic security review using Dozor inspection tools.

## Audit Flow

```
1. NETWORK — exposed ports, container isolation
   server_inspect(mode: "security")

2. SERVICES — running versions, known vulnerabilities
   server_inspect(mode: "health")

3. SSL — certificate expiry and configuration
   server_inspect(mode: "remote")  <- checks remote server SSL

4. ACCESS — authentication, API keys, exposed endpoints
   server_inspect(mode: "security")

5. RESOURCES — unusual disk/memory/CPU patterns
   server_inspect(mode: "overview")
```

## What to Check

### Network Exposure
- Ports bound to 0.0.0.0 vs 127.0.0.1
- Services that should be internal-only (postgres, redis, rabbitmq)
- Docker network isolation between containers
- Firewall rules (ufw/iptables)

### Container Security
- Containers running as root
- Privileged containers
- Unbounded resource limits (memory, CPU)
- Outdated base images

### SSL/TLS
- Certificate expiry < 14 days = WARNING, < 7 days = CRITICAL
- HTTP to HTTPS redirect working
- TLS version (should be 1.2+)

### Authentication
- API endpoints without auth
- Default passwords on services
- Exposed management ports (RabbitMQ management, Qdrant dashboard)

### Logs
- Failed SSH login attempts
- Repeated 401/403 in nginx
- Unusual access patterns (high request rate from single IP)

## Severity Classification

| Finding | Severity |
|---------|----------|
| Service bound to 0.0.0.0 with no auth | P0 |
| SSL expires < 7 days | P0 |
| Container running as root with volumes | P1 |
| SSL expires < 14 days | P1 |
| Outdated base images | P2 |
| Missing rate limiting | P2 |

## Audit Report Format

```
## Security Audit — [date]

### Status: [PASS / NEEDS ATTENTION / CRITICAL]

### Findings
1. [severity] [description] — [recommendation]
2. ...

### Positive
- [good practices in place]

### Recommendations
- [prioritized action items]
```

## Rules

- Security audit is read-only — never change configs during audit
- Report findings, recommend fixes, let user decide
- Escalate P0 security findings to orchestrator immediately
- Run security audit as part of full reports (reporting skill)
