// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package memberport

// The Go half of the Go <-> Java member port interop leg (tacplus#322):
// cross-test/memberport/run.sh --language go --interop runs it beside
// MemberPortInteropTest.java, both pointed at MEMBERPORT_INTEROP_DIR. This side
// writes the one fake fleet there (the kernel signing key, the CA, the Java
// member's serving certificate); then a Go member port and a Java member port
// tunnel to each other both ways at once (Java forward -> Go listen, Go forward
// -> Java listen) on 6-second tokens, so each session rotates several times,
// and every byte must arrive intact both ways. The files coordinate the two
// processes: fleet.ready, go.port, java.port, go.done, java.done.
//
// The leg is bounded by its bytes, not by seconds (#919): each stream is a
// fixed count, cut only when it stops moving (the peer's stall bound). Neither
// side takes its member port down before the other is done: a side whose own
// direction fails says so in its done file ("ok", or its failures) and keeps
// serving the other's session, so each side reports its own result, not a
// reset. Every wait after the streams start has an explicit deadline, named
// when it passes.

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
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
	got, ok := readInterop(dir, name, timeout)
	if !ok {
		t.Fatalf("timed out waiting for %s from the Java side", name)
	}
	return got
}

// interopDeadline is each wait's after the streams start: for the other
// side's session, and for its done file. Generous: the streams take seconds.
const interopDeadline = 5 * time.Minute

// readInterop polls for the Java side's file name until the deadline; its
// trimmed content, or false once the deadline has passed.
func readInterop(dir, name string, deadline time.Duration) (string, bool) {
	until := time.Now().Add(deadline)
	for {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			return strings.TrimSpace(string(b)), true
		}
		if time.Now().After(until) {
			return "", false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// interopReport is a done file's content: "ok", or one failure a line.
func interopReport(failures []string) []byte {
	if len(failures) == 0 {
		return []byte("ok\n")
	}
	return []byte(strings.Join(failures, "\n") + "\n")
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

	// 6-second tokens renewed with 4 seconds left: a stream lives 4 to 6
	// seconds and the client rotates at 75% of it, so a cutover has a second
	// or more before the old stream's exp, as in the behaviour tests (#737).
	gm := newMinter(f, tenantX, 6, 4)
	g := fx.memberWith(gm.token, certRef(f.leaf(t, tenantX, testSvc, "Ed25519")), f.lookup)
	// Behind the Go port: pushes seed 41, expects the Java client's seed 42.
	server := newPeerServer(t, pushMode, 41, 1_600_000, 42, 4096, 20*time.Millisecond)
	gAddr := fx.listen(g, server.address())
	writeInterop(t, dir, "go.port", []byte(gAddr.String()+"\n"))

	javaAddr := waitInterop(t, dir, "java.port", 10*time.Minute)
	var failures []string
	// Go client -> Go port -> Java port -> Java peer server (pushes 31, expects 32).
	if fwd, err := g.Forward(javaAddr); err != nil {
		failures = append(failures, fmt.Sprintf("Go forward -> Java listen: forward: %v", err))
	} else {
		start := time.Now()
		r := exchangeWith(fwd, 32, 1_600_000, 31, 4096, 20*time.Millisecond, 0)
		elapsed := time.Since(start)
		switch {
		case !r.ok(1_600_000):
			failures = append(failures, fmt.Sprintf("Go forward -> Java listen: no loss, duplication or reordering: %v after %v", r, elapsed))
		case elapsed < 12500*time.Millisecond:
			failures = append(failures, fmt.Sprintf("the session must outlive two token lifetimes, took %v", elapsed))
		}
		if got := gm.mints.Load(); got < 3 {
			failures = append(failures, fmt.Sprintf("the Go client rotated onto new tokens: %d mints", got))
		}
	}
	// Java client -> Java port -> Go port -> this peer server.
	until := time.Now().Add(interopDeadline)
	for server.resultCount() == 0 && time.Now().Before(until) {
		time.Sleep(20 * time.Millisecond)
	}
	if sr, ok := server.result(1); !ok {
		failures = append(failures, fmt.Sprintf("Java forward -> Go listen: no session ended within the Go side's deadline of %v", interopDeadline))
	} else if !sr.ok(1_600_000) {
		failures = append(failures, fmt.Sprintf("Java forward -> Go listen: %v", sr))
	}
	if got := server.accepted.Load(); got != 1 {
		failures = append(failures, fmt.Sprintf("one byte session from Java: the target was dialled %d times", got))
	}
	writeInterop(t, dir, "go.done", interopReport(failures))
	java, ok := readInterop(dir, "java.done", interopDeadline)
	if !ok {
		java = fmt.Sprintf("nothing: it was not done within the Go side's deadline of %v", interopDeadline)
	}
	if len(failures) > 0 {
		t.Fatalf("%s\nthe Java side reported: %s", strings.Join(failures, "\n"), java)
	}
}
