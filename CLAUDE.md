# Dozor - Server Monitoring MCP

## Rule: No Hardcoding

**All configuration MUST come from environment variables.**

- Server host, user, port → `.env`
- Project paths → `SERVER_COMPOSE_PATH`
- Service lists → `SERVER_SERVICES`

Never hardcode IPs, paths, or usernames in code.

## Setup

```bash
cp .env.example .env
# Edit .env with your values
```

cd dozor && pytest tests/ -v