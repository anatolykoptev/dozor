#!/bin/bash
# Dozor - One-line installer
# Usage: curl -fsSL https://raw.githubusercontent.com/anatolykoptev/dozor/main/install.sh | bash
#
# Or with custom config:
#   curl -fsSL ... | SERVICES="nginx,postgres" COMPOSE_PATH="~/myproject" bash

set -e

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

info() { echo -e "${GREEN}[+]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
error() { echo -e "${RED}[x]${NC} $1"; exit 1; }

# Configuration
INSTALL_DIR="${INSTALL_DIR:-$HOME/dozor}"
REPO_URL="https://github.com/anatolykoptev/dozor.git"

info "Installing Dozor to $INSTALL_DIR..."

# Check dependencies
command -v python3 >/dev/null 2>&1 || error "python3 is required"
command -v pip3 >/dev/null 2>&1 || command -v pip >/dev/null 2>&1 || error "pip is required"
command -v git >/dev/null 2>&1 || error "git is required"

# Clone or update
if [ -d "$INSTALL_DIR/.git" ]; then
    info "Updating existing installation..."
    cd "$INSTALL_DIR"
    git pull --ff-only
else
    info "Cloning repository..."
    git clone "$REPO_URL" "$INSTALL_DIR"
    cd "$INSTALL_DIR"
fi

# Install dependencies
info "Installing Python dependencies..."
pip3 install -q -e . 2>/dev/null || pip install -q -e .

# Create .env if not exists
if [ ! -f "$INSTALL_DIR/.env" ]; then
    info "Creating .env file..."

    # Use provided vars or prompt
    COMPOSE_PATH="${COMPOSE_PATH:-}"
    SERVICES="${SERVICES:-}"

    if [ -z "$COMPOSE_PATH" ]; then
        echo -n "Enter docker-compose project path (e.g., ~/myproject): "
        read COMPOSE_PATH
    fi

    if [ -z "$SERVICES" ]; then
        echo -n "Enter services to monitor (comma-separated, e.g., nginx,postgres): "
        read SERVICES
    fi

    cat > "$INSTALL_DIR/.env" << EOF
# Dozor Configuration
SERVER_HOST=local
SERVER_USER=$(whoami)
SERVER_COMPOSE_PATH=$COMPOSE_PATH
SERVER_SERVICES=$SERVICES
EOF

    info "Created .env with your configuration"
else
    warn ".env already exists, skipping"
fi

# Create run script
cat > "$INSTALL_DIR/run-mcp.sh" << 'EOF'
#!/bin/bash
cd "$(dirname "$0")"
exec python3 -m dozor.mcp_server
EOF
chmod +x "$INSTALL_DIR/run-mcp.sh"

# Verify installation
info "Verifying installation..."
if python3 -c "from dozor import ServerAgent; print('OK')" 2>/dev/null; then
    echo ""
    echo -e "${GREEN}========================================${NC}"
    echo -e "${GREEN}Dozor installed successfully!${NC}"
    echo -e "${GREEN}========================================${NC}"
    echo ""
    echo "Configuration: $INSTALL_DIR/.env"
    echo ""
    echo "Test CLI:"
    echo "  cd $INSTALL_DIR && python3 -m dozor.cli health"
    echo ""
    echo "MCP config (add to Claude Code):"
    echo '  {'
    echo '    "mcpServers": {'
    echo '      "dozor": {'
    echo '        "command": "ssh",'
    echo "        \"args\": [\"$(hostname)\", \"$INSTALL_DIR/run-mcp.sh\"]"
    echo '      }'
    echo '    }'
    echo '  }'
    echo ""
else
    error "Installation verification failed"
fi
