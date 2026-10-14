// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"

	"go.uber.org/zap"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// generatePythonUtils generates the utils/ directory with utils.py and validation.py
// for parity with Go's utils/utils.go and utils/validation.go.
func generatePythonUtils(config *tactype.Config) error {
	utilsDir := filepath.Join(config.OutputDir, "utils")
	if err := os.MkdirAll(utilsDir, 0755); err != nil {
		return fmt.Errorf("failed to create utils directory: %w", err)
	}

	// Generate __init__.py
	initContent := header.SimpleHeader(consts.LangPython) + `from .utils import decode_base64, grpc_status_to_http
from .validation import (
    validate_assert_url_http,
    validate_assert_url_https,
    validate_assert_url_http_or_https,
    validate_assert_email,
    validate_assert_fqdn,
    validate_assert_listen_addr,
    validate_assert_base64,
    validate_assert_semver,
)
`
	if err := os.WriteFile(filepath.Join(utilsDir, "__init__.py"), []byte(initContent), 0644); err != nil {
		return fmt.Errorf("failed to write __init__.py: %w", err)
	}

	// Generate utils.py
	utilsContent := header.SimpleHeader(consts.LangPython) + `"""Shared utility functions for base64 decoding and gRPC-to-HTTP status mapping.

Mirrors Go's utils/utils.go for cross-language parity.
"""

import base64
from typing import Optional


def decode_base64(input_str: str) -> bytes:
    """Decode a base64-encoded string, automatically detecting the encoding variant.

    Tries URL-safe, then standard encoding, with and without padding.

    Args:
        input_str: The base64-encoded string.

    Returns:
        The decoded bytes.

    Raises:
        ValueError: If all decoding variants fail.
    """
    # Try URL-safe without padding
    try:
        return base64.urlsafe_b64decode(input_str + "=" * (-len(input_str) % 4))
    except Exception:
        pass

    # Try standard without padding
    try:
        return base64.b64decode(input_str + "=" * (-len(input_str) % 4))
    except Exception:
        pass

    # Try URL-safe as-is
    try:
        return base64.urlsafe_b64decode(input_str)
    except Exception:
        pass

    # Try standard as-is
    try:
        return base64.b64decode(input_str)
    except Exception:
        pass

    raise ValueError("failed to decode base64 string")


# gRPC status code constants
_GRPC_OK = 0
_GRPC_CANCELLED = 1
_GRPC_UNKNOWN = 2
_GRPC_INVALID_ARGUMENT = 3
_GRPC_DEADLINE_EXCEEDED = 4
_GRPC_NOT_FOUND = 5
_GRPC_ALREADY_EXISTS = 6
_GRPC_PERMISSION_DENIED = 7
_GRPC_RESOURCE_EXHAUSTED = 8
_GRPC_FAILED_PRECONDITION = 9
_GRPC_ABORTED = 10
_GRPC_OUT_OF_RANGE = 11
_GRPC_UNIMPLEMENTED = 12
_GRPC_INTERNAL = 13
_GRPC_UNAVAILABLE = 14
_GRPC_DATA_LOSS = 15
_GRPC_UNAUTHENTICATED = 16

# Mapping from gRPC status codes to HTTP status codes
_GRPC_TO_HTTP: dict[int, int] = {
    _GRPC_OK: 200,
    _GRPC_INVALID_ARGUMENT: 400,
    _GRPC_UNAUTHENTICATED: 401,
    _GRPC_PERMISSION_DENIED: 403,
    _GRPC_NOT_FOUND: 404,
    _GRPC_ALREADY_EXISTS: 409,
    _GRPC_RESOURCE_EXHAUSTED: 429,
    _GRPC_FAILED_PRECONDITION: 400,
    _GRPC_UNIMPLEMENTED: 501,
    _GRPC_UNAVAILABLE: 503,
    _GRPC_DEADLINE_EXCEEDED: 504,
}


def grpc_status_to_http(code: Optional[int] = None) -> int:
    """Map a gRPC status code to an HTTP status code.

    Args:
        code: The gRPC status code. If None, returns 500.

    Returns:
        The corresponding HTTP status code.
    """
    if code is None:
        return 500
    return _GRPC_TO_HTTP.get(code, 500)
`
	if err := os.WriteFile(filepath.Join(utilsDir, "utils.py"), []byte(utilsContent), 0644); err != nil {
		return fmt.Errorf("failed to write utils.py: %w", err)
	}

	// Generate validation.py
	validationContent := header.SimpleHeader(consts.LangPython) + `"""Shared validation functions for proto constraint annotations.

Mirrors Go's utils/validation.go for cross-language parity.
"""

import re
from urllib.parse import urlparse


# Compiled regex patterns
_EMAIL_REGEX = re.compile(
    r"^[a-zA-Z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-zA-Z0-9]"
    r"(?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?"
    r"(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$"
)

_FQDN_REGEX = re.compile(
    r"^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$"
)

_HOSTNAME_REGEX = re.compile(
    r"^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?"
    r"(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$"
)

_SEMVER_REGEX = re.compile(
    r"^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)"
    r"(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)"
    r"(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?"
    r"(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$"
)


def validate_assert_url_http(url_str: str) -> None:
    """Validate that a URL uses the http:// scheme.

    Args:
        url_str: The URL string to validate.

    Raises:
        ValueError: If the URL is invalid or does not use http://.
    """
    u = urlparse(url_str)
    if u.scheme != "http":
        raise ValueError(f"URL must use http:// scheme, got: {u.scheme}://")
    if not u.hostname:
        raise ValueError("URL must have a host")


def validate_assert_url_https(url_str: str) -> None:
    """Validate that a URL uses the https:// scheme.

    Args:
        url_str: The URL string to validate.

    Raises:
        ValueError: If the URL is invalid or does not use https://.
    """
    u = urlparse(url_str)
    if u.scheme != "https":
        raise ValueError(f"URL must use https:// scheme, got: {u.scheme}://")
    if not u.hostname:
        raise ValueError("URL must have a host")


def validate_assert_url_http_or_https(url_str: str) -> None:
    """Validate that a URL uses either http:// or https:// scheme.

    Args:
        url_str: The URL string to validate.

    Raises:
        ValueError: If the URL is invalid or does not use http:// or https://.
    """
    u = urlparse(url_str)
    if u.scheme not in ("http", "https"):
        raise ValueError(
            f"URL must use http:// or https:// scheme, got: {u.scheme}://"
        )
    if not u.hostname:
        raise ValueError("URL must have a host")


def validate_assert_email(email: str) -> None:
    """Validate that a string is a valid email address.

    Args:
        email: The email address to validate.

    Raises:
        ValueError: If the email address is invalid.
    """
    if not _EMAIL_REGEX.match(email):
        raise ValueError("invalid email address format")


def validate_assert_fqdn(fqdn: str) -> None:
    """Validate that a string is a valid fully qualified domain name.

    Args:
        fqdn: The FQDN to validate.

    Raises:
        ValueError: If the FQDN is invalid.
    """
    if not _FQDN_REGEX.match(fqdn):
        raise ValueError("invalid FQDN format")


def validate_assert_listen_addr(addr: str) -> None:
    """Validate that a string is a valid listen address (host:port format).

    Args:
        addr: The listen address to validate.

    Raises:
        ValueError: If the listen address is invalid.
    """
    if ":" not in addr:
        raise ValueError("invalid host:port format")

    # Handle IPv6 addresses like [::1]:8080
    if addr.startswith("["):
        bracket_end = addr.rfind("]")
        if bracket_end < 0:
            raise ValueError("invalid host:port format")
        host = addr[1:bracket_end]
        port_str = addr[bracket_end + 2:]  # skip ]:
    else:
        last_colon = addr.rfind(":")
        host = addr[:last_colon]
        port_str = addr[last_colon + 1:]

    # Validate host (can be IP, hostname, or empty for any)
    if host and host != "*":
        import ipaddress

        try:
            ipaddress.ip_address(host)
        except ValueError:
            if not _HOSTNAME_REGEX.match(host):
                raise ValueError(
                    "invalid host: must be IP address, hostname, or empty"
                )

    # Validate port
    try:
        port = int(port_str)
    except ValueError:
        raise ValueError(f"invalid port: must be 0-65535, got: {port_str}")
    if port < 0 or port > 65535:
        raise ValueError(f"invalid port: must be 0-65535, got: {port_str}")


def validate_assert_base64(s: str) -> None:
    """Validate that a string is valid base64 encoding.

    Args:
        s: The string to validate.

    Raises:
        ValueError: If the string is not valid base64.
    """
    from .utils import decode_base64

    try:
        decode_base64(s)
    except ValueError:
        raise ValueError("invalid base64 encoding")


def validate_assert_semver(version: str) -> None:
    """Validate that a string follows semantic versioning.

    Accepts formats like "1.0.0", "v2.3.4", "1.0.0-alpha.1", "1.0.0+build.123".

    Args:
        version: The version string to validate.

    Raises:
        ValueError: If the version string is not valid semver.
    """
    if not _SEMVER_REGEX.match(version):
        raise ValueError(f"invalid semantic version format: {version}")
`
	if err := os.WriteFile(filepath.Join(utilsDir, "validation.py"), []byte(validationContent), 0644); err != nil {
		return fmt.Errorf("failed to write validation.py: %w", err)
	}

	zap.L().Info("Generated Python utils directory",
		zap.String("path", utilsDir))
	return nil
}
