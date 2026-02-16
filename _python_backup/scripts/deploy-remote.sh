#!/bin/bash
# Deploy dozor to remote server for MCP access
#
# Usage:
#   ./scripts/deploy-remote.sh krolik    # Deploy to krolik server
#   ./scripts/deploy-remote.sh --local   # Test locally first

set -e

# Configuration
REMOTE_HOST="${1:-krolik}"
REMOTE_DIR="~/dozor"
MCP_PORT=8765

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# Get script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AGENT_DIR="$(dirname "$SCRIPT_DIR")"

if [ "$REMOTE_HOST" = "--local" ]; then
    info "Testing locally..."
    cd "$AGENT_DIR"
    pip install -e ".[sse]" 2>/dev/null || pip install mcp starlette uvicorn
    python -m dozor.mcp_server_sse --transport sse --port $MCP_PORT
    exit 0
fi

info "Deploying dozor to $REMOTE_HOST..."

# 1. Create remote directory structure
info "Creating remote directory structure..."
ssh "$REMOTE_HOST" "mkdir -p $REMOTE_DIR"

# 2. Sync agent code
info "Syncing code..."
rsync -avz --exclude '__pycache__' --exclude '*.pyc' --exclude '.git' \
    "$AGENT_DIR/" "$REMOTE_HOST:$REMOTE_DIR/"

# 3. Install dependencies on remote
info "Installing dependencies..."
ssh "$REMOTE_HOST" "cd $REMOTE_DIR && pip install -e '.[sse]' 2>/dev/null || pip install mcp starlette uvicorn"

# 4. Create systemd service
info "Creating systemd service..."
ssh "$REMOTE_HOST" "cat > /tmp/dozor-mcp.service << 'EOF'
[Unit]
Description=Server Agent MCP Server
After=network.target docker.service

[Service]
Type=simple
WorkingDirectory=$HOME/dozor
ExecStart=/usr/bin/python3 -m dozor.mcp_server_sse --transport sse --port $MCP_PORT
Restart=always
RestartSec=10
Environment=PYTHONUNBUFFERED=1

[Install]
WantedBy=multi-user.target
EOF
sudo mv /tmp/dozor-mcp.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable dozor-mcp
sudo systemctl restart dozor-mcp"

# 5. Verify service is running
sleep 2
if ssh "$REMOTE_HOST" "curl -s http://localhost:$MCP_PORT/health" | grep -q "healthy"; then
    info "MCP server is running!"
else
    warn "Service may not have started correctly. Check: ssh $REMOTE_HOST 'sudo journalctl -u dozor-mcp -f'"
fi

# 6. Print connection instructions
echo ""
echo "=========================================="
echo "Deployment complete!"
echo "=========================================="
echo ""
echo "To connect from local machine:"
echo ""
echo "1. Create SSH tunnel:"
echo "   ssh -L $MCP_PORT:localhost:$MCP_PORT $REMOTE_HOST -N &"
echo ""
echo "2. Add to Claude Code settings (~/.config/claude-code/settings.json):"
echo "   {"
echo "     \"mcpServers\": {"
echo "       \"dozor-remote\": {"
echo "         \"transport\": \"sse\","
echo "         \"url\": \"http://localhost:$MCP_PORT/sse\""
echo "       }"
echo "     }"
echo "   }"
echo ""
echo "3. Or use the proxy script:"
echo "   ./scripts/connect-remote.sh $REMOTE_HOST"
echo ""
