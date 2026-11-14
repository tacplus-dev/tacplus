// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	protogen "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
)

// memberPortProtoDir is a flat proto dir with the sys protos and one service of
// package grid that declares (spec.member_port).
func memberPortProtoDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := t.TempDir()
	if err := protogen.CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "svc"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := `syntax = "proto3";
package grid;
import "sys/spec.proto";
message Ping { string m = 1; }
service GridService {
  option (spec.service_s2s) = {label: "grid"};
  option (spec.member_port) = {};
  rpc Get(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["*"]};
  }
}
`
	if err := os.WriteFile(filepath.Join(dir, "svc", "grid.proto"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(dir, dir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}
	return dir
}

// Rust generates the member port (tacplus#322) instead of refusing the
// annotation, and keeps Java's rule that it needs auto_tls: the port serves
// TLS with the member's serving certificate and never falls back to plaintext.
func TestRustDetectMemberPort(t *testing.T) {
	dir := memberPortProtoDir(t)
	withTLS := &tactype.Config{
		SelfPackage:  "grid",
		FlatProtoDir: dir,
		States:       []tactype.StateEntry{{MessageName: "GridState", AutoTlsKeyType: "ed25519"}},
	}
	got, err := detectMemberPort(withTLS)
	if err != nil {
		t.Fatalf("detectMemberPort: %v", err)
	}
	if !got {
		t.Fatal("grid declares (spec.member_port): the ServiceManager must own a member port")
	}

	other := &tactype.Config{SelfPackage: "other", FlatProtoDir: dir}
	if got, err := detectMemberPort(other); err != nil || got {
		t.Fatalf("another package's member port is not this service's: got %v, %v", got, err)
	}

	noTLS := &tactype.Config{SelfPackage: "grid", FlatProtoDir: dir}
	if _, err := detectMemberPort(noTLS); err == nil ||
		!strings.Contains(err.Error(), "declare auto_tls on its (spec.service_state)") {
		t.Fatalf("a member port without auto_tls must fail generation, got %v", err)
	}
}

// The ServiceManager owns the member port: the config is checked before any
// retry loop, the port starts after the first auto-TLS delivery with the
// identity manager's member:<svc> token and the watched JWKS, is reachable
// through member_port(), and is closed on shutdown.
func TestRustServiceManagerMemberPortWiring(t *testing.T) {
	data := smTemplateData{
		ServiceLabel:        "grid",
		StateStoreServiceID: "statestore",
		HasGrpc:             true,
		HasTlsGrpc:          true,
		HasAutoTls:          true,
		AutoTlsKeyType:      "ed25519",
		HasMemberPort:       true,
	}
	rendered := renderRustServiceManagerForTest(t, data)
	order := []string{
		"config_defaults(&mut cfg);",
		"member_port_addresses(&cfg).await?;",
		"IdentityManager::new(channel, &cfg)",
		"match tls_ready_rx.await",
		"let member_jwks = authorizer.jwk_cache().clone();",
		"start_member_port(&cfg, identity.clone(), member_jwks, tls_server_certified_key_arc.clone())",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(rendered[at:], want)
		if i < 0 {
			t.Fatalf("ServiceManager::new is missing %q after the previous step", want)
		}
		at += i + len(want)
	}
	for _, want := range []string{
		`memberport::audience_for("grid")`,
		"im.get_token_for(&aud)",
		"pub fn member_port(&self) -> &memberport::MemberPort",
		"member_port: memberport::MemberPort,",
		"must be a loopback address",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("ServiceManager missing %q", want)
		}
	}
	if n := strings.Count(rendered, "self.member_port.close();"); n != 2 {
		t.Fatalf("the member port must be closed by close() and at the end of run(), found %d closes", n)
	}

	data.HasMemberPort = false
	if strings.Contains(renderRustServiceManagerForTest(t, data), "member_port") {
		t.Fatal("a service without (spec.member_port) must not get a member port")
	}
}

// The chassis member port serves the tunnel by hand on its own TLS 1.3
// listener and resets a dropped session's loopback connection.
func TestRustMemberPortChassisShape(t *testing.T) {
	read := func(name string) string {
		b, err := templates.FS.ReadFile("rust/chassis/memberport/src/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(b)
	}
	memberport, tunnel, tls, session := read("memberport.rs.tmpl"), read("tunnel.rs.tmpl"), read("tls.rs.tmpl"), read("session.rs.tmpl")
	for _, c := range []struct{ src, want, why string }{
		{memberport, `pub const OPEN_PATH: &str = "/memberport.MemberTunnel/Open";`, "the tunnel's one method"},
		{memberport, `pub const TOKEN_HEADER: &str = "tacplus-member-token";`, "the receiver's token header"},
		{memberport, "pub const MAX_PENDING_ADMISSIONS: usize = 256;", "bounded admissions"},
		{tls, "with_protocol_versions(&[&rustls::version::TLS13])", "TLS 1.3 only"},
		{tls, "expect_service(service_label)", "the receiver's certificate names this service"},
		{tunnel, "impl tonic::server::NamedService for TunnelService", "a hand-built tunnel service"},
		{tunnel, `Status::new(Code::PermissionDenied, "")`, "refusals carry no description"},
		{memberport, "s.set_linger(Some(Duration::ZERO))", "a dropped session resets its loopback connection"},
		{session, "zero_linger(local.as_ref())", "the reader resets the local connection of a dropped session"},
	} {
		if !strings.Contains(c.src, c.want) {
			t.Errorf("%s: missing %q", c.why, c.want)
		}
	}
}
