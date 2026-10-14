// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// rustRenderCallerWrapper renders one service's wrapper file the way the
// generator does — through generateServiceWrapper, not generateRustGRPCWrapper
// directly — because the caller splice lives in the caller loop, not in the
// per-method renderer.
func rustRenderCallerWrapper(t *testing.T, targetService *parser.ServiceInfo) string {
	t.Helper()
	protoInfo := &parser.ProtoInfo{
		Package:  "contractclient",
		Services: []*parser.ServiceInfo{targetService},
	}
	outputDir := t.TempDir()
	svc := ServiceInfo{
		ProtoFile:   "contract/api.proto",
		PackageName: "contractclient",
		ServiceName: targetService.Name,
		GrpcName:    "contract",
		IsGrpc:      true,
		IsC2S:       targetService.IsC2S,
		IsC2D:       targetService.IsC2D,
		OutputDir:   outputDir,
		ParentDir:   "contract",
		Config:      &tactype.Config{OutputDir: outputDir},
	}
	if err := generateServiceWrapper(svc, protoInfo, map[string]string{}, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, newRustEgressSanitizer(nil)); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one generated file in %s, got %v (err %v)", outputDir, entries, err)
	}
	rendered, err := os.ReadFile(filepath.Join(outputDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read generated wrapper: %v", err)
	}
	return string(rendered)
}

// rustMethodBody returns just the one method's body out of the rendered
// ValidationWrapper impl, so an assertion about one auth mode cannot be
// satisfied by a sibling method — nor by the passthrough proxy method of the
// same name that ServiceWrapper carries earlier in the same file.
func rustMethodBody(t *testing.T, rendered, methodName string) string {
	t.Helper()
	const implMarker = "// gRPC method implementations with validation for ValidationWrapper"
	implStart := strings.Index(rendered, implMarker)
	if implStart < 0 {
		t.Fatalf("rendered wrapper has no ValidationWrapper impl:\n%s", rendered)
	}
	rendered = rendered[implStart:]

	marker := "    async fn " + prostSnakeName(methodName) + "("
	start := strings.Index(rendered, marker)
	if start < 0 {
		t.Fatalf("rendered wrapper has no method %q:\n%s", methodName, rendered)
	}
	body := rendered[start:]
	if end := strings.Index(body[len(marker):], "\n    async fn "); end >= 0 {
		body = body[:len(marker)+end]
	}
	return body
}

func rustCallerTestMethod(name string) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           name,
		InputType:      "ContractRequest",
		OutputType:     "ContractResponse",
		FullInputType:  "contractclient.ContractRequest",
		FullOutputType: "contractclient.ContractResponse",
	}
}

// Fable #2: the handler-facing `caller` was spliced in unconditionally, built
// by base64-decoding the raw authorization header with token::Token::parse —
// which verifies no signature. On the auth_disabled/c2d branch rustAuthLine
// runs no authorizer at all, so nothing had ever checked that header: a client
// could present an unsigned JWT with any `sub` it liked and the handler saw
// Some(caller) with that subject. The accounting identity in the same branch
// already answered "no identity"; the handler-facing caller must answer the
// same, which is also the contract the handler trait documents ("None when
// disable_auth = true").
//
// On every other path the authorizer verified that same header (authorize_method
// / enforce_authenticated / enforce_at_or_below, each of which errors when no
// authorizer is configured) before the splice re-parses it, so extraction there
// is safe and must not regress.
func TestRustCallerExtractedOnlyWhereTheAuthorizerVerifiedTheHeader(t *testing.T) {
	authorized := &parser.ServiceInfo{
		Name:  "ContractApi",
		IsC2S: true,
		Methods: []*parser.MethodInfo{
			rustCallerTestMethod("GetContract"),
			func() *parser.MethodInfo {
				m := rustCallerTestMethod("PublicPing")
				m.AuthDisabled = true
				return m
			}(),
			func() *parser.MethodInfo {
				m := rustCallerTestMethod("ListForMe")
				m.AuthAnyUser = true
				return m
			}(),
		},
	}
	rendered := rustRenderCallerWrapper(t, authorized)

	const placeholder = "let caller: Option<servicemanager::auth::Caller> = None;"
	const unverifiedParse = "token::Token::parse"

	t.Run("auth_disabled method keeps caller None", func(t *testing.T) {
		body := rustMethodBody(t, rendered, "PublicPing")
		if strings.Contains(body, unverifiedParse) {
			t.Fatalf("auth_disabled method must not build a caller from an unverified token:\n%s", body)
		}
		if !strings.Contains(body, placeholder) {
			t.Fatalf("auth_disabled method must leave caller None:\n%s", body)
		}
	})

	t.Run("default method still extracts caller", func(t *testing.T) {
		body := rustMethodBody(t, rendered, "GetContract")
		if !strings.Contains(body, "self.authorize_method(") {
			t.Fatalf("default method lost its authorizer call:\n%s", body)
		}
		if !strings.Contains(body, unverifiedParse) || strings.Contains(body, placeholder) {
			t.Fatalf("authorized method must still extract the caller the authorizer verified:\n%s", body)
		}
	})

	t.Run("auth_any_user method still extracts caller", func(t *testing.T) {
		body := rustMethodBody(t, rendered, "ListForMe")
		if !strings.Contains(body, "self.enforce_authenticated(") {
			t.Fatalf("auth_any_user method lost its authenticate call:\n%s", body)
		}
		if !strings.Contains(body, unverifiedParse) || strings.Contains(body, placeholder) {
			t.Fatalf("auth_any_user method must still extract the caller the authorizer verified:\n%s", body)
		}
	})

	// A c2d service's trust boundary is its unix-socket file permissions, not a
	// token — it has no authorizer at all, which is exactly why rustAuthLine
	// gives it the empty accounting identity. The handler-facing caller follows
	// the same branch for the same reason.
	t.Run("c2d method keeps caller None", func(t *testing.T) {
		daemon := &parser.ServiceInfo{
			Name:    "DaemonControl",
			IsC2D:   true,
			Methods: []*parser.MethodInfo{rustCallerTestMethod("GetContract")},
		}
		body := rustMethodBody(t, rustRenderCallerWrapper(t, daemon), "GetContract")
		if strings.Contains(body, unverifiedParse) {
			t.Fatalf("c2d method must not build a caller from an unverified token:\n%s", body)
		}
		if !strings.Contains(body, placeholder) {
			t.Fatalf("c2d method must leave caller None:\n%s", body)
		}
	})
}
