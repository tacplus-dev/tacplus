// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package memberport

// The member port's leg of stream-expiry (tacplus#260, #321, #322): a member
// tunnel stream closes at its opening token's exp like every other stream,
// with no exception for member traffic. The Go twin of
// MemberPortStreamExpiryTest.java; the stream-expiry gate runs these, the
// memberport gate runs them too.

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
)

func TestMemberPortStreamExpiryAnExpiredUnrotatedStreamEndsUnauthenticatedAtExpWithALocalReset(t *testing.T) {
	fx := newFixture(t)
	behind := fx.echo()
	port := fx.listen(fx.member(tenantX), behind.address())
	exp := time.Now().Unix() + 3
	r := fx.raw(fx.rawConn(port), fx.fleet.mintWith(fx.fleet.kernel, testSvc, tenantX, testAud, exp))
	openSession(t, r)
	r.send(dataChunk(1, 2, 3))
	if got := r.next().Payload; !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("the stream works until exp: %v", got)
	}
	s := r.status(8 * time.Second)
	late := time.Since(time.Unix(exp, 0))
	assertRefused(t, codes.Unauthenticated, s, "an expired, unrotated stream")
	if late < -250*time.Millisecond || late > 1500*time.Millisecond {
		t.Fatalf("closed at exp, not before or long after: %v after it", late)
	}
	if res := fx.serverResult(behind, 1, 5*time.Second); !strings.Contains(res.err, "reset") {
		t.Fatalf("the connection behind the port is reset: %v", res)
	}
}

func TestMemberPortStreamExpiryAMemberThatDoesNotRotateIsClosedAtExpiry(t *testing.T) {
	fx := newFixture(t)
	stuck := newMinter(fx.fleet, tenantX, 3, 1)
	a := fx.memberWith(stuck.token, certRef(fx.fleet.leaf(t, tenantX, testSvc, "Ed25519")), fx.fleet.lookup)
	fwd := fx.tunnel(a, fx.member(tenantX), fx.echo())
	_, _ = stuck.token()
	stuck.neverRenew.Store(true)
	start := time.Now()
	r := exchangeWith(fwd, 41, 2_000_000, 41, 2048, 10*time.Millisecond, 0)
	elapsed := time.Since(start)
	if r.ok(2_000_000) || r.firstMismatch >= 0 {
		t.Fatalf("cut at expiry, what arrived intact: %v", r)
	}
	if !r.wasReset() {
		t.Fatalf("the black-box client sees a reset: %v", r)
	}
	if elapsed > 4500*time.Millisecond {
		t.Fatalf("closed at the token's expiry, took %v", elapsed)
	}
}
