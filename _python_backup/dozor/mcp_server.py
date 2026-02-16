"""
Dozor MCP server for server diagnostics.

Run with:
    python -m dozor.mcp_server
    # or
    dozor-mcp
"""

import asyncio
import json
import sys
import os

# Add parent to path for imports
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from mcp.server import Server
from mcp.server.stdio import stdio_server
from mcp.types import Tool, TextContent

from .mcp_tools import TOOLS
from .mcp_handlers import handle_tool


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


async def main():
    """Run the MCP server."""
    async with stdio_server() as (read_stream, write_stream):
        await server.run(
            read_stream,
            write_stream,
            server.create_initialization_options(),
        )


if __name__ == "__main__":
    asyncio.run(main())
