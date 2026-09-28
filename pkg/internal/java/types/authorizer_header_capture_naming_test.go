// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// The header-capture interceptor used to be called Authorizer.interceptor().
// It authenticates nothing — it copies the authorization header into a Context
// key and lets every call through — but sat one method away from
// enforcingInterceptor() under a name that reads like "the auth interceptor".
// A service wired onto a builder that installs it, believing it protects the
// service, gets zero protection. The name is the whole guard here, so pin it:
// the enforcing-sounding bare name must not come back, and the Javadoc must
// keep saying plainly that the interceptor enforces nothing.
func TestJavaAuthorizerHeaderCaptureIsNotNamedLikeAnAuthenticator(t *testing.T) {
	src, err := templates.FS.ReadFile("java/chassis/grpcserver/Authorizer.java.tmpl")
	if err != nil {
		t.Fatalf("read Authorizer.java template: %v", err)
	}
	code := stripJavaComments(string(src))

	if !strings.Contains(code, "public static ServerInterceptor headerCaptureInterceptor()") {
		t.Fatal("Authorizer.java template no longer declares headerCaptureInterceptor(); " +
			"the header would never reach AUTH_HEADER_CTX_KEY for authorize() to read")
	}

	for _, banned := range []string{
		"ServerInterceptor interceptor()",
		"ServerInterceptor authInterceptor()",
		"ServerInterceptor authenticatingInterceptor()",
	} {
		if strings.Contains(code, banned) {
			t.Fatalf("Authorizer.java template declares %q: a method that only stashes the "+
				"authorization header must not carry a name a reader can mistake for an "+
				"authenticator. Enforcement lives in enforcingInterceptor()/"+
				"authorizingInterceptor(); the capture-only one is headerCaptureInterceptor().", banned)
		}
	}

	// The disclaimer is the other half of the fix — a rename alone still lets a
	// reader assume the interceptor does something.
	doc := string(src)
	if !strings.Contains(doc, "performs NO authentication and NO authorization") {
		t.Fatal("headerCaptureInterceptor()'s Javadoc no longer states that it performs no " +
			"authentication and no authorization; that disclaimer is what stops the next " +
			"integrator from wiring it as if it were the auth chain")
	}
}
