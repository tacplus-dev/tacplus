// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package com.example.grid.model.chassis.memberport;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.example.grid.model.chassis.memberport.HazelcastMemberPortTest.Relay;
import com.hazelcast.core.HazelcastInstance;
import com.hazelcast.map.IMap;
import io.grpc.ManagedChannel;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.condition.EnabledIfEnvironmentVariable;

/**
 * HazelcastMemberPortTest with every member port in another process
 * (tacplus#322): cross-test/memberport/run.sh --language go|rust --interop runs
 * it beside memberport_hazelcast_test.go (resp. memberport_hazelcast.rs), both
 * pointed at MEMBERPORT_HAZELCAST_DIR. The Hazelcast members, their counting
 * relays and their MemberAddressProvider glue are here, as in
 * HazelcastMemberPortTest; each member's member port is a Go (resp. Rust)
 * generated member port, with its fleet, its token minter and its
 * certificates, in the other process. This side drives it through numbered
 * command files: it writes {@code cmd.N} (one line: a verb and its words) and
 * the other side answers {@code ack.N} ({@code ok [value]} or
 * {@code err <reason>}). The verbs: {@code member <id> <namespace> <ttl>
 * <renewBefore> <target>} (a member port listening on loopback in front of
 * {@code target}; answers its address), {@code forward <id> <remote>} (answers
 * the loopback forward), {@code mints <id>}, {@code never-renew <id>},
 * {@code mint <namespace> <ttl>} (a member token from the fleet),
 * {@code reset} (closes every member port) and {@code end}.
 */
@EnabledIfEnvironmentVariable(named = "MEMBERPORT_HAZELCAST_DIR", matches = ".+")
class HazelcastMemberPortInteropTest {
    static final String X = MemberPortBehaviorTest.X;
    static final String Y = MemberPortBehaviorTest.Y;

    /** The other process's member ports, over the command files. */
    static final class Remote {
        final Path dir;
        int next;

        Remote(Path dir) {
            this.dir = dir;
        }

        /** Runs one command; its answer's value, or an AssertionError with the other side's reason. */
        synchronized String call(Object... words) throws Exception {
            int n = next++;
            StringBuilder line = new StringBuilder();
            for (Object w : words) {
                line.append(line.length() == 0 ? "" : " ").append(w);
            }
            Path tmp = dir.resolve("cmd." + n + ".java.tmp");
            Files.writeString(tmp, line + "\n", StandardCharsets.UTF_8);
            Files.move(tmp, dir.resolve("cmd." + n), StandardCopyOption.ATOMIC_MOVE);
            // The first command waits for the other side's build.
            long deadline = System.currentTimeMillis() + (n == 0 ? 600_000 : 180_000);
            Path ack = dir.resolve("ack." + n);
            while (!Files.exists(ack)) {
                if (System.currentTimeMillis() > deadline) {
                    throw new AssertionError("no answer to \"" + line + "\" from the other side");
                }
                Thread.sleep(20);
            }
            String answer = Files.readString(ack, StandardCharsets.UTF_8).trim();
            if (answer.equals("ok")) {
                return "";
            }
            if (answer.startsWith("ok ")) {
                return answer.substring(3);
            }
            throw new AssertionError("\"" + line + "\" failed on the other side: " + answer);
        }
    }

    static Remote remote;

    static String hostPort(InetSocketAddress a) {
        return a.getAddress().getHostAddress() + ":" + a.getPort();
    }

    static InetSocketAddress address(String hostPort) {
        int colon = hostPort.lastIndexOf(':');
        return new InetSocketAddress(hostPort.substring(0, colon), Integer.parseInt(hostPort.substring(colon + 1)));
    }

    @BeforeAll
    static void connect() {
        remote = new Remote(Path.of(System.getenv("MEMBERPORT_HAZELCAST_DIR")));
    }

    @AfterAll
    static void end() throws Exception {
        remote.call("end");
    }

    final List<AutoCloseable> closers = new ArrayList<>();
    int members;

    @AfterEach
    void tearDown() throws Exception {
        for (int i = closers.size() - 1; i >= 0; i--) {
            try {
                closers.get(i).close();
            } catch (Exception ignored) {
                // Best effort.
            }
        }
        remote.call("reset");
    }

    /** One member: its Hazelcast port and relay here, its member port in the other process. */
    final class Member {
        final String id;
        final String namespace;
        final int hzPort;
        final Relay relay;
        final InetSocketAddress portAddress;
        final AtomicReference<InetSocketAddress> publicAddress = new AtomicReference<>();
        final AtomicInteger removed = new AtomicInteger();
        HazelcastInstance hz;

        Member(String namespace, long ttl, long renewBefore) throws Exception {
            this.id = "m" + members++;
            this.namespace = namespace;
            this.hzPort = HazelcastMemberPortTest.freePort();
            this.relay = new Relay(new InetSocketAddress(InetAddress.getLoopbackAddress(), hzPort));
            closers.add(relay);
            this.portAddress = address(remote.call("member", id, namespace, ttl, renewBefore, hostPort(relay.address())));
        }

        /** This member's member port's forward to {@code remotePort}. */
        InetSocketAddress forward(InetSocketAddress remotePort) throws Exception {
            return address(remote.call("forward", id, hostPort(remotePort)));
        }

        int mints() throws Exception {
            return Integer.parseInt(remote.call("mints", id));
        }

        void neverRenew() throws Exception {
            remote.call("never-renew", id);
        }

        /** Starts Hazelcast, joining {@code seeds} (the loopback forwards to the other members). */
        void start(List<InetSocketAddress> seeds) {
            hz = HazelcastMemberPortTest.startHazelcast(hzPort, publicAddress, removed, seeds);
            closers.add(hz::shutdown);
        }

        int clusterSize() {
            return hz.getCluster().getMembers().size();
        }
    }

    /** Starts two members at once: each one's join waits for the other. */
    static void startTogether(Member a, List<InetSocketAddress> aSeeds, Member b, List<InetSocketAddress> bSeeds) throws Exception {
        Thread t = new Thread(() -> a.start(aSeeds), "start-" + a.hzPort);
        t.start();
        b.start(bSeeds);
        t.join(60_000);
    }

    /** Wires two members to each other: each one's public address is the other's forward to its member port. */
    void pair(Member a, Member b) throws Exception {
        InetSocketAddress aToB = a.forward(b.portAddress);
        InetSocketAddress bToA = b.forward(a.portAddress);
        b.publicAddress.set(aToB);
        a.publicAddress.set(bToA);
    }

    @Test
    void sameTenantMembersFormAClusterAndOtherTenantsCannotJoin() throws Exception {
        // Three-second tokens renewed with two seconds left: the cluster lives
        // through several session rotations during the test.
        Member x1 = new Member(X, 3, 2);
        Member x2 = new Member(X, 3, 2);
        Member y1 = new Member(Y, 300, 60);
        pair(x1, x2);
        InetSocketAddress yToX1 = y1.forward(x1.portAddress);
        y1.publicAddress.set(new InetSocketAddress(InetAddress.getLoopbackAddress(), y1.hzPort));

        // Each seeds the other: the address it reaches the other at.
        startTogether(x1, List.of(x2.publicAddress.get()), x2, List.of(x1.publicAddress.get()));
        MemberPortBehaviorTest.waitFor("tenant X's members to form a cluster", 60_000,
                () -> x1.clusterSize() == 2 && x2.clusterSize() == 2);
        int x1Relayed = x1.relay.accepted.get();
        assertTrue(x1Relayed > 0, "X's members talk through the member port");

        // Tenant Y's member seeds tenant X's member through its own member port.
        y1.start(List.of(yToX1));
        Thread.sleep(5_000);
        assertEquals(1, y1.clusterSize(), "tenant Y's member must not join tenant X's cluster");
        assertEquals(2, x1.clusterSize(), "tenant X's cluster is untouched");

        IMap<String, String> m1 = x1.hz.getMap("records");
        m1.put("k", "from-x1");
        assertEquals("from-x1", x2.hz.<String, String>getMap("records").get("k"), "X's members share data");
        assertNull(y1.hz.<String, String>getMap("records").get("k"), "tenant Y sees none of it");

        // Rotation mid-stream: eight more seconds of traffic spanning several
        // token lifetimes, with no member ever dropping out.
        int removedBefore = x1.removed.get() + x2.removed.get();
        int mintsBefore = x1.mints();
        long end = System.currentTimeMillis() + 8_000;
        int i = 0;
        while (System.currentTimeMillis() < end) {
            m1.put("n" + i, "v" + i);
            assertEquals("v" + i, x2.hz.<String, String>getMap("records").get("n" + i));
            i++;
            Thread.sleep(20);
        }
        assertTrue(x1.mints() - mintsBefore >= 3, "the sessions rotated onto new tokens during the run");
        assertEquals(removedBefore, x1.removed.get() + x2.removed.get(), "a rotation must not interrupt Hazelcast");
        assertEquals(2, x1.clusterSize());
    }

    @Test
    void nothingReachesHazelcastBeforeAdmission() throws Exception {
        Member x1 = new Member(X, 300, 60);
        x1.start(List.of());
        MemberPortBehaviorTest probe = new MemberPortBehaviorTest();
        ManagedChannel ch = probe.rawChannel(x1.portAddress);
        closers.add(ch::shutdownNow);
        // No token, another tenant's token, an expired token (minted by the
        // other side's fleet): each refused, its bytes never forwarded.
        String[] tokens = {
            null,
            remote.call("mint", Y, 300),
            remote.call("mint", X, -5),
        };
        for (String t : tokens) {
            MemberPortBehaviorTest.Raw r = new MemberPortBehaviorTest.Raw(ch, t);
            r.send(MemberPortBehaviorTest.open()).send(MemberPortBehaviorTest.data("HZC".getBytes()));
            assertTrue(!r.status(5_000).isOk(), "refused");
        }
        try (Socket plain = new Socket()) {
            plain.connect(x1.portAddress, 5_000);
            // Hazelcast's own opening bytes, as a member would send them in the clear.
            plain.getOutputStream().write("HZC".getBytes());
            plain.getOutputStream().write(new byte[64]);
            assertTrue(MemberPortBehaviorTest.dropped(plain, 12_000), "a plaintext member is dropped");
        }
        assertEquals(0, x1.relay.accepted.get(), "no connection may reach Hazelcast before admission");
        assertEquals(1, x1.clusterSize());
    }

    @Test
    void anExpiredTokenDropsTheMemberConnection() throws Exception {
        Member x1 = new Member(X, 300, 60);
        Member x2 = new Member(X, 4, 1);
        pair(x1, x2);
        // Each seeds the other: the address it reaches the other at.
        startTogether(x1, List.of(x2.publicAddress.get()), x2, List.of(x1.publicAddress.get()));
        MemberPortBehaviorTest.waitFor("the cluster", 60_000, () -> x1.clusterSize() == 2);
        // x2's identity stops renewing: its connections into x1 must be cut
        // when the token in hand expires, and new ones refused.
        x2.neverRenew();
        MemberPortBehaviorTest.waitFor("x2's connections into x1 to be dropped", 15_000,
                () -> x1.relay.open.get() == 0);
        MemberPortBehaviorTest.waitFor("x2 to be removed from x1's cluster", 30_000, () -> x1.clusterSize() == 1);
    }
}
