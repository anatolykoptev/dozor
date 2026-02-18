# Operational Memory

This file contains learned patterns and operational notes.
It is loaded into your context at startup to help you make better decisions.

## Infrastructure

### Docker Compose Services (~/krolik-server)
- postgres, qdrant, redis, rabbitmq — core data stores
- memdb-api (:8000) — MemDB REST API
- memdb-mcp (:8001) — MemDB MCP server
- memdb-go (:8080) — MemDB Go service
- cliproxyapi — LLM proxy (internal only, no host port)
- searxng (:8888) — search engine

### Systemd User Services
- go-search (:8890) — MCP search server
- vaelor-orchestrator (:18790) — central agent coordinator
- vaelor-content (:18791) — content agent
- vaelor-seo (:18794) — SEO agent
- vaelor-devops (:18793) — DevOps agent

### Remote Server (piter.now)
- Host: piteronline__usr58@s261005.hostiman.com
- Services: nginx, php84-php-fpm, mariadb, redis
- URL: https://piter.now

## Known Issues & Patterns

<!-- Add learned patterns here as you encounter them -->
<!-- Format: ### Issue Title -->
<!-- - Symptoms: what you see -->
<!-- - Root cause: why it happens -->
<!-- - Fix: what resolved it -->
