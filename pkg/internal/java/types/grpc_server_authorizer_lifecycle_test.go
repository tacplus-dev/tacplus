// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// GrpcServer used to build a verify-nothing test-mode Authorizer in applyConfig
// and expose a setAuthorizer/getAuthorizer pair that nothing ever called. Every
// service therefore logged "test mode: empty key source" at INFO in production,
// and shutdown closed that placeholder while the REAL authorizer — the one
// ServiceManager builds and enforces through enforcingInterceptor — was never
// closed, leaving its RemoteJwkSource re-subscribing on a dying channel.
//
// The go chassis never had an authorizer field on its server for exactly this
// reason. These two tests pin both halves of the correction so the field cannot
// come back and the real close cannot be dropped.

// stripJavaComments drops // line comments and /* */ block comments so a
// banned-symbol scan cannot be tripped by prose that merely names the symbol.
func stripJavaComments(src string) string {
	var out strings.Builder
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		for {
			if inBlock {
				end := strings.Index(line, "*/")
				if end < 0 {
					line = ""
					break
				}
				line = line[end+2:]
				inBlock = false
				continue
			}
			start := strings.Index(line, "/*")
			lineComment := strings.Index(line, "//")
			if lineComment >= 0 && (start < 0 || lineComment < start) {
				line = line[:lineComment]
				break
			}
			if start < 0 {
				break
			}
			out.WriteString(line[:start])
			line = line[start+2:]
			inBlock = true
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}

func TestJavaGrpcServerHoldsNoAuthorizer(t *testing.T) {
	src, err := templates.FS.ReadFile("java/chassis/grpcserver/GrpcServer.java.tmpl")
	if err != nil {
		t.Fatalf("read GrpcServer.java template: %v", err)
	}
	// The template carries a comment block explaining why the field is gone and
	// naming the removed symbols, so match against code lines only.
	rendered := stripJavaComments(string(src))

	for _, banned := range []string{
		"setAuthorizer",
		"getAuthorizer",
		"Authorizer.create()",
		"Authorizer authorizer",
	} {
		if strings.Contains(rendered, banned) {
			t.Fatalf("GrpcServer.java template contains %q: the server must not own an "+
				"Authorizer. Authorization is enforced by the generated wrappers via "+
				"Authorizer.enforcingInterceptor(), and ServiceManager owns the real "+
				"authorizer's lifecycle.", banned)
		}
	}

	// listenTLS still installs the static header-stashing interceptor; that is
	// the only Authorizer touch the server is allowed to have.
	if !strings.Contains(rendered, "Authorizer.headerCaptureInterceptor()") {
		t.Fatal("GrpcServer.java template no longer installs Authorizer.headerCaptureInterceptor(); " +
			"the auth header would never reach the context")
	}
}

func TestJavaServiceManagerClosesTheRealAuthorizer(t *testing.T) {
	src, err := templates.FS.ReadFile("java/types/ServiceManager.java.tmpl")
	if err != nil {
		t.Fatalf("read ServiceManager.java template: %v", err)
	}
	rendered := string(src)

	build := strings.Index(rendered, "new Authorizer(Authorizer.remoteSource(")
	if build < 0 {
		t.Fatal("ServiceManager.java template no longer builds the remote-source Authorizer")
	}

	hook := strings.Index(rendered, "Runtime.getRuntime().addShutdownHook(")
	if hook < 0 {
		t.Fatal("ServiceManager.java template no longer registers a shutdown hook")
	}

	closeIdx := strings.Index(rendered, "authorizer.close()")
	if closeIdx < 0 {
		t.Fatal("ServiceManager.java template never closes the authorizer it built; its " +
			"RemoteJwkSource would keep re-subscribing on the dying channel through shutdown")
	}
	if closeIdx < hook {
		t.Fatalf("authorizer.close() appears at offset %d, before the shutdown hook at %d; "+
			"it must run on shutdown, not during startup", closeIdx, hook)
	}
}
