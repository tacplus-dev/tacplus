// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package com.example.grid.model.chassis.memberport;

import static org.junit.jupiter.api.Assertions.assertTrue;

import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

/**
 * The peer itself (#919): a stream is bounded by its progress, never by its
 * total time, so a slowed peer finishes however long it takes, and a stalled
 * one fails with the bound named.
 */
class MemberportPeerTest {
    final long stallMillis = MemberportPeer.stallMillis;

    @AfterEach
    void restore() {
        MemberportPeer.stallMillis = stallMillis;
    }

    @Test
    void aSlowedStreamOutlastsTheStallBound() throws Exception {
        MemberportPeer.stallMillis = 300;
        // The client sends its few bytes at once and reads slowly while the
        // server pushes slowly, so the server's push outlives its read side too.
        try (MemberportPeer.Server server = new MemberportPeer.Server(MemberportPeer.Mode.PUSH, 61, 40 * 1024, 62, 1024, 50)) {
            long start = System.currentTimeMillis();
            MemberportPeer.Result r = MemberportPeer.exchange(server.address(), 62, 4096, 61, 4096, 0, 20);
            long elapsed = System.currentTimeMillis() - start;
            assertTrue(r.ok(40 * 1024), "a slowed stream, " + elapsed + "ms long against a 300ms stall bound: " + r);
            assertTrue(elapsed > 900, "the stream must outlast the stall bound several times over, took " + elapsed + "ms");
            MemberPortBehaviorTest.waitFor("the server's result", 10_000, () -> server.results.size() == 1);
            assertTrue(server.results.get(1).ok(4096), "server: " + server.results.get(1));
        }
    }

    @Test
    void aStalledPeerIsCutAtTheStallBound() throws Exception {
        MemberportPeer.stallMillis = 300;
        try (ServerSocket silent = new ServerSocket(0, 1, InetAddress.getLoopbackAddress())) {
            // Accepts (the backlog does), then neither reads nor writes.
            long start = System.currentTimeMillis();
            MemberportPeer.Result r = MemberportPeer.exchange(
                    (InetSocketAddress) silent.getLocalSocketAddress(), 1, 1024, 2, 1024, 0, 0);
            long elapsed = System.currentTimeMillis() - start;
            assertTrue(r.error != null && r.error.contains("stall bound"), "a stalled peer: " + r);
            assertTrue(elapsed < 5_000, "a stalled peer is cut at the stall bound, took " + elapsed + "ms");
        }
    }
}
