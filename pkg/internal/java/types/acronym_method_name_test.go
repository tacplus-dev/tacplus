// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// An RPC whose name ends in an all-caps acronym. grpc-java's own generator names
// this method "executeACL" (protoc convention: lowercase the first letter only);
// a snake_case round-trip mangles it to "executeAcl".
func javaAcronymMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           "ExecuteACL",
		InputType:      "ExecuteACLRequest",
		OutputType:     "ExecuteACLResponse",
		FullInputType:  "acl.ExecuteACLRequest",
		FullOutputType: "acl.ExecuteACLResponse",
		AuthDisabled:   true,
	}
}

// The wrapper extends grpc-java's QueryServiceImplBase and marks its rpc methods
// @Override. The name must be exactly what protoc-gen-grpc-java emitted, or the
// @Override does not compile.
func TestJavaAcronymRPC_WrapperOverrideMatchesGrpcJava(t *testing.T) {
	got := generateJavaGRPCWrapper(
		javaAcronymMethod(), "QueryService", "", "Acl.ExecuteACLRequest", "Acl.ExecuteACLResponse",
		"", "", false, true,
		map[string]service.AccountingInfo{}, "acl", "services", map[string]string{}, proto.SkippedTraceTypes{}, nil, nil, nil)
	if !strings.Contains(got, "public void executeACL(") {
		t.Errorf("wrapper must @Override grpc-java's executeACL; got:\n%s", got)
	}
	if strings.Contains(got, "executeAcl") {
		t.Errorf("wrapper mangled the acronym to executeAcl:\n%s", got)
	}
}

// The wrapper's body calls handler.<name>(...). Whatever name that is, the
// Handler interface this generator also emits must declare the same one.
func TestJavaAcronymRPC_HandlerInterfaceMatchesWrapperCall(t *testing.T) {
	wrapper := generateJavaGRPCWrapper(
		javaAcronymMethod(), "QueryService", "", "Acl.ExecuteACLRequest", "Acl.ExecuteACLResponse",
		"", "", false, true,
		map[string]service.AccountingInfo{}, "acl", "services", map[string]string{}, proto.SkippedTraceTypes{}, nil, nil, nil)
	decl := generateJavaHandlerMethod(javaAcronymMethod(), "Acl.ExecuteACLRequest", "Acl.ExecuteACLResponse", nil)

	call := "handler.executeACL("
	if !strings.Contains(wrapper, call) {
		t.Errorf("wrapper must call %s; got:\n%s", call, wrapper)
	}
	if !strings.Contains(decl, " executeACL(") {
		t.Errorf("Handler interface must declare executeACL; got:\n%s", decl)
	}
}

// MCP dispatch and its generated tests spell the same handler method through
// methodNameLowerCamel; it must agree with the Handler interface declaration.
func TestJavaAcronymRPC_MethodNameLowerCamelMatchesHandler(t *testing.T) {
	if got := methodNameLowerCamel("ExecuteACL"); got != "executeACL" {
		t.Errorf("methodNameLowerCamel(ExecuteACL) = %q, want executeACL", got)
	}
}
