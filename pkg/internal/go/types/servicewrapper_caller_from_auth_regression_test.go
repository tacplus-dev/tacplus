// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

// The bug this guards (Fable audit finding #5): the wrapper used to build the
// caller from servicemanager.TokenFromContext(ctx) — a context key written only
// by AuthUnaryInterceptor/AuthStreamInterceptor, neither of which is ever
// installed on the generated grpc.Server (auth is per-RPC via the Authorizer).
// The generated source read as correct ("if tok := ...; tok != nil") while the
// condition was never true, so every Go handler received a nil caller and every
// accounting identityMap was empty.
//
// The invariant: on an auth-bearing RPC the caller must be built from the token
// the authorize/authenticate call that just ran RETURNED. Reading identity from
// anywhere else is how the bug happened, so no generated wrapper may mention
// TokenFromContext at all.

func callerAuthMethods() []struct {
	name   string
	svc    *parser.ServiceInfo
	method *parser.MethodInfo
} {
	base := func(name string) *parser.MethodInfo {
		return &parser.MethodInfo{
			Name:           name,
			InputType:      "ContractRequest",
			OutputType:     "ContractResponse",
			FullInputType:  "contractclient.ContractRequest",
			FullOutputType: "contractclient.ContractResponse",
		}
	}
	svc := &parser.ServiceInfo{Name: "ContractApi"}

	unary := base("Read")

	serverStream := base("StreamEvents")
	serverStream.ServerStreaming = true

	clientStream := base("SendEvents")
	clientStream.ClientStreaming = true

	// A client-streaming method with a target_namespace_field authorizes each
	// inbound message, so its stream-open check is authenticate-only — a
	// separate code path that still has to produce the caller.
	perMessage := base("SendScopedEvents")
	perMessage.ClientStreaming = true
	perMessage.TargetNamespaceField = "tenant_namespace"

	bidi := base("AssistEvents")
	bidi.ClientStreaming = true
	bidi.ServerStreaming = true

	anyUser := base("AnyUserRead")
	anyUser.AuthAnyUser = true

	anyUserFixedNs := base("AnyUserScopedRead")
	anyUserFixedNs.AuthAnyUser = true
	anyUserFixedNs.TargetNamespaceField = "/tenants/acme"

	tier2 := base("ListItems")
	tier2.ServerStreaming = true
	tier2.Resolved = &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierStream, MaxItems: 1000, OrderBy: []string{"id"}}

	tier3 := base("CursorItems")
	tier3.ServerStreaming = true
	tier3.Resolved = &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierCursor, MaxItems: 1000, OrderBy: []string{"id"}}

	watch := &parser.MethodInfo{
		Name:            "Watch",
		InputType:       "WatchReq",
		OutputType:      "WatchEvent",
		FullInputType:   "contractclient.WatchReq",
		FullOutputType:  "contractclient.WatchEvent",
		ServerStreaming: true,
		Resolved: &collection.Resolved{
			Mode: "WATCH", ReplayDepth: 1024, IdleSecs: 300, OrderBy: []string{"id"},
			WatchOneOf: "event", WatchItemType: "contractclient.Item", WatchChangeType: "contractclient.WatchChange",
			WatchRevisionField: "revision",
		},
	}

	page := &parser.MethodInfo{
		Name:           "ListPaged",
		InputType:      "ListPagedRequest",
		OutputType:     "ListPagedPage",
		FullInputType:  "contractclient.ListPagedRequest",
		FullOutputType: "contractclient.ListPagedPage",
		Resolved: &collection.Resolved{
			Mode: "PAGE", OrderBy: []string{"id"},
			DefaultPageSize: 50, MaxPageSize: 10000,
			PageItemType: "contractclient.Event", PageItemsField: "items", PageHasTotal: true,
		},
	}
	pageAnyUser := *page
	pageAnyUser.Name = "ListPagedAnyUser"
	pageAnyUser.AuthAnyUser = true

	return []struct {
		name   string
		svc    *parser.ServiceInfo
		method *parser.MethodInfo
	}{
		{"unary", svc, unary},
		{"server streaming", svc, serverStream},
		{"client streaming", svc, clientStream},
		{"client streaming per-message auth", svc, perMessage},
		{"bidi streaming", svc, bidi},
		{"auth_any_user", svc, anyUser},
		{"auth_any_user fixed namespace", svc, anyUserFixedNs},
		{"tier-2 snapshot collection", svc, tier2},
		{"tier-3 snapshot cursor", svc, tier3},
		{"watch collection", svc, watch},
		{"page collection", svc, page},
		{"page collection auth_any_user", svc, &pageAnyUser},
	}
}

func TestGoWrapperBuildsCallerFromTheVerifiedToken(t *testing.T) {
	for _, tc := range callerAuthMethods() {
		t.Run(tc.name, func(t *testing.T) {
			rendered := generateGRPCWrapper(
				tc.svc, tc.method, "contractclient", "contract", "contract",
				map[string]service.AccountingInfo{}, map[string]string{},
				proto.SkippedTraceTypes{}, nil,
			)

			// The auth check must BIND the token it verified...
			bind := strings.Index(rendered, "authTok, err := p.auth")
			if bind < 0 {
				t.Fatalf("auth check must capture the verified token as authTok:\n%s", rendered)
			}
			// ...and the caller must be built from that same binding, after it.
			build := strings.Index(rendered, "servicemanager.NewCaller(authTok, requestID)")
			if build < 0 {
				t.Fatalf("caller must be built from the token the auth check returned:\n%s", rendered)
			}
			if build < bind {
				t.Fatalf("caller is built before the auth check that produces its token:\n%s", rendered)
			}
			// The dead context key is how the bug hid; it must not come back.
			if strings.Contains(rendered, "TokenFromContext") {
				t.Fatalf("caller must not be read from a context key no auth path writes:\n%s", rendered)
			}
		})
	}
}

// The inverse: an auth-disabled RPC verifies no token at all, so it must hand
// the handler no caller — and must not reach for one from anywhere.
func TestGoWrapperAuthDisabledHasNoCaller(t *testing.T) {
	for _, tc := range callerAuthMethods() {
		t.Run(tc.name, func(t *testing.T) {
			svc := *tc.svc
			svc.AuthDisabled = true
			method := *tc.method
			method.AuthDisabled = true

			rendered := generateGRPCWrapper(
				&svc, &method, "contractclient", "contract", "contract",
				map[string]service.AccountingInfo{}, map[string]string{},
				proto.SkippedTraceTypes{}, nil,
			)

			if !strings.Contains(rendered, "var caller *servicemanager.Caller") {
				t.Fatalf("auth-disabled RPC must still declare the (nil) caller:\n%s", rendered)
			}
			for _, forbidden := range []string{"servicemanager.NewCaller(", "TokenFromContext", "authTok"} {
				if strings.Contains(rendered, forbidden) {
					t.Fatalf("auth-disabled RPC must not produce a caller (found %q):\n%s", forbidden, rendered)
				}
			}
		})
	}
}
