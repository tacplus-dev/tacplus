// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package com.example.grid.model.chassis.memberport;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.hazelcast.cluster.MembershipEvent;
import com.hazelcast.cluster.MembershipListener;
import com.hazelcast.config.Config;
import com.hazelcast.config.JoinConfig;
import com.hazelcast.config.MemberAddressProviderConfig;
import com.hazelcast.config.NetworkConfig;
import com.hazelcast.core.Hazelcast;
import com.hazelcast.core.HazelcastInstance;
import com.hazelcast.map.IMap;
import com.hazelcast.spi.MemberAddressProvider;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.util.ArrayList;
import java.util.List;
import java.util.Properties;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;
import javax.net.ssl.SSLSocket;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

/**
 * Two tenants' Hazelcast members through the generated member port
 * (tacplus#212), in-process. Hazelcast binds to loopback only; every member
 * reaches another through its own member port's {@code forward}, and is
 * reached through the other's {@code listen}. Each member port delivers to a
 * counting relay in front of Hazelcast, so the test sees exactly what reached
 * Hazelcast. All members share one cluster name: isolation is the port's.
 */
class HazelcastMemberPortTest {
    static final String X = MemberPortBehaviorTest.X;
    static final String Y = MemberPortBehaviorTest.Y;

    MemberPortBehaviorTest.Fleet fleet;
    final List<AutoCloseable> closers = new ArrayList<>();

    @BeforeEach
    void setUp() throws Exception {
        fleet = new MemberPortBehaviorTest.Fleet();
    }

    @AfterEach
    void tearDown() {
        for (int i = closers.size() - 1; i >= 0; i--) {
            try {
                closers.get(i).close();
            } catch (Exception ignored) {
                // Best effort.
            }
        }
    }

    /** A loopback TCP relay that counts what reaches the protocol behind it. */
    static final class Relay implements AutoCloseable {
        final ServerSocket socket;
        final InetSocketAddress target;
        final AtomicInteger accepted = new AtomicInteger();
        final AtomicInteger open = new AtomicInteger();

        Relay(InetSocketAddress target) throws IOException {
            this.target = target;
            this.socket = new ServerSocket(0, 64, InetAddress.getLoopbackAddress());
            Thread t = new Thread(this::loop, "relay");
            t.setDaemon(true);
            t.start();
        }

        InetSocketAddress address() {
            return (InetSocketAddress) socket.getLocalSocketAddress();
        }

        private void loop() {
            while (!socket.isClosed()) {
                try {
                    Socket in = socket.accept();
                    accepted.incrementAndGet();
                    open.incrementAndGet();
                    Socket out = new Socket();
                    out.connect(target, 5_000);
                    AtomicInteger halves = new AtomicInteger();
                    Runnable done = () -> {
                        if (halves.incrementAndGet() == 2) {
                            open.decrementAndGet();
                            closeQuietly(in);
                            closeQuietly(out);
                        }
                    };
                    pipe(in, out, done);
                    pipe(out, in, done);
                } catch (IOException e) {
                    return;
                }
            }
        }

        private static void pipe(Socket from, Socket to, Runnable done) {
            Thread t = new Thread(() -> {
                byte[] buf = new byte[16 * 1024];
                try {
                    InputStream in = from.getInputStream();
                    OutputStream out = to.getOutputStream();
                    int n;
                    while ((n = in.read(buf)) >= 0) {
                        out.write(buf, 0, n);
                    }
                    to.shutdownOutput();
                } catch (IOException ignored) {
                    closeQuietly(from);
                    closeQuietly(to);
                } finally {
                    done.run();
                }
            }, "relay-pipe");
            t.setDaemon(true);
            t.start();
        }

        @Override
        public void close() throws IOException {
            socket.close();
        }
    }

    static void closeQuietly(Socket s) {
        try {
            s.close();
        } catch (IOException ignored) {
            // Best effort.
        }
    }

    static int freePort() throws IOException {
        try (ServerSocket s = new ServerSocket(0, 1, InetAddress.getLoopbackAddress())) {
            return s.getLocalPort();
        }
    }

    /** One member: its Hazelcast port, relay and member port. */
    final class Member {
        final String namespace;
        final MemberPortBehaviorTest.Minter minter;
        final int hzPort;
        final Relay relay;
        final MemberPort port;
        final InetSocketAddress portAddress;
        final AtomicReference<InetSocketAddress> publicAddress = new AtomicReference<>();
        final AtomicInteger removed = new AtomicInteger();
        HazelcastInstance hz;

        Member(String namespace, long ttl, long renewBefore) throws Exception {
            this.namespace = namespace;
            this.minter = new MemberPortBehaviorTest.Minter(fleet, namespace, ttl, renewBefore);
            this.hzPort = freePort();
            InetSocketAddress hzAddress = new InetSocketAddress(InetAddress.getLoopbackAddress(), hzPort);
            this.relay = new Relay(hzAddress);
            closers.add(relay);
            AtomicReference<MemberPort.Material> cert = new AtomicReference<>(fleet.leaf(namespace, MemberPortBehaviorTest.SVC, "Ed25519"));
            this.port = new MemberPort(MemberPortBehaviorTest.SVC, MemberPortBehaviorTest.TD, List.of(fleet.ca),
                    minter, fleet.jwks::get, cert::get);
            closers.add(port);
            this.portAddress = port.listen(new InetSocketAddress(InetAddress.getLoopbackAddress(), 0), relay.address());
        }

        /** Starts Hazelcast, joining {@code seeds} (the loopback forwards to the other members). */
        void start(List<InetSocketAddress> seeds) {
            Config c = new Config();
            c.setClusterName("grid");
            Properties p = c.getProperties();
            p.setProperty("hazelcast.logging.type", "none");
            p.setProperty("hazelcast.phone.home.enabled", "false");
            p.setProperty("hazelcast.max.no.heartbeat.seconds", "8");
            p.setProperty("hazelcast.max.join.seconds", "10");
            p.setProperty("hazelcast.heartbeat.interval.seconds", "1");
            p.setProperty("hazelcast.merge.first.run.delay.seconds", "5");
            p.setProperty("hazelcast.merge.next.run.delay.seconds", "5");
            NetworkConfig n = c.getNetworkConfig();
            n.setPort(hzPort).setPortAutoIncrement(false);
            MemberAddressProviderConfig map = n.getMemberAddressProviderConfig();
            map.setEnabled(true);
            map.setImplementation(new MemberAddressProvider() {
                @Override
                public InetSocketAddress getBindAddress() {
                    return new InetSocketAddress(InetAddress.getLoopbackAddress(), hzPort);
                }

                @Override
                public InetSocketAddress getBindAddress(com.hazelcast.instance.EndpointQualifier q) {
                    return getBindAddress();
                }

                @Override
                public InetSocketAddress getPublicAddress() {
                    InetSocketAddress pub = publicAddress.get();
                    return pub != null ? pub : getBindAddress();
                }

                @Override
                public InetSocketAddress getPublicAddress(com.hazelcast.instance.EndpointQualifier q) {
                    return getPublicAddress();
                }
            });
            JoinConfig j = n.getJoin();
            j.getMulticastConfig().setEnabled(false);
            j.getAutoDetectionConfig().setEnabled(false);
            j.getTcpIpConfig().setEnabled(true).setConnectionTimeoutSeconds(3);
            for (InetSocketAddress s : seeds) {
                j.getTcpIpConfig().addMember(s.getHostString() + ":" + s.getPort());
            }
            hz = Hazelcast.newHazelcastInstance(c);
            hz.getCluster().addMembershipListener(new MembershipListener() {
                @Override
                public void memberAdded(MembershipEvent e) {}

                @Override
                public void memberRemoved(MembershipEvent e) {
                    removed.incrementAndGet();
                }
            });
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

    /**
     * Wires two members to each other: each one's public address is the
     * other's forward to its member port (the MemberAddressProvider glue a
     * service owns, here as Stratum would write it).
     */
    void pair(Member a, Member b) throws Exception {
        InetSocketAddress aToB = a.port.forward(b.portAddress);
        InetSocketAddress bToA = b.port.forward(a.portAddress);
        b.publicAddress.set(aToB);
        a.publicAddress.set(bToA);
    }

    @Test
    void sameTenantMembersFormAClusterAndOtherTenantsCannotJoin() throws Exception {
        // Three-second tokens renewed with two seconds left: the cluster lives
        // through several in-band refreshes during the test.
        Member x1 = new Member(X, 3, 2);
        Member x2 = new Member(X, 3, 2);
        Member y1 = new Member(Y, 300, 60);
        pair(x1, x2);
        InetSocketAddress yToX1 = y1.port.forward(x1.portAddress);
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

        // Mid-stream refresh: eight more seconds of traffic spanning several
        // token lifetimes, with no member ever dropping out.
        int removedBefore = x1.removed.get() + x2.removed.get();
        int mintsBefore = x1.minter.mints.get();
        long end = System.currentTimeMillis() + 8_000;
        int i = 0;
        while (System.currentTimeMillis() < end) {
            m1.put("n" + i, "v" + i);
            assertEquals("v" + i, x2.hz.<String, String>getMap("records").get("n" + i));
            i++;
            Thread.sleep(20);
        }
        assertTrue(x1.minter.mints.get() - mintsBefore >= 3, "tokens were refreshed in-band during the run");
        assertEquals(removedBefore, x1.removed.get() + x2.removed.get(), "a refresh must not interrupt Hazelcast");
        assertEquals(2, x1.clusterSize());
    }

    @Test
    void nothingReachesHazelcastBeforeAValidAuth() throws Exception {
        Member x1 = new Member(X, 300, 60);
        x1.start(List.of());
        MemberPortBehaviorTest probe = new MemberPortBehaviorTest();
        probe.fleet = fleet;
        // No token, another tenant's token, an expired token, a plaintext peer.
        byte[][] attempts = {
            MemberPortBehaviorTest.frameBytes(MemberPort.FRAME_DATA, "HZC".getBytes()),
            MemberPortBehaviorTest.frameBytes(MemberPort.FRAME_AUTH, MemberPortBehaviorTest.auth(
                    fleet.mint(MemberPortBehaviorTest.SVC, Y, MemberPortBehaviorTest.AUD, 300))),
            MemberPortBehaviorTest.frameBytes(MemberPort.FRAME_AUTH, MemberPortBehaviorTest.auth(
                    fleet.mint(MemberPortBehaviorTest.SVC, X, MemberPortBehaviorTest.AUD, -5))),
        };
        for (byte[] a : attempts) {
            try (SSLSocket s = probe.rawTls(x1.portAddress, true)) {
                s.getOutputStream().write(a);
                s.getOutputStream().write(MemberPortBehaviorTest.frameBytes(MemberPort.FRAME_DATA, "HZC".getBytes()));
                s.getOutputStream().flush();
                assertTrue(MemberPortBehaviorTest.dropped(s, 5_000));
            } catch (IOException dropped) {
                // Dropped while writing: as good.
            }
        }
        try (Socket plain = new Socket()) {
            plain.connect(x1.portAddress, 5_000);
            // Hazelcast's own opening bytes, as a member would send them in the clear.
            plain.getOutputStream().write("HZC".getBytes());
            plain.getOutputStream().write(new byte[64]);
            assertTrue(MemberPortBehaviorTest.dropped(plain, 12_000), "a plaintext member is dropped");
        }
        assertEquals(0, x1.relay.accepted.get(), "no connection may reach Hazelcast before a valid AUTH");
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
        x2.minter.neverRenew = true;
        MemberPortBehaviorTest.waitFor("x2's connections into x1 to be dropped", 15_000,
                () -> x1.relay.open.get() == 0);
        MemberPortBehaviorTest.waitFor("x2 to be removed from x1's cluster", 30_000, () -> x1.clusterSize() == 1);
    }
}
