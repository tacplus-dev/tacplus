// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"
)

// Pins descriptor-based (spec.token_minter) discovery (WS-D3 / OF-17a). The
// previous implementation single-line text-scanned the proto, so a multiline
// `(spec.token_minter) = { proxy: true }` was misclassified as AUTHORITATIVE
// (proxy lived on a different line than the option keyword) and pointed every
// service's identity bootstrap at the C2S proxy. Reading GetProxy() from the
// parsed descriptor classifies correctly regardless of formatting.

// tokenMinterDir writes a proto using spec annotations into a temp dir
// (alongside a copy of spec.proto) and returns the dir for the Discover* /
// scan* / Validate* entry points (which take a protoc-resolvable proto dir).
func tokenMinterDir(t *testing.T, protoBody string) string {
	t.Helper()
	dir := t.TempDir()
	copySpecProto(t, dir)
	content := `syntax = "proto3";

package minttest;

import "sys/spec.proto";

message Ping { string m = 1; }
` + protoBody
	writeTempProtoFile(t, dir, "minttest.proto", content)
	return dir
}

func TestDiscoverTokenMinter_MultilineProxyClassifiedCorrectly(t *testing.T) {
	skipIfNoProtoc(t)

	// The proxy uses a MULTILINE option block — the case the old text scan got
	// wrong. The authoritative uses a bare {}.
	dir := tokenMinterDir(t, `
service AuthMint {
  option (spec.token_minter) = {};
  rpc Mint(Ping) returns (Ping);
}

service ProxyMint {
  option (spec.token_minter) = {
    proxy: true
  };
  rpc Mint(Ping) returns (Ping);
}
`)

	auth, err := DiscoverTokenExchangerPackage(dir)
	if err != nil {
		t.Fatalf("DiscoverTokenExchangerPackage: %v", err)
	}
	if auth == nil {
		t.Fatal("expected an authoritative minter, got nil")
	}
	if auth.ServiceName != "AuthMint" || auth.IsProxy {
		t.Errorf("authoritative minter = %+v, want AuthMint with IsProxy=false", auth)
	}
	if auth.Package != "minttest" {
		t.Errorf("authoritative minter package = %q, want minttest", auth.Package)
	}
	if auth.Dir != "minttest" {
		t.Errorf("authoritative minter dir = %q, want minttest", auth.Dir)
	}

	client, err := DiscoverClientTokenExchangerPackage(dir)
	if err != nil {
		t.Fatalf("DiscoverClientTokenExchangerPackage: %v", err)
	}
	if client == nil || client.ServiceName != "ProxyMint" || !client.IsProxy {
		t.Errorf("client minter = %+v, want ProxyMint with IsProxy=true (multiline proxy must NOT be promoted to authoritative)", client)
	}
}

func TestDiscoverTokenMinter_RecordsExchangeMethodNames(t *testing.T) {
	skipIfNoProtoc(t)

	dir := tokenMinterDir(t, `
message TokenMintRequest {}
message MintServiceTokenRequest {}
message MintExternalServiceTokenRequest {}
message TokenMintResponse {}
message X509MintRequest {}
message X509CertChain {}

service AuthMint {
  option (spec.token_minter) = {};
  rpc MintServiceToken(MintServiceTokenRequest) returns (TokenMintResponse);
  rpc MintExternalServiceToken(MintExternalServiceTokenRequest) returns (TokenMintResponse);
  rpc MintX509Cert(X509MintRequest) returns (X509CertChain);
}

service ProxyMint {
  option (spec.token_minter) = { proxy: true };
  rpc MintToken(TokenMintRequest) returns (TokenMintResponse);
}
`)

	auth, err := DiscoverTokenExchangerPackage(dir)
	if err != nil {
		t.Fatalf("DiscoverTokenExchangerPackage: %v", err)
	}
	if auth == nil {
		t.Fatal("expected authoritative minter, got nil")
	}
	if auth.MintServiceTokenMethod != "MintServiceToken" {
		t.Errorf("authoritative MintServiceTokenMethod = %q, want MintServiceToken", auth.MintServiceTokenMethod)
	}
	if auth.MintExternalServiceTokenMethod != "MintExternalServiceToken" {
		t.Errorf("authoritative MintExternalServiceTokenMethod = %q, want MintExternalServiceToken", auth.MintExternalServiceTokenMethod)
	}
	if auth.X509MintMethod != "MintX509Cert" {
		t.Errorf("authoritative X509MintMethod = %q, want MintX509Cert", auth.X509MintMethod)
	}

	client, err := DiscoverClientTokenExchangerPackage(dir)
	if err != nil {
		t.Fatalf("DiscoverClientTokenExchangerPackage: %v", err)
	}
	if client == nil {
		t.Fatal("expected client minter, got nil")
	}
	if client.TokenMintMethod != "MintToken" {
		t.Errorf("client TokenMintMethod = %q, want MintToken", client.TokenMintMethod)
	}
	if client.MintExternalServiceTokenMethod != "" {
		t.Errorf("client MintExternalServiceTokenMethod = %q, want empty (ProxyMint declares none)", client.MintExternalServiceTokenMethod)
	}
}

func TestScanTokenMinters_MultipleAuthoritativeIsError(t *testing.T) {
	skipIfNoProtoc(t)

	dir := tokenMinterDir(t, `
service MintA { option (spec.token_minter) = {}; rpc Mint(Ping) returns (Ping); }
service MintB { option (spec.token_minter) = {}; rpc Mint(Ping) returns (Ping); }
`)
	if _, err := scanTokenMinters(dir); err == nil {
		t.Fatal("expected error for two authoritative minters, got nil")
	} else if !strings.Contains(err.Error(), "multiple authoritative") {
		t.Fatalf("error %q does not mention multiple authoritative minters", err.Error())
	}
}

func TestValidateTokenMinters(t *testing.T) {
	skipIfNoProtoc(t)

	cases := []struct {
		name      string
		body      string
		s2s       bool
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "s2s enabled with one authoritative minter is valid",
			body:    `service Mint { option (spec.token_minter) = {}; rpc Mint(Ping) returns (Ping); }`,
			s2s:     true,
			wantErr: false,
		},
		{
			name:      "s2s enabled with no authoritative minter fails",
			body:      `service ProxyOnly { option (spec.token_minter) = { proxy: true }; rpc Mint(Ping) returns (Ping); }`,
			s2s:       true,
			wantErr:   true,
			errSubstr: "no authoritative",
		},
		{
			name:    "no s2s surface needs no minter",
			body:    `service Plain { rpc Get(Ping) returns (Ping); }`,
			s2s:     false,
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tokenMinterDir(t, tc.body)
			err := ValidateTokenMinters(dir, tc.s2s)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateTokenMinters: expected error, got nil")
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.errSubstr)
				}
			} else if err != nil {
				t.Fatalf("ValidateTokenMinters: unexpected error: %v", err)
			}
		})
	}
}
