// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/tmplutil"
)

// An outgoing S2S call's bearer token must be minted for the service that
// physically answers the channel. A publisher channel is the case where a
// per-proto-package guess gets that wrong: the EventBus publish surface is a
// VIRTUAL service whose method name carries the owning record's package
// (e.g. "oauth.CredentialStateService/PublishToCreated"), while the call is
// answered by the broker (eventbus). A package-keyed audience lookup therefore
// mints for the record's own host — kernel, for a state package — and eventbus
// rejects it.
//
// go and rust already mint from the broker's TargetService at the publisher's
// own call site. java's publishers were the outlier: they dialled the broker
// channel but attached IdentityManager.callCredentials(), whose audience comes
// from audienceFromMethod (the package guess), even though the fixed-audience
// variant callCredentialsForAudience already existed and the generated
// subscribers next to them already used it.
//
// This pins the broker-audience half of the cross-language contract. The
// one-attach-point half is TestSingleOutgoingAuthorizationAttachPoint.
func TestPublisherTokenAudienceIsTheBrokerService(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dir     string
		file    string
		require []string
	}{
		{
			name: "go",
			dir:  "go/types",
			file: "service_manager.go.tmpl",
			require: []string{
				`func() (string, error) { return sm.identity.GetTokenFor("{{.TargetService}}") },`,
				`func() (string, error) { return sm.identity.GetTokenFor("{{.TracePublisherTarget}}") },`,
				`func() (string, error) { return sm.identity.GetTokenFor("{{.AccountingPublisher.TargetService}}") },`,
			},
		},
		{
			name: "rust",
			dir:  "rust/types",
			file: "service_manager.rs.tmpl",
			require: []string{
				// Business and accounting publishers each build their own
				// TokenProvider bound to the broker. The trace publisher is an
				// alias of a business publisher, so it inherits the same one.
				`im.get_token_for("{{.TargetService}}").await`,
			},
		},
		{
			name: "java",
			dir:  "java/types",
			file: "ServiceManager.java.tmpl",
			require: []string{
				`.withCallCredentials(identity.callCredentialsForAudience("{{.TargetService}}"))),`,
				`.withCallCredentials(identity.callCredentialsForAudience("{{.TracePublisherTarget}}"))),`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, err := tmplutil.LoadTemplate(tc.dir, tc.file)
			if err != nil {
				t.Fatalf("load %s/%s: %v", tc.dir, tc.file, err)
			}
			for _, r := range tc.require {
				if !strings.Contains(src, r) {
					t.Errorf("%s/%s lost %q — a publisher must mint its token for the "+
						"broker's target service, not from the publish method's proto package",
						tc.dir, tc.file, r)
				}
			}
		})
	}

	// Every java publisher stream dials the resolved broker channel as
	// `ebConn.newCall(...)`. None of them may take the method-name-derived
	// credentials: that is the package guess this test exists to keep out.
	src, err := tmplutil.LoadTemplate("java/types", "ServiceManager.java.tmpl")
	if err != nil {
		t.Fatalf("load java/types/ServiceManager.java.tmpl: %v", err)
	}
	const brokerCall = "ebConn.newCall(method, io.grpc.CallOptions.DEFAULT\n" +
		"                        .withCallCredentials(identity.callCredentials()))"
	if strings.Contains(src, brokerCall) {
		t.Errorf("java publisher dials the broker with identity.callCredentials() — its audience " +
			"comes from audienceFromMethod, which sees the owning record's proto package, not the " +
			"broker; use callCredentialsForAudience(<broker target>) as the subscribers do")
	}
}

// The second half of the contract: there is exactly ONE place a bearer token is
// attached to an outgoing call, so a correctly-audienced token can never be
// overwritten or duplicated by a second, guessing layer.
//
// This is the invariant the 2026-09-27 aud=kernel bug broke in go: a blanket
// per-channel Unary/StreamInterceptor re-minted from audienceFromMethod and
// attached a SECOND authorization value on top of the publisher's own, and
// grpc-go applies the dial-time stream interceptor AFTER the caller's metadata,
// so the guess won. Each language holds the invariant differently, so each gets
// the assertion its own architecture needs.
func TestSingleOutgoingAuthorizationAttachPoint(t *testing.T) {
	// go: the interceptors remain, so they must stand down when the caller has
	// already attached its own token.
	src, err := tmplutil.LoadTemplate("go/chassis/service_im", "service_im.go.tmpl")
	if err != nil {
		t.Fatalf("load go/chassis/service_im/service_im.go.tmpl: %v", err)
	}
	if !strings.Contains(src, "func hasOutgoingAuthorization(ctx context.Context) bool") {
		t.Fatalf("go service_im lost hasOutgoingAuthorization")
	}
	for _, fn := range []string{"UnaryInterceptor", "StreamInterceptor"} {
		start := strings.Index(src, "func (im *IdentityManager) "+fn+"(")
		if start < 0 {
			t.Fatalf("go service_im lost %s", fn)
		}
		end := strings.Index(src[start:], "\n}\n")
		if end < 0 {
			t.Fatalf("could not find end of go %s body", fn)
		}
		if !strings.Contains(src[start:start+end], "hasOutgoingAuthorization(ctx)") {
			t.Errorf("go %s mints without checking hasOutgoingAuthorization — it would append a "+
				"second, heuristically-audienced token on top of one the caller already set", fn)
		}
	}

	// rust: there is no interceptor layer at all. Every outgoing token is
	// attached at the call site with MetadataMap::insert, which REPLACES the
	// key. `append` would accumulate values and reintroduce the two-token
	// ambiguity, so it must not appear.
	rustSrc, err := tmplutil.LoadTemplate("rust/types", "service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("load rust/types/service_manager.rs.tmpl: %v", err)
	}
	if !strings.Contains(rustSrc, `req.metadata_mut().insert("authorization", token_meta);`) {
		t.Errorf("rust lost the insert-based authorization attachment")
	}
	for _, banned := range []string{
		`metadata_mut().append("authorization"`,
		// A client interceptor would be a second attach point layered over the
		// per-call inserts — the exact shape of the go bug.
		"with_interceptor(",
	} {
		if strings.Contains(rustSrc, banned) {
			t.Errorf("rust/types/service_manager.rs.tmpl must not use %q: outgoing tokens are "+
				"attached once per call, keyed to the channel's target service", banned)
		}
	}

	// java: CallCredentials is the single attach point — withCallCredentials
	// replaces rather than accumulates, so one call carries one token. The
	// generated typed client must not attach an authorization header of its own
	// on top of it.
	clientSrc, err := tmplutil.LoadTemplate("java/types", "grpcclient.java.tmpl")
	if err != nil {
		t.Fatalf("load java/types/grpcclient.java.tmpl: %v", err)
	}
	for _, banned := range []string{
		".put(AUTH_KEY",
		"MetadataUtils.attachHeaders",
	} {
		if strings.Contains(clientSrc, banned) {
			t.Errorf("java/types/grpcclient.java.tmpl must not use %q: the channel's "+
				"CallCredentials is the one place an outgoing token is attached, and a header "+
				"set here would compete with it", banned)
		}
	}
}
