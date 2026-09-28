// Copyright (c) 2026 Second Star Cavalier LLC

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

// generateRustUtils generates the utils/ directory with mod.rs, utils.rs, and validation.rs
// for parity with Go's utils/utils.go and utils/validation.go.
func generateRustUtils(config *tactype.Config) error {
	utilsDir := filepath.Join(config.OutputDir, "utils")
	if err := os.MkdirAll(utilsDir, 0755); err != nil {
		return fmt.Errorf("failed to create utils directory: %w", err)
	}

	// Generate mod.rs
	modContent := header.SimpleHeader(consts.LangRust) + `pub mod utils;
pub mod validation;

pub use utils::*;
pub use validation::*;
`
	if err := os.WriteFile(filepath.Join(utilsDir, "mod.rs"), []byte(modContent), 0644); err != nil {
		return fmt.Errorf("failed to write mod.rs: %w", err)
	}

	// Generate utils.rs
	utilsContent := header.SimpleHeader(consts.LangRust) + `//! Shared utility functions for base64 decoding and gRPC-to-HTTP status mapping.
//!
//! Mirrors Go's utils/utils.go for cross-language parity.

use base64::{engine::general_purpose, Engine as _};

/// Decodes a base64-encoded string, automatically detecting the encoding variant.
/// Tries URL-safe, then standard encoding, with and without padding.
///
/// # Errors
/// Returns an error if all decoding variants fail.
pub fn decode_base64(input: &str) -> Result<Vec<u8>, String> {
    // Try URL-safe without padding
    if let Ok(bytes) = general_purpose::URL_SAFE_NO_PAD.decode(input) {
        return Ok(bytes);
    }

    // Try standard without padding
    if let Ok(bytes) = general_purpose::STANDARD_NO_PAD.decode(input) {
        return Ok(bytes);
    }

    // Try URL-safe with padding
    if let Ok(bytes) = general_purpose::URL_SAFE.decode(input) {
        return Ok(bytes);
    }

    // Try standard with padding
    if let Ok(bytes) = general_purpose::STANDARD.decode(input) {
        return Ok(bytes);
    }

    let mut padded = input.to_string();
    let remainder = padded.len() % 4;
    if remainder != 0 {
        padded.extend(std::iter::repeat('=').take(4 - remainder));

        if let Ok(bytes) = general_purpose::URL_SAFE.decode(&padded) {
            return Ok(bytes);
        }

        if let Ok(bytes) = general_purpose::STANDARD.decode(&padded) {
            return Ok(bytes);
        }
    }

    Err("failed to decode base64 string".to_string())
}

/// gRPC status codes.
#[allow(dead_code)]
pub mod grpc_code {
    pub const OK: i32 = 0;
    pub const CANCELLED: i32 = 1;
    pub const UNKNOWN: i32 = 2;
    pub const INVALID_ARGUMENT: i32 = 3;
    pub const DEADLINE_EXCEEDED: i32 = 4;
    pub const NOT_FOUND: i32 = 5;
    pub const ALREADY_EXISTS: i32 = 6;
    pub const PERMISSION_DENIED: i32 = 7;
    pub const RESOURCE_EXHAUSTED: i32 = 8;
    pub const FAILED_PRECONDITION: i32 = 9;
    pub const ABORTED: i32 = 10;
    pub const OUT_OF_RANGE: i32 = 11;
    pub const UNIMPLEMENTED: i32 = 12;
    pub const INTERNAL: i32 = 13;
    pub const UNAVAILABLE: i32 = 14;
    pub const DATA_LOSS: i32 = 15;
    pub const UNAUTHENTICATED: i32 = 16;
}

/// Maps a gRPC status code to an HTTP status code.
pub fn grpc_status_to_http(code: i32) -> u16 {
    match code {
        grpc_code::OK => 200,
        grpc_code::INVALID_ARGUMENT => 400,
        grpc_code::UNAUTHENTICATED => 401,
        grpc_code::PERMISSION_DENIED => 403,
        grpc_code::NOT_FOUND => 404,
        grpc_code::ALREADY_EXISTS => 409,
        grpc_code::RESOURCE_EXHAUSTED => 429,
        grpc_code::FAILED_PRECONDITION => 400,
        grpc_code::UNIMPLEMENTED => 501,
        grpc_code::UNAVAILABLE => 503,
        grpc_code::DEADLINE_EXCEEDED => 504,
        _ => 500,
    }
}
`

	if err := os.WriteFile(filepath.Join(utilsDir, "utils.rs"), []byte(utilsContent), 0644); err != nil {
		return fmt.Errorf("failed to write utils.rs: %w", err)
	}

	// Generate validation.rs
	validationContent := header.SimpleHeader(consts.LangRust) + `//! Shared validation functions for proto constraint annotations.
//!
//! Mirrors Go's utils/validation.go for cross-language parity.

use regex::Regex;
use std::sync::LazyLock;

use super::utils::decode_base64;

static EMAIL_REGEX: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^[a-zA-Z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$").unwrap()
});

static FQDN_REGEX: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$").unwrap()
});

static HOSTNAME_REGEX: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$").unwrap()
});

static SEMVER_REGEX: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$").unwrap()
});

/// Parses a URL and returns (scheme, host). Returns an error if the URL is malformed.
fn parse_url_parts(url_str: &str) -> Result<(String, String), String> {
    let scheme_end = url_str.find("://").ok_or("invalid URL: missing scheme")?;
    let scheme = &url_str[..scheme_end];
    let rest = &url_str[scheme_end + 3..];
    // Extract host (up to first / or end)
    let host = match rest.find('/') {
        Some(pos) => &rest[..pos],
        None => rest,
    };
    if host.is_empty() {
        return Err("URL must have a host".to_string());
    }
    Ok((scheme.to_string(), host.to_string()))
}

/// Validates that a URL uses the http:// scheme.
///
/// # Errors
/// Returns an error if the URL is invalid or does not use http://.
pub fn validate_assert_url_http(url_str: &str) -> Result<(), String> {
    let (scheme, _) = parse_url_parts(url_str)?;
    if scheme != "http" {
        return Err(format!("URL must use http:// scheme, got: {}://", scheme));
    }
    Ok(())
}

/// Validates that a URL uses the https:// scheme.
///
/// # Errors
/// Returns an error if the URL is invalid or does not use https://.
pub fn validate_assert_url_https(url_str: &str) -> Result<(), String> {
    let (scheme, _) = parse_url_parts(url_str)?;
    if scheme != "https" {
        return Err(format!("URL must use https:// scheme, got: {}://", scheme));
    }
    Ok(())
}

/// Validates that a URL uses either http:// or https:// scheme.
///
/// # Errors
/// Returns an error if the URL is invalid or does not use http:// or https://.
pub fn validate_assert_url_http_or_https(url_str: &str) -> Result<(), String> {
    let (scheme, _) = parse_url_parts(url_str)?;
    if scheme != "http" && scheme != "https" {
        return Err(format!("URL must use http:// or https:// scheme, got: {}://", scheme));
    }
    Ok(())
}

/// Validates that a string is a valid email address.
///
/// # Errors
/// Returns an error if the email address is invalid.
pub fn validate_assert_email(email: &str) -> Result<(), String> {
    if !EMAIL_REGEX.is_match(email) {
        return Err("invalid email address format".to_string());
    }
    Ok(())
}

/// Validates that a string is a valid fully qualified domain name.
///
/// # Errors
/// Returns an error if the FQDN is invalid.
pub fn validate_assert_fqdn(fqdn: &str) -> Result<(), String> {
    if !FQDN_REGEX.is_match(fqdn) {
        return Err("invalid FQDN format".to_string());
    }
    Ok(())
}

/// Validates that a string is a valid listen address (host:port format).
///
/// # Errors
/// Returns an error if the listen address is invalid.
pub fn validate_assert_listen_addr(addr: &str) -> Result<(), String> {
    if !addr.contains(':') {
        return Err("invalid host:port format".to_string());
    }

    let (host, port_str) = if addr.starts_with('[') {
        // IPv6: [::1]:8080
        let bracket_end = addr.rfind(']').ok_or("invalid host:port format")?;
        let host = &addr[1..bracket_end];
        let port_str = &addr[bracket_end + 2..]; // skip ]:
        (host, port_str)
    } else {
        let last_colon = addr.rfind(':').ok_or("invalid host:port format")?;
        (&addr[..last_colon], &addr[last_colon + 1..])
    };

    // Validate host
    if !host.is_empty() && host != "*" {
        let is_ip = host.parse::<std::net::IpAddr>().is_ok();
        if !is_ip && !HOSTNAME_REGEX.is_match(host) {
            return Err("invalid host: must be IP address, hostname, or empty".to_string());
        }
    }

    // Validate port
    let port: i32 = port_str.parse().map_err(|_| {
        format!("invalid port: must be 0-65535, got: {}", port_str)
    })?;
    if port < 0 || port > 65535 {
        return Err(format!("invalid port: must be 0-65535, got: {}", port_str));
    }

    Ok(())
}

/// Validates that a string is valid base64 encoding.
///
/// # Errors
/// Returns an error if the string is not valid base64.
pub fn validate_assert_base64(s: &str) -> Result<(), String> {
    decode_base64(s).map_err(|_| "invalid base64 encoding".to_string())?;
    Ok(())
}

/// Validates that a string follows semantic versioning.
/// Accepts formats like "1.0.0", "v2.3.4", "1.0.0-alpha.1", "1.0.0+build.123".
///
/// # Errors
/// Returns an error if the version string is not valid semver.
pub fn validate_assert_semver(version: &str) -> Result<(), String> {
    if !SEMVER_REGEX.is_match(version) {
        return Err(format!("invalid semantic version format: {}", version));
    }
    Ok(())
}
`

	if err := os.WriteFile(filepath.Join(utilsDir, "validation.rs"), []byte(validationContent), 0644); err != nil {
		return fmt.Errorf("failed to write validation.rs: %w", err)
	}

	zap.L().Info("Generated Rust utils directory",
		zap.String("path", utilsDir))
	return nil
}
