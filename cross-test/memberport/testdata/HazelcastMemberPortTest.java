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
import io.grpc.ManagedChannel;
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
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

/**
 * Two tenants' Hazelcast members through the generated member port
 * (tacplus#212, #321), in-process. Hazelcast binds to loopback only; every member
 * reaches another through its own member port's {@code forward}, and is
 * reached through the other's {@code listen}. Each member port delivers to a
 * counting relay in front of Hazelcast, so the test sees exactly what reached
 * Hazelcast. All members share one cluster name: isolation is the port's.
 */
class HazelcastMemberPortTest {
    static final String X = MemberPortBehaviorTest.X;
    static final String Y = MemberPortBehaviorTest.Y;
    /** A member that sends no heartbeat for this long is removed. */
    static final long NO_HEARTBEAT_SECONDS = 8;
    static final long HEARTBEAT_SECONDS = 1;

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
        /** Where Hazelcast listens: moved when its first port was taken before it bound. */
        volatile InetSocketAddress target;
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
        /** Hazelcast's port: free when picked, and picked again if taken before Hazelcast binds it. */
        volatile int hzPort;
        final Relay relay;
        final MemberPort port;
        final InetSocketAddress portAddress;
        final AtomicReference<InetSocketAddress> publicAddress = new AtomicReference<>();
        final AtomicInteger removed = new AtomicInteger();
        /** Counted down by the next member removal this member sees. */
        volatile CountDownLatch removal = new CountDownLatch(1);
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

        /**
         * Starts Hazelcast, joining {@code seeds} (the loopback forwards to the
         * other members). A port picked free can be taken by another socket
         * before Hazelcast binds it: then it picks another and tries again.
         */
        void start(List<InetSocketAddress> seeds) {
<<<<<<< HEAD
            hz = startHazelcast(hzPort, publicAddress, removed, seeds);
=======
            for (int attempt = 1; ; attempt++) {
                try {
                    startOn(seeds);
                    return;
                } catch (com.hazelcast.core.HazelcastException e) {
                    if (attempt == 5 || !String.valueOf(e.getMessage()).contains("already in use")) {
                        throw e;
                    }
                    try {
                        hzPort = freePort();
                    } catch (IOException io) {
                        throw new java.io.UncheckedIOException(io);
                    }
                    relay.target = new InetSocketAddress(InetAddress.getLoopbackAddress(), hzPort);
                }
            }
        }

        private void startOn(List<InetSocketAddress> seeds) {
            Config c = new Config();
            c.setClusterName("grid");
            Properties p = c.getProperties();
            p.setProperty("hazelcast.logging.type", "none");
            p.setProperty("hazelcast.phone.home.enabled", "false");
            p.setProperty("hazelcast.max.no.heartbeat.seconds", String.valueOf(NO_HEARTBEAT_SECONDS));
            p.setProperty("hazelcast.max.join.seconds", "10");
            p.setProperty("hazelcast.heartbeat.interval.seconds", String.valueOf(HEARTBEAT_SECONDS));
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
                    removal.countDown();
                }
            });
>>>>>>> origin/main
            closers.add(hz::shutdown);
        }

        int clusterSize() {
            return hz.getCluster().getMembers().size();
        }
    }

    /**
     * Starts a Hazelcast member bound to loopback {@code hzPort}, advertising
     * {@code publicAddress} once set (else its bind address), joining
     * {@code seeds} and counting removed members into {@code removed}.
     */
    static HazelcastInstance startHazelcast(int hzPort, AtomicReference<InetSocketAddress> publicAddress,
            AtomicInteger removed, List<InetSocketAddress> seeds) {
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
        HazelcastInstance hz = Hazelcast.newHazelcastInstance(c);
        hz.getCluster().addMembershipListener(new MembershipListener() {
            @Override
            public void memberAdded(MembershipEvent e) {}

            @Override
            public void memberRemoved(MembershipEvent e) {
                removed.incrementAndGet();
            }
        });
        return hz;
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
        // through several session rotations during the test.
        Member x1 = new Member(X, 3, 2);
        Member x2 = new Member(X, 3, 2);
        Member y1 = new Member(Y, 300, 60);
        pair(x1, x2);
        InetSocketAddress yToX1 = y1.port.forward(x1.portAddress);
        // y1's public address is its bind address: no member of X reaches it.

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
        int mintsBefore = x1.minter.mints.get();
        long end = System.currentTimeMillis() + 8_000;
        int i = 0;
        while (System.currentTimeMillis() < end) {
            m1.put("n" + i, "v" + i);
            assertEquals("v" + i, x2.hz.<String, String>getMap("records").get("n" + i));
            i++;
            Thread.sleep(20);
        }
        assertTrue(x1.minter.mints.get() - mintsBefore >= 3, "the sessions rotated onto new tokens during the run");
        assertEquals(removedBefore, x1.removed.get() + x2.removed.get(), "a rotation must not interrupt Hazelcast");
        assertEquals(2, x1.clusterSize());
    }

    @Test
    void nothingReachesHazelcastBeforeAdmission() throws Exception {
        Member x1 = new Member(X, 300, 60);
        x1.start(List.of());
        MemberPortBehaviorTest probe = new MemberPortBehaviorTest();
        probe.fleet = fleet;
        ManagedChannel ch = probe.rawChannel(x1.portAddress);
        closers.add(ch::shutdownNow);
        // No token, another tenant's token, an expired token: each refused, its
        // bytes never forwarded.
        String[] tokens = {
            null,
            fleet.mint(MemberPortBehaviorTest.SVC, Y, MemberPortBehaviorTest.AUD, 300),
            fleet.mint(MemberPortBehaviorTest.SVC, X, MemberPortBehaviorTest.AUD, -5),
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
        // x2's identity stops renewing: every connection between the two must
        // be cut when the token in hand expires, whichever member opened it
        // (#465: x1's sessions into x2 checked x2's token only at their open),
        // and new ones refused.
        x1.removal = new CountDownLatch(1);
        x2.minter.neverRenew = true;
        long cut = (x2.minter.ttl + 2) * 1_000;
        MemberPortBehaviorTest.waitFor("x2's connections into x1 to be dropped", cut, () -> x1.relay.open.get() == 0);
        MemberPortBehaviorTest.waitFor("x1's connections into x2 to be dropped", cut, () -> x2.relay.open.get() == 0);
        // With no connection left, x1 stops hearing x2's heartbeats and removes
        // it within the no-heartbeat bound.
        assertTrue(x1.removal.await(NO_HEARTBEAT_SECONDS + 2 * HEARTBEAT_SECONDS, TimeUnit.SECONDS),
                "x1 must remove x2 within the no-heartbeat bound; x1 still sees " + x1.clusterSize() + " members");
        assertEquals(1, x1.clusterSize());
    }
}
