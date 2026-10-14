// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// TestJavaSubscriberAttachesCallCredentials verifies that the generated
// Subscriber class accepts a CallCredentials supplier and attaches it to
// every gRPC call — never using bare CallOptions.DEFAULT without credentials.
func TestJavaSubscriberAttachesCallCredentials(t *testing.T) {
	sub := readJavaTemplate(t, "java/types/Subscriber.java.tmpl")

	// The subscriber must import CallCredentials.
	if !strings.Contains(sub, "import io.grpc.CallCredentials;") {
		t.Fatal("Subscriber must import io.grpc.CallCredentials")
	}

	// The constructor must accept a Supplier<CallCredentials>.
	if !strings.Contains(sub, "Supplier<CallCredentials> credentialsSupplier") {
		t.Fatal("Subscriber constructor must accept a Supplier<CallCredentials>")
	}

	// The newCall site must attach credentials via withCallCredentials,
	// not use bare CallOptions.DEFAULT.
	newCallIdx := strings.Index(sub, "channel.newCall(methodDesc,")
	if newCallIdx < 0 {
		t.Fatal("Subscriber must contain channel.newCall(methodDesc, ...)")
	}
	// Grab enough context after the newCall to check for withCallCredentials.
	tail := sub[newCallIdx:]
	semi := strings.Index(tail, ";")
	if semi < 0 {
		t.Fatal("could not find statement terminator after newCall")
	}
	callStmt := tail[:semi]
	if !strings.Contains(callStmt, ".withCallCredentials(credentialsSupplier.get())") {
		t.Fatalf("Subscriber newCall must use .withCallCredentials(credentialsSupplier.get());\ngot: %s", callStmt)
	}

	// Ensure bare CallOptions.DEFAULT (without withCallCredentials) is NOT
	// used — it would silently drop auth.
	bareDefault := "channel.newCall(methodDesc, CallOptions.DEFAULT)"
	for i := 0; i < len(sub); {
		idx := strings.Index(sub[i:], bareDefault)
		if idx < 0 {
			break
		}
		pos := i + idx
		after := pos + len(bareDefault)
		// If the next non-whitespace char after CallOptions.DEFAULT is NOT
		// a dot (i.e. no .withCallCredentials chained), that's a bare usage.
		rest := strings.TrimLeft(sub[after:], " \t\r\n")
		if !strings.HasPrefix(rest, ".withCallCredentials(") {
			t.Fatalf("Subscriber contains bare CallOptions.DEFAULT without .withCallCredentials at offset %d", pos)
		}
		i = after
	}
}

// TestJavaServiceManagerWiresSubscriberCredentials verifies that the
// ServiceManager passes call credentials to the subscriber constructor,
// using callCredentialsForAudience with the target service (event-bus).
func TestJavaServiceManagerWiresSubscriberCredentials(t *testing.T) {
	sm := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")

	// Find subscriber construction — it must include a credentials supplier.
	subIdx := strings.Index(sm, "new {{.ClassName}}(")
	if subIdx < 0 {
		// Template may not emit subscriptions if HasSubscriptions is false.
		return
	}
	// Grab the constructor call.
	tail := sm[subIdx:]
	semi := strings.Index(tail, ";")
	if semi < 0 {
		t.Fatal("could not find statement terminator after subscriber constructor")
	}
	ctorStmt := tail[:semi]

	if !strings.Contains(ctorStmt, `callCredentialsForAudience("{{.TargetService}}")`) {
		t.Fatalf("ServiceManager must wire subscriber with callCredentialsForAudience(TargetService);\ngot: %s", ctorStmt)
	}
}
