#!/bin/bash
# Connect to remote dozor MCP server via SSH tunnel
#
# Usage:
#   ./scripts/connect-remote.sh krolik       # Start tunnel
#   ./scripts/connect-remote.sh krolik test  # Test connection
#   ./scripts/connect-remote.sh --stop       # Stop tunnel

set -e

REMOTE_HOST="${1:-krolik}"
ACTION="${2:-start}"
MCP_PORT=8765

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; }

# Check if tunnel already exists
tunnel_exists() {
    pgrep -f "ssh.*-L.*$MCP_PORT:localhost:$MCP_PORT" > /dev/null 2>&1
}

stop_tunnel() {
    if tunnel_exists; then
        info "Stopping existing tunnel..."
        pkill -f "ssh.*-L.*$MCP_PORT:localhost:$MCP_PORT" || true
        sleep 1
    fi
}

start_tunnel() {
    if tunnel_exists; then
        info "Tunnel already running"
        return 0
    fi

    info "Starting SSH tunnel to $REMOTE_HOST:$MCP_PORT..."
    ssh -f -N -L $MCP_PORT:localhost:$MCP_PORT "$REMOTE_HOST"

    sleep 1
    if tunnel_exists; then
        info "Tunnel established!"
    else
        error "Failed to start tunnel"
        exit 1
    fi
}

test_connection() {
    info "Testing connection to MCP server..."

    if ! tunnel_exists; then
        warn "Tunnel not running. Starting..."
        start_tunnel
    fi

    if curl -s "http://localhost:$MCP_PORT/health" | grep -q "healthy"; then
        info "MCP server is healthy!"
        echo ""
        curl -s "http://localhost:$MCP_PORT/health" | python3 -m json.tool
    else
        error "Cannot connect to MCP server"
        echo "Check if service is running: ssh $REMOTE_HOST 'sudo systemctl status dozor-mcp'"
        exit 1
    fi
}

case "$ACTION" in
    start)
        start_tunnel
        echo ""
        echo "Add to Claude Code settings (~/.config/claude-code/settings.json):"
        echo ""
        echo "  \"dozor-remote\": {"
        echo "    \"transport\": \"sse\","
        echo "    \"url\": \"http://localhost:$MCP_PORT/sse\""
        echo "  }"
        ;;
    test)
        test_connection
        ;;
    stop|--stop)
        stop_tunnel
        info "Tunnel stopped"
        ;;
    *)
        echo "Usage: $0 <host> [start|test|stop]"
        exit 1
        ;;
esac
