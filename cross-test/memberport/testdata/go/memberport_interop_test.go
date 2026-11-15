// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package memberport

// The Go half of the Go <-> Java member port interop leg (tacplus#322):
// cross-test/memberport/run.sh --language go --interop runs it beside
// MemberPortInteropTest.java, both pointed at MEMBERPORT_INTEROP_DIR. This side
// writes the one fake fleet there (the kernel signing key, the CA, the Java
// member's serving certificate); then a Go member port and a Java member port
// tunnel to each other both ways at once (Java forward -> Go listen, Go forward
// -> Java listen) on 3-second tokens, so each session rotates several times,
// and every byte must arrive intact both ways. The files coordinate the two
// processes: fleet.ready, go.port, java.port, go.done, java.done.

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeInterop(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	tmp := filepath.Join(dir, name+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}

func waitInterop(t *testing.T, dir, name string, timeout time.Duration) string {
	t.Helper()
	var got []byte
	waitFor(t, name+" from the Java side", timeout, func() bool {
		b, err := os.ReadFile(filepath.Join(dir, name))
		got = b
		return err == nil
	})
	return strings.TrimSpace(string(got))
}

func TestMemberPortInteropWithJava(t *testing.T) {
	dir := os.Getenv("MEMBERPORT_INTEROP_DIR")
	if dir == "" {
		t.Skip("MEMBERPORT_INTEROP_DIR runs the Go <-> Java interop leg (run.sh --interop)")
	}
	fx := newFixture(t)
	f := fx.fleet

	// The shared fleet: the Java side signs its own member tokens with the same
	// kernel key and trusts the same CA.
	writeInterop(t, dir, "kernel.key", []byte(f.kernel.Proto().Id+"\n"+f.kernel.Proto().Base64+"\n"))
	writeInterop(t, dir, "ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.ca.Raw}))
	javaLeaf := f.leaf(t, tenantX, testSvc, "EC")
	var chain []byte
	for _, der := range javaLeaf.Certificate {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(javaLeaf.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	writeInterop(t, dir, "java-leaf.pem", chain)
	writeInterop(t, dir, "java-leaf.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
	writeInterop(t, dir, "fleet.ready", []byte("1\n"))

	// 3-second tokens renewed with 2 seconds left: about one rotation every
	// 2.25 seconds, each way.
	gm := newMinter(f, tenantX, 3, 2)
	g := fx.memberWith(gm.token, certRef(f.leaf(t, tenantX, testSvc, "Ed25519")), f.lookup)
	// Behind the Go port: pushes seed 41, expects the Java client's seed 42.
	server := newPeerServer(t, pushMode, 41, 800_000, 42, 4096, 20*time.Millisecond)
	gAddr := fx.listen(g, server.address())
	writeInterop(t, dir, "go.port", []byte(gAddr.String()+"\n"))

	javaAddr := waitInterop(t, dir, "java.port", 10*time.Minute)
	fwd, err := g.Forward(javaAddr)
	if err != nil {
		t.Fatal(err)
	}
	// Go client -> Go port -> Java port -> Java peer server (pushes 31, expects 32).
	start := time.Now()
	r := exchangeWith(fwd, 32, 800_000, 31, 4096, 20*time.Millisecond, 0)
	elapsed := time.Since(start)
	if !r.ok(800_000) {
		t.Fatalf("Go forward -> Java listen: no loss, duplication or reordering: %v", r)
	}
	if elapsed < 6500*time.Millisecond {
		t.Fatalf("the session must outlive two token lifetimes, took %v", elapsed)
	}
	if got := gm.mints.Load(); got < 3 {
		t.Fatalf("the Go client rotated onto new tokens: %d mints", got)
	}
	// Java client -> Java port -> Go port -> this peer server.
	if sr := fx.serverResult(server, 1, 2*time.Minute); !sr.ok(800_000) {
		t.Fatalf("Java forward -> Go listen: %v", sr)
	}
	if got := server.accepted.Load(); got != 1 {
		t.Fatalf("one byte session from Java: the target was dialled %d times", got)
	}
	writeInterop(t, dir, "go.done", []byte("1\n"))
	waitInterop(t, dir, "java.done", 2*time.Minute)
}
