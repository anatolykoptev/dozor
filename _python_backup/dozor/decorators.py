"""
Decorators for Dozor MCP handlers.

Provides reusable validation patterns to eliminate code duplication.
"""

from functools import wraps
from typing import Any, Callable

from .validation import validate_service_name


def require_valid_service(func: Callable[[dict[str, Any]], str]) -> Callable[[dict[str, Any]], str]:
    """
    Decorator to validate service name argument.

    Extracts 'service' from arguments dict and validates it.
    Returns error string if invalid, otherwise calls the handler.

    Usage:
        @require_valid_service
        def handle_server_status(arguments: dict[str, Any]) -> str:
            service = arguments["service"]
            # ... handler logic
    """
    @wraps(func)
    def wrapper(arguments: dict[str, Any]) -> str:
        service = arguments.get("service")
        if not service:
            return "Error: service is required"

        valid, reason = validate_service_name(service)
        if not valid:
            return f"Invalid service name: {reason}"

        return func(arguments)

    return wrapper
