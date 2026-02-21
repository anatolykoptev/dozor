# Installation

## Pre-built Binary (recommended)

Download the latest release for your platform:

```bash
# Linux amd64
curl -L https://github.com/anatolykoptev/dozor/releases/latest/download/dozor-linux-amd64 -o dozor
chmod +x dozor
mv dozor ~/.local/bin/
```

## Build from Source

Requires Go 1.23+.

```bash
git clone https://github.com/anatolykoptev/dozor.git
cd dozor
go build ./cmd/dozor/
mv dozor ~/.local/bin/
```

Or use `make`:

```bash
make install    # builds and copies to ~/.local/bin/
```

## Configuration

Copy the example config and edit as needed:

```bash
cp .env.example .env
```

All settings are optional — Dozor auto-detects Docker Compose projects and services. See [CONFIGURATION.md](CONFIGURATION.md) for the full list of environment variables.

## Running

### MCP Server (stdio)

For use with Claude Code, Cursor, or any MCP client:

```bash
dozor serve --stdio
```

### MCP Server (HTTP)

```bash
dozor serve --port 8765
```

### One-shot Diagnostics

```bash
dozor check              # Full triage
dozor check --json       # JSON output
dozor check --services web,api  # Specific services
```

### Periodic Monitoring

```bash
dozor watch --interval 4h
dozor watch --interval 30m --webhook https://hooks.example.com/alert
```

### Gateway Mode (full agent)

Runs MCP server + Telegram bot + watch mode + A2A protocol:

```bash
dozor gateway
```

Requires LLM configuration (`DOZOR_LLM_URL`, `DOZOR_LLM_MODEL`).

## MCP Client Configuration

### Claude Code / Claude Desktop

**Option A: Stdio over SSH** (recommended for remote servers)

```json
{
  "mcpServers": {
    "dozor": {
      "command": "ssh",
      "args": ["your-server", "dozor serve --stdio"]
    }
  }
}
```

**Option B: HTTP** (for local or network access)

```json
{
  "mcpServers": {
    "dozor": {
      "type": "streamable-http",
      "url": "http://localhost:8765/mcp"
    }
  }
}
```

## Systemd Service (optional)

For running Dozor as a persistent service:

```ini
# ~/.config/systemd/user/dozor.service
[Unit]
Description=Dozor Server Monitor
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=%h/.local/bin/dozor gateway
WorkingDirectory=%h
EnvironmentFile=%h/dozor/.env
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
```

Enable and start:

```bash
systemctl --user daemon-reload
systemctl --user enable --now dozor
loginctl enable-linger $USER    # auto-start on boot
```

## Docker (alternative)

```dockerfile
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o dozor ./cmd/dozor/

FROM alpine:3.20
COPY --from=builder /app/dozor /usr/local/bin/
ENTRYPOINT ["dozor"]
CMD ["serve", "--port", "8765"]
```

```bash
docker build -t dozor .
docker run -d --name dozor \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -p 8765:8765 \
  --env-file .env \
  dozor
```

> Note: Mount the Docker socket for container monitoring.
