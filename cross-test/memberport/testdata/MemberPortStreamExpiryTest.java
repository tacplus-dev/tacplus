// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package com.example.grid.model.chassis.memberport;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.grpc.Status;
import java.net.InetSocketAddress;
import java.util.concurrent.atomic.AtomicReference;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

/**
 * The member port's leg of stream-expiry (tacplus#260, #321): a member tunnel
 * stream closes at its opening token's exp like every other stream, with no
 * exception for member traffic. The stream-expiry gate runs this class; the
 * memberport gate runs it too.
 */
class MemberPortStreamExpiryTest {
    final MemberPortBehaviorTest fixture = new MemberPortBehaviorTest();

    @BeforeEach
    void setUp() throws Exception {
        fixture.setUp();
    }

    @AfterEach
    void tearDown() {
        fixture.tearDown();
    }

    @Test
    void anExpiredUnrotatedStreamEndsUnauthenticatedAtExpWithALocalReset() throws Exception {
        MemberportPeer.Server behind = fixture.echo();
        InetSocketAddress port = fixture.member(MemberPortBehaviorTest.X).listen(MemberPortBehaviorTest.loopback0(), behind.address());
        long ttlSeconds = 3;
        long exp = (System.currentTimeMillis() / 1000 + ttlSeconds) * 1000;
        MemberPortBehaviorTest.Raw r = new MemberPortBehaviorTest.Raw(fixture.rawChannel(port),
                fixture.fleet.mint(MemberPortBehaviorTest.SVC, MemberPortBehaviorTest.X, MemberPortBehaviorTest.AUD, ttlSeconds));
        MemberPortBehaviorTest.openSession(r);
        r.send(MemberPortBehaviorTest.data(new byte[] {1, 2, 3}));
        assertArrayEquals(new byte[] {1, 2, 3}, r.next().getPayload().toByteArray(), "the stream works until exp");
        Status s = r.status(ttlSeconds * 1000 + 5_000);
        long late = System.currentTimeMillis() - exp;
        MemberPortBehaviorTest.assertRefused(Status.Code.UNAUTHENTICATED, s, "an expired, unrotated stream");
        assertTrue(late > -250 && late < 1_500, "closed at exp, not before or long after: " + late + "ms after it");
        MemberPortBehaviorTest.waitFor("the connection behind the port to end", 5_000, () -> behind.results.size() == 1);
        String error = String.valueOf(behind.results.get(1).error);
        assertTrue(error.contains("reset"), "the connection behind the port is reset: " + error);
    }

    @Test
    void aMemberThatDoesNotRotateIsClosedAtExpiry() throws Exception {
        MemberPortBehaviorTest.Minter stuck = new MemberPortBehaviorTest.Minter(fixture.fleet, MemberPortBehaviorTest.X, 3, 1);
        MemberPort a = fixture.member(stuck, new AtomicReference<>(
                fixture.fleet.leaf(MemberPortBehaviorTest.X, MemberPortBehaviorTest.SVC, "Ed25519")), fixture.fleet.jwks::get);
        MemberportPeer.Server echo = fixture.echo();
        InetSocketAddress fwd = fixture.tunnel(a, fixture.member(MemberPortBehaviorTest.X), echo);
        stuck.token();
        stuck.neverRenew = true;
        long start = System.currentTimeMillis();
        MemberportPeer.Result r = MemberportPeer.exchange(fwd, 41, 2_000_000, 41, 2048, 10, 0);
        long elapsed = System.currentTimeMillis() - start;
        assertTrue(!r.ok(2_000_000) && r.firstMismatch < 0, "cut at expiry, what arrived intact: " + r);
        assertTrue(MemberPortBehaviorTest.reset(r), "the black-box client sees a reset: " + r);
        assertTrue(elapsed < 4_500, "closed at the token's expiry, took " + elapsed + "ms");
    }
}
