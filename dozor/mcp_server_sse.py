"""
Dozor MCP server with SSE transport for remote access.

Run locally on server:
    python -m dozor.mcp_server_sse --port 8765

Connect from local machine via SSH tunnel:
    ssh -L 8765:localhost:8765 <ssh-alias> -N &
    # Then configure Claude Code to connect to localhost:8765
"""

import asyncio
import argparse
import sys
import os
from contextlib import asynccontextmanager
from typing import AsyncIterator

# Add parent to path for imports
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from mcp.server import Server
from mcp.types import Tool, TextContent

from .mcp_tools import TOOLS
from .mcp_handlers import handle_tool, reset_agent

# Optional SSE transport
try:
    from mcp.server.sse import SseServerTransport
    from starlette.applications import Starlette
    from starlette.routing import Route
    from starlette.responses import JSONResponse
    import uvicorn
    SSE_AVAILABLE = True
except ImportError:
    SSE_AVAILABLE = False


# Create MCP server
server = Server("dozor")


@server.list_tools()
async def list_tools() -> list[Tool]:
    """List available tools."""
    return [
        Tool(
            name=tool["name"],
            description=tool["description"],
            inputSchema=tool["inputSchema"],
        )
        for tool in TOOLS
    ]


@server.call_tool()
async def call_tool(name: str, arguments: dict) -> list[TextContent]:
    """Handle tool calls."""
    result = handle_tool(name, arguments)
    return [TextContent(type="text", text=result)]


def create_sse_app(host: str = "127.0.0.1", port: int = 8765):
    """Create Starlette app with SSE transport."""
    if not SSE_AVAILABLE:
        raise ImportError(
            "SSE transport requires: pip install mcp[sse] starlette uvicorn"
        )

    sse = SseServerTransport("/messages/")

    async def handle_sse(request):
        async with sse.connect_sse(
            request.scope, request.receive, request._send
        ) as streams:
            await server.run(
                streams[0], streams[1],
                server.create_initialization_options()
            )

    async def handle_messages(request):
        await sse.handle_post_message(request.scope, request.receive, request._send)

    async def health(request):
        return JSONResponse({
            "status": "healthy",
            "server": "dozor",
            "transport": "sse",
            "tools": len(TOOLS),
        })

    async def reload(request):
        """Reload configuration by clearing agent cache."""
        reset_agent()
        return JSONResponse({
            "status": "reloaded",
            "message": "Agent cache cleared. Next request will use fresh config.",
        })

    app = Starlette(
        routes=[
            Route("/health", health),
            Route("/reload", reload, methods=["POST"]),
            Route("/sse", handle_sse),
            Route("/messages/", handle_messages, methods=["POST"]),
        ],
    )

    return app


async def run_stdio():
    """Run with stdio transport (for local use)."""
    from mcp.server.stdio import stdio_server

    async with stdio_server() as (read_stream, write_stream):
        await server.run(
            read_stream,
            write_stream,
            server.create_initialization_options(),
        )


def run_sse(host: str = "127.0.0.1", port: int = 8765):
    """Run with SSE transport (for remote use)."""
    if not SSE_AVAILABLE:
        print("ERROR: SSE transport requires additional dependencies:")
        print("  pip install mcp[sse] starlette uvicorn")
        sys.exit(1)

    app = create_sse_app(host, port)

    print(f"Starting MCP SSE server on http://{host}:{port}")
    print(f"  Health: http://{host}:{port}/health")
    print(f"  SSE:    http://{host}:{port}/sse")
    print()
    print("To connect from local machine:")
    print(f"  ssh -L {port}:localhost:{port} krolik -N &")
    print()

    uvicorn.run(app, host=host, port=port, log_level="info")


def main():
    parser = argparse.ArgumentParser(description="Dozor MCP Server")
    parser.add_argument(
        "--transport",
        choices=["stdio", "sse"],
        default="stdio",
        help="Transport type (default: stdio)"
    )
    parser.add_argument(
        "--host",
        default="127.0.0.1",
        help="Host to bind (default: 127.0.0.1, use 0.0.0.0 for all interfaces)"
    )
    parser.add_argument(
        "--port",
        type=int,
        default=8765,
        help="Port for SSE transport (default: 8765)"
    )

    args = parser.parse_args()

    if args.transport == "sse":
        run_sse(args.host, args.port)
    else:
        asyncio.run(run_stdio())


if __name__ == "__main__":
    main()
