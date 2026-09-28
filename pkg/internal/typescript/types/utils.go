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

// generateTypeScriptUtils generates the utils/ directory with utils.ts and validation.ts
// for parity with Go's utils/utils.go and utils/validation.go.
func generateTypeScriptUtils(config *tactype.Config) error {
	utilsDir := filepath.Join(config.OutputDir, "utils")
	if err := os.MkdirAll(utilsDir, 0755); err != nil {
		return fmt.Errorf("failed to create utils directory: %w", err)
	}

	// Generate utils.ts
	utilsContent := header.SimpleHeader(consts.LangTypeScript) + `/**
 * Shared utility functions for base64 decoding and gRPC-to-HTTP status mapping.
 * Mirrors Go's utils/utils.go for cross-language parity.
 */

/**
 * Decodes a base64-encoded string, automatically detecting the encoding variant.
 * Tries URL-safe, then standard encoding, with and without padding.
 *
 * @param input - The base64-encoded string.
 * @returns The decoded bytes as a Uint8Array.
 * @throws Error if all decoding variants fail.
 */
export function decodeBase64(input: string): Uint8Array {
  // Normalize: replace URL-safe characters with standard base64
  const standardized = input.replace(/-/g, '+').replace(/_/g, '/');

  // Add padding if necessary
  const padded = standardized + '='.repeat((4 - (standardized.length % 4)) % 4);

  try {
    const binaryString = atob(padded);
    const bytes = new Uint8Array(binaryString.length);
    for (let i = 0; i < binaryString.length; i++) {
      bytes[i] = binaryString.charCodeAt(i);
    }
    return bytes;
  } catch {
    // Try without padding modification
    try {
      const binaryString = atob(standardized);
      const bytes = new Uint8Array(binaryString.length);
      for (let i = 0; i < binaryString.length; i++) {
        bytes[i] = binaryString.charCodeAt(i);
      }
      return bytes;
    } catch {
      throw new Error('failed to decode base64 string');
    }
  }
}

/** gRPC status codes */
export const GrpcCode = {
  OK: 0,
  CANCELLED: 1,
  UNKNOWN: 2,
  INVALID_ARGUMENT: 3,
  DEADLINE_EXCEEDED: 4,
  NOT_FOUND: 5,
  ALREADY_EXISTS: 6,
  PERMISSION_DENIED: 7,
  RESOURCE_EXHAUSTED: 8,
  FAILED_PRECONDITION: 9,
  ABORTED: 10,
  OUT_OF_RANGE: 11,
  UNIMPLEMENTED: 12,
  INTERNAL: 13,
  UNAVAILABLE: 14,
  DATA_LOSS: 15,
  UNAUTHENTICATED: 16,
} as const;

/** Mapping from gRPC status codes to HTTP status codes */
const grpcToHttpMap: Record<number, number> = {
  [GrpcCode.OK]: 200,
  [GrpcCode.INVALID_ARGUMENT]: 400,
  [GrpcCode.UNAUTHENTICATED]: 401,
  [GrpcCode.PERMISSION_DENIED]: 403,
  [GrpcCode.NOT_FOUND]: 404,
  [GrpcCode.ALREADY_EXISTS]: 409,
  [GrpcCode.RESOURCE_EXHAUSTED]: 429,
  [GrpcCode.FAILED_PRECONDITION]: 400,
  [GrpcCode.UNIMPLEMENTED]: 501,
  [GrpcCode.UNAVAILABLE]: 503,
  [GrpcCode.DEADLINE_EXCEEDED]: 504,
};

/**
 * Maps a gRPC status code to an HTTP status code.
 *
 * @param code - The gRPC status code.
 * @returns The corresponding HTTP status code.
 */
export function grpcStatusToHttp(code: number): number {
  return grpcToHttpMap[code] ?? 500;
}
`

	if err := os.WriteFile(filepath.Join(utilsDir, "utils.ts"), []byte(utilsContent), 0644); err != nil {
		return fmt.Errorf("failed to write utils.ts: %w", err)
	}

	// Generate validation.ts
	validationContent := header.SimpleHeader(consts.LangTypeScript) + `/**
 * Shared validation functions for proto constraint annotations.
 * Mirrors Go's utils/validation.go for cross-language parity.
 */

import { decodeBase64 } from './utils.js';

/**
 * Validates that a URL uses the http:// scheme.
 * @param urlStr - The URL string to validate.
 * @throws Error if the URL is invalid or does not use http://.
 */
export function validateAssertUrlHttp(urlStr: string): void {
  let u: URL;
  try {
    u = new URL(urlStr);
  } catch {
    throw new Error('invalid URL');
  }
  if (u.protocol !== 'http:') {
    throw new Error(` + "`" + `URL must use http:// scheme, got: ${u.protocol}//` + "`" + `);
  }
  if (!u.hostname) {
    throw new Error('URL must have a host');
  }
}

/**
 * Validates that a URL uses the https:// scheme.
 * @param urlStr - The URL string to validate.
 * @throws Error if the URL is invalid or does not use https://.
 */
export function validateAssertUrlHttps(urlStr: string): void {
  let u: URL;
  try {
    u = new URL(urlStr);
  } catch {
    throw new Error('invalid URL');
  }
  if (u.protocol !== 'https:') {
    throw new Error(` + "`" + `URL must use https:// scheme, got: ${u.protocol}//` + "`" + `);
  }
  if (!u.hostname) {
    throw new Error('URL must have a host');
  }
}

/**
 * Validates that a URL uses either http:// or https:// scheme.
 * @param urlStr - The URL string to validate.
 * @throws Error if the URL is invalid or does not use http:// or https://.
 */
export function validateAssertUrlHttpOrHttps(urlStr: string): void {
  let u: URL;
  try {
    u = new URL(urlStr);
  } catch {
    throw new Error('invalid URL');
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') {
    throw new Error(` + "`" + `URL must use http:// or https:// scheme, got: ${u.protocol}//` + "`" + `);
  }
  if (!u.hostname) {
    throw new Error('URL must have a host');
  }
}

const EMAIL_REGEX =
  /^[a-zA-Z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$/;

/**
 * Validates that a string is a valid email address.
 * @param email - The email address to validate.
 * @throws Error if the email address is invalid.
 */
export function validateAssertEmail(email: string): void {
  if (!EMAIL_REGEX.test(email)) {
    throw new Error('invalid email address format');
  }
}

const FQDN_REGEX =
  /^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$/;

/**
 * Validates that a string is a valid fully qualified domain name.
 * @param fqdn - The FQDN to validate.
 * @throws Error if the FQDN is invalid.
 */
export function validateAssertFqdn(fqdn: string): void {
  if (!FQDN_REGEX.test(fqdn)) {
    throw new Error('invalid FQDN format');
  }
}

const HOSTNAME_REGEX =
  /^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$/;

const IPV4_REGEX = /^(\d{1,3}\.){3}\d{1,3}$/;
const IPV6_REGEX = /^[\da-fA-F:]+$/;

/**
 * Validates that a string is a valid listen address (host:port format).
 * @param addr - The listen address to validate.
 * @throws Error if the listen address is invalid.
 */
export function validateAssertListenAddr(addr: string): void {
  if (!addr.includes(':')) {
    throw new Error('invalid host:port format');
  }

  let host: string;
  let portStr: string;

  if (addr.startsWith('[')) {
    // IPv6: [::1]:8080
    const bracketEnd = addr.lastIndexOf(']');
    if (bracketEnd < 0) {
      throw new Error('invalid host:port format');
    }
    host = addr.substring(1, bracketEnd);
    portStr = addr.substring(bracketEnd + 2); // skip ]:
  } else {
    const lastColon = addr.lastIndexOf(':');
    host = addr.substring(0, lastColon);
    portStr = addr.substring(lastColon + 1);
  }

  // Validate host
  if (host && host !== '*') {
    const isIPv4 = IPV4_REGEX.test(host);
    const isIPv6 = IPV6_REGEX.test(host);
    if (!isIPv4 && !isIPv6 && !HOSTNAME_REGEX.test(host)) {
      throw new Error('invalid host: must be IP address, hostname, or empty');
    }
  }

  // Validate port
  const port = /^[+-]?[0-9]+$/.test(portStr) ? Number(portStr) : NaN;
  if (isNaN(port) || port < 0 || port > 65535) {
    throw new Error(` + "`" + `invalid port: must be 0-65535, got: ${portStr}` + "`" + `);
  }
}

/**
 * Parsed listen address with host and port components.
 */
export interface ListenAddr {
  host: string;
  port: number;
}

/**
 * Parses a "host:port" string into its host and port components.
 * Handles IPv6 addresses in bracket notation (e.g. "[::1]:8080").
 * @param addr - The address string in host:port format.
 * @returns An object with host and port properties.
 * @throws Error if the address is not in valid host:port format.
 */
export function parseListenAddr(addr: string): ListenAddr {
  let host: string;
  let portStr: string;

  if (addr.startsWith('[')) {
    const bracketEnd = addr.indexOf(']');
    if (bracketEnd < 0) {
      throw new Error(` + "`" + `invalid IPv6 host:port format: ${addr}` + "`" + `);
    }
    host = addr.substring(1, bracketEnd);
    if (bracketEnd + 1 >= addr.length || addr[bracketEnd + 1] !== ':') {
      throw new Error(` + "`" + `invalid host:port format: ${addr}` + "`" + `);
    }
    portStr = addr.substring(bracketEnd + 2);
  } else {
    const lastColon = addr.lastIndexOf(':');
    if (lastColon < 0) {
      throw new Error(` + "`" + `invalid host:port format: ${addr}` + "`" + `);
    }
    host = addr.substring(0, lastColon);
    portStr = addr.substring(lastColon + 1);
  }

  const port = /^[+-]?[0-9]+$/.test(portStr) ? Number(portStr) : NaN;
  if (isNaN(port) || port < 0 || port > 65535) {
    throw new Error(` + "`" + `port out of range (0-65535): ${portStr}` + "`" + `);
  }

  return { host, port };
}

/**
 * Validates that a string is valid base64 encoding.
 * @param s - The string to validate.
 * @throws Error if the string is not valid base64.
 */
export function validateAssertBase64(s: string): void {
  try {
    decodeBase64(s);
  } catch {
    throw new Error('invalid base64 encoding');
  }
}

const SEMVER_REGEX =
  /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$/;

/**
 * Validates that a string follows semantic versioning.
 * Accepts formats like "1.0.0", "v2.3.4", "1.0.0-alpha.1", "1.0.0+build.123".
 * @param version - The version string to validate.
 * @throws Error if the version string is not valid semver.
 */
export function validateAssertSemver(version: string): void {
  if (!SEMVER_REGEX.test(version)) {
    throw new Error(` + "`" + `invalid semantic version format: ${version}` + "`" + `);
  }
}
`

	if err := os.WriteFile(filepath.Join(utilsDir, "validation.ts"), []byte(validationContent), 0644); err != nil {
		return fmt.Errorf("failed to write validation.ts: %w", err)
	}

	// Generate index.ts for the utils package
	indexContent := header.SimpleHeader(consts.LangTypeScript) + `export * from './utils.js';
export * from './validation.js';
`
	if err := os.WriteFile(filepath.Join(utilsDir, "index.ts"), []byte(indexContent), 0644); err != nil {
		return fmt.Errorf("failed to write index.ts: %w", err)
	}

	zap.L().Info("Generated TypeScript utils directory",
		zap.String("path", utilsDir))
	return nil
}
