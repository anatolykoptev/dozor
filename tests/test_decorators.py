"""Tests for decorators module."""

import pytest
from unittest.mock import MagicMock, patch

from dozor.decorators import require_valid_service


class TestRequireValidService:
    """Tests for @require_valid_service decorator."""

    def test_valid_service_calls_handler(self):
        """Test that valid service name allows handler execution."""
        mock_handler = MagicMock(return_value="success")
        decorated = require_valid_service(mock_handler)

        result = decorated({"service": "nginx"})

        mock_handler.assert_called_once_with({"service": "nginx"})
        assert result == "success"

    def test_missing_service_returns_error(self):
        """Test that missing service returns error."""
        mock_handler = MagicMock(return_value="success")
        decorated = require_valid_service(mock_handler)

        result = decorated({})

        mock_handler.assert_not_called()
        assert "required" in result.lower()

    def test_empty_service_returns_error(self):
        """Test that empty service returns error."""
        mock_handler = MagicMock(return_value="success")
        decorated = require_valid_service(mock_handler)

        result = decorated({"service": ""})

        mock_handler.assert_not_called()
        assert "required" in result.lower()

    def test_invalid_service_name_returns_error(self):
        """Test that invalid service name returns error."""
        mock_handler = MagicMock(return_value="success")
        decorated = require_valid_service(mock_handler)

        result = decorated({"service": "123-invalid"})

        mock_handler.assert_not_called()
        assert "invalid" in result.lower()

    def test_service_name_with_injection_blocked(self):
        """Test that service names with injection attempts are blocked."""
        mock_handler = MagicMock(return_value="success")
        decorated = require_valid_service(mock_handler)

        # Various injection attempts
        injection_attempts = [
            "nginx; rm -rf /",
            "nginx$(whoami)",
            "nginx`id`",
            "nginx && cat /etc/passwd",
            "../../../etc/passwd",
        ]

        for attempt in injection_attempts:
            result = decorated({"service": attempt})
            mock_handler.assert_not_called()
            assert "invalid" in result.lower(), f"Should block: {attempt}"

    def test_preserves_function_metadata(self):
        """Test that decorator preserves function metadata."""
        @require_valid_service
        def my_handler(arguments):
            """Handler docstring."""
            return "result"

        assert my_handler.__name__ == "my_handler"
        assert "Handler docstring" in my_handler.__doc__

    def test_passes_all_arguments_to_handler(self):
        """Test that all arguments are passed to handler."""
        mock_handler = MagicMock(return_value="success")
        decorated = require_valid_service(mock_handler)

        arguments = {
            "service": "nginx",
            "lines": 100,
            "errors_only": True,
            "extra_param": "value",
        }

        decorated(arguments)

        mock_handler.assert_called_once_with(arguments)
