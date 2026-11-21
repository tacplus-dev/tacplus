// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package com.example.grid.model.chassis.memberport;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.example.grid.model.chassis.token.TokenWrapper;
import com.example.grid.model.types.sys.memberport.Memberport.MemberChunk;
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
    /**
     * How long after its token's exp a stream may take to close. Generous on
     * purpose, as the stream-expiry legs' are: on a loaded runner every step
     * runs about a second late (#627); a stream that is never closed still fails.
     */
    static final long CLOSE_BOUND_MS = 5_000;

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
        // The token's life is the property under test, not a bound on the
        // runner (#919): a loaded runner can spend a 3-second token before the
        // session opens. Then the stream is refused at exp before it worked
        // at all, which says nothing about expiry, so it is tried again on a
        // token twice as long, up to MAX_TTL_SECONDS, named if it is reached.
        for (long ttlSeconds = 3; ; ttlSeconds *= 2) {
            if (expiresAtExp(ttlSeconds)) {
                return;
            }
            if (ttlSeconds * 2 > MAX_TTL_SECONDS) {
                throw new AssertionError("no session opened before its token's exp, on tokens of up to "
                        + ttlSeconds + "s");
            }
        }
    }

    /** The longest token {@link #anExpiredUnrotatedStreamEndsUnauthenticatedAtExpWithALocalReset} tries. */
    static final long MAX_TTL_SECONDS = 24;

    /**
     * One stream on a {@code ttlSeconds} token: false when it ended before its
     * session opened (the token spent on a slow runner), else its checks.
     */
    boolean expiresAtExp(long ttlSeconds) throws Exception {
        MemberportPeer.Server behind = fixture.echo();
        InetSocketAddress port = fixture.member(MemberPortBehaviorTest.X).listen(MemberPortBehaviorTest.loopback0(), behind.address());
        long exp = (System.currentTimeMillis() / 1000 + ttlSeconds) * 1000;
        MemberPortBehaviorTest.Raw r = new MemberPortBehaviorTest.Raw(fixture.rawChannel(port),
                fixture.fleet.mint(MemberPortBehaviorTest.SVC, MemberPortBehaviorTest.X, MemberPortBehaviorTest.AUD, ttlSeconds));
        MemberChunk first = r.send(MemberPortBehaviorTest.open()).poll(MemberPortBehaviorTest.Raw.NEXT_DEADLINE_MS);
        if (first == null && r.closed.isDone() && r.closed.get().getCode() == Status.Code.UNAUTHENTICATED
                && System.currentTimeMillis() >= exp - 250) {
            return false;
        }
        assertTrue(first != null && !first.getSessionId().isEmpty(),
                "the server's first chunk names the session: " + first + "; stream status " + r.closed.getNow(null));
        r.send(MemberPortBehaviorTest.data(new byte[] {1, 2, 3}));
        MemberChunk echoed = r.poll(MemberPortBehaviorTest.Raw.NEXT_DEADLINE_MS);
        if (echoed == null && System.currentTimeMillis() >= exp - 250) {
            return false;
        }
        assertTrue(echoed != null, "the stream works until exp; stream status " + r.closed.getNow(null));
        assertArrayEquals(new byte[] {1, 2, 3}, echoed.getPayload().toByteArray(), "the stream works until exp");
        Status s = r.status(ttlSeconds * 1000 + CLOSE_BOUND_MS + 1_000);
        long late = System.currentTimeMillis() - exp;
        MemberPortBehaviorTest.assertRefused(Status.Code.UNAUTHENTICATED, s, "an expired, unrotated stream");
        assertTrue(late > -250 && late < CLOSE_BOUND_MS,
                "closed at exp, not before or long after: " + late + "ms after it (bound " + CLOSE_BOUND_MS + "ms)");
        MemberPortBehaviorTest.waitFor("the connection behind the port to end", 15_000, () -> behind.results.size() == 1);
        String error = String.valueOf(behind.results.get(1).error);
        assertTrue(error.contains("reset"), "the connection behind the port is reset: " + error);
        return true;
    }

    @Test
    void aMemberThatDoesNotRotateIsClosedAtExpiry() throws Exception {
        MemberPortBehaviorTest.Minter stuck = new MemberPortBehaviorTest.Minter(fixture.fleet, MemberPortBehaviorTest.X, 3, 1);
        MemberPort a = fixture.member(stuck, new AtomicReference<>(
                fixture.fleet.leaf(MemberPortBehaviorTest.X, MemberPortBehaviorTest.SVC, "Ed25519")), fixture.fleet.jwks::get);
        MemberportPeer.Server echo = fixture.echo();
        InetSocketAddress fwd = fixture.tunnel(a, fixture.member(MemberPortBehaviorTest.X), echo);
        long exp = TokenWrapper.parse(stuck.token()).getExpiration() * 1000;
        stuck.neverRenew = true;
        MemberportPeer.Result r = MemberportPeer.exchange(fwd, 41, 2_000_000, 41, 2048, 10, 0);
        long late = System.currentTimeMillis() - exp;
        assertTrue(!r.ok(2_000_000) && r.firstMismatch < 0, "cut at expiry, what arrived intact: " + r);
        assertTrue(MemberPortBehaviorTest.reset(r), "the black-box client sees a reset: " + r);
        // Uncut, the exchange takes about 10 s (2 MB in 2 KiB chunks, 10 ms apart).
        assertTrue(late < CLOSE_BOUND_MS,
                "closed at the token's expiry: " + late + "ms after it (bound " + CLOSE_BOUND_MS + "ms)");
    }
}
