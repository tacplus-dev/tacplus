// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package com.example.grid.model.chassis.memberport;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.example.grid.model.chassis.jwk.ImmutableJwk;
import com.example.grid.model.chassis.key.ImmutableKey;
import com.example.grid.model.chassis.key.KeyHelpers;
import com.example.grid.model.chassis.token.TokenWrapper;
import com.example.grid.model.types.sys.jwk.JwkOuterClass;
import com.example.grid.model.types.sys.key.KeyOuterClass;
import java.io.DataInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.math.BigInteger;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.KeyStore;
import java.security.cert.X509Certificate;
import java.security.spec.ECGenParameterSpec;
import java.util.ArrayList;
import java.util.Base64;
import java.util.Date;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicReference;
import javax.net.ssl.KeyManagerFactory;
import javax.net.ssl.SSLContext;
import javax.net.ssl.SSLParameters;
import javax.net.ssl.SSLServerSocket;
import javax.net.ssl.SSLSocket;
import javax.net.ssl.TrustManagerFactory;
import org.bouncycastle.asn1.x500.X500Name;
import org.bouncycastle.asn1.x509.BasicConstraints;
import org.bouncycastle.asn1.x509.Extension;
import org.bouncycastle.asn1.x509.GeneralName;
import org.bouncycastle.asn1.x509.GeneralNames;
import org.bouncycastle.cert.X509v3CertificateBuilder;
import org.bouncycastle.cert.jcajce.JcaX509CertificateConverter;
import org.bouncycastle.cert.jcajce.JcaX509v3CertificateBuilder;
import org.bouncycastle.operator.jcajce.JcaContentSignerBuilder;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.condition.EnabledIfEnvironmentVariable;

/**
 * The generated member port (tacplus#212), driven over real sockets in-process:
 * memberport-peer ↔ member port ↔ member port ↔ memberport-peer, with no
 * Hazelcast. The fleet is faked: a kernel signing key and its JWK, a CA (the
 * fleet trust bundle) and serving certificates with SPIFFE SANs.
 */
class MemberPortBehaviorTest {
    static final String TD = "example.org";
    static final String SVC = "grid";
    static final String AUD = MemberPort.audienceFor(SVC);
    static final String X = "/tenants/aaaaaaaa";
    static final String Y = "/tenants/bbbbbbbb";
    static final int MB = 1024 * 1024;

    // ---- The fake fleet ----

    static final class Fleet {
        final ImmutableKey kernel;
        final Map<String, ImmutableJwk> jwks = new ConcurrentHashMap<>();
        final KeyPair caKeys;
        final X509Certificate ca;
        final AtomicLong serial = new AtomicLong(100);

        Fleet() throws Exception {
            kernel = ImmutableKey.from(KeyHelpers.newEd25519Key());
            jwks.put(kernel.getProto().getId(), ImmutableJwk.from(jwkFor(kernel)));
            caKeys = ecKeys();
            X500Name name = new X500Name("CN=fleet-ca");
            X509v3CertificateBuilder b = new JcaX509v3CertificateBuilder(name, BigInteger.ONE,
                    new Date(System.currentTimeMillis() - 60_000), new Date(System.currentTimeMillis() + 86_400_000L),
                    name, caKeys.getPublic());
            b.addExtension(Extension.basicConstraints, true, new BasicConstraints(true));
            ca = new JcaX509CertificateConverter().getCertificate(
                    b.build(new JcaContentSignerBuilder("SHA256withECDSA").build(caKeys.getPrivate())));
        }

        String mint(String service, String namespace, String audience, long ttlSeconds) {
            return mint(kernel, service, namespace, audience, ttlSeconds);
        }

        String mint(ImmutableKey key, String service, String namespace, String audience, long ttlSeconds) {
            TokenWrapper tok = TokenWrapper.create()
                    .setIssuer("kernel")
                    .setAudience(audience)
                    .setSubType("service")
                    .setSubject(service)
                    .setNamespace(namespace)
                    .setExpiration(System.currentTimeMillis() / 1000 + ttlSeconds);
            tok.sign(key);
            return tok.getBase64();
        }

        /** A serving certificate naming {@code spiffe://example.org<namespace>/services/<service>}. */
        MemberPort.Material leaf(String namespace, String service, String keyType) throws Exception {
            KeyPair keys = "Ed25519".equals(keyType) ? KeyPairGenerator.getInstance("Ed25519").generateKeyPair() : ecKeys();
            X509v3CertificateBuilder b = new JcaX509v3CertificateBuilder(new X500Name("CN=fleet-ca"),
                    BigInteger.valueOf(serial.incrementAndGet()),
                    new Date(System.currentTimeMillis() - 60_000), new Date(System.currentTimeMillis() + 86_400_000L),
                    new X500Name("CN=" + service), keys.getPublic());
            b.addExtension(Extension.subjectAlternativeName, false, new GeneralNames(
                    new GeneralName(GeneralName.uniformResourceIdentifier,
                            "spiffe://" + TD + namespace + "/services/" + service)));
            X509Certificate cert = new JcaX509CertificateConverter().getCertificate(
                    b.build(new JcaContentSignerBuilder("SHA256withECDSA").build(caKeys.getPrivate())));
            return new MemberPort.Material(new X509Certificate[] {cert, ca}, keys.getPrivate());
        }
    }

    static KeyPair ecKeys() throws Exception {
        KeyPairGenerator g = KeyPairGenerator.getInstance("EC");
        g.initialize(new ECGenParameterSpec("secp256r1"));
        return g.generateKeyPair();
    }

    static JwkOuterClass.Jwk jwkFor(ImmutableKey key) {
        byte[] spki = key.getPublicKey().getEncoded();
        byte[] raw = new byte[32];
        System.arraycopy(spki, spki.length - 32, raw, 0, 32);
        return JwkOuterClass.Jwk.newBuilder()
                .setKid(key.getProto().getId())
                .setKty(KeyOuterClass.KeyType.KEY_TYPE_OKP)
                .setAlg(key.getAlg())
                .setCrv("Ed25519")
                .setX(Base64.getUrlEncoder().withoutPadding().encodeToString(raw))
                .build();
    }

    /**
     * A member's token source, like the ServiceManager's: a cached token,
     * renewed once fewer than {@code renewBefore} seconds remain. It can be
     * made to stall (the mint hangs) or to never renew.
     */
    static final class Minter implements MemberPort.TokenSource {
        final Fleet fleet;
        final String namespace;
        final long ttl;
        final long renewBefore;
        final AtomicInteger mints = new AtomicInteger();
        volatile CountDownLatch stall;
        volatile boolean neverRenew;
        private String cached;

        Minter(Fleet fleet, String namespace, long ttl, long renewBefore) {
            this.fleet = fleet;
            this.namespace = namespace;
            this.ttl = ttl;
            this.renewBefore = renewBefore;
        }

        @Override
        public synchronized String token() throws Exception {
            if (cached != null) {
                long left = TokenWrapper.parse(cached).getExpiration() - System.currentTimeMillis() / 1000;
                if (neverRenew || left > renewBefore) {
                    return cached;
                }
            }
            CountDownLatch s = stall;
            if (s != null) {
                s.await();
            }
            mints.incrementAndGet();
            cached = fleet.mint(SVC, namespace, AUD, ttl);
            return cached;
        }
    }

    // ---- Fixture plumbing ----

    Fleet fleet;
    final List<AutoCloseable> closers = new ArrayList<>();

    @BeforeEach
    void setUp() throws Exception {
        fleet = new Fleet();
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

    <T extends AutoCloseable> T track(T c) {
        closers.add(c);
        return c;
    }

    MemberPort member(MemberPort.TokenSource tokens, AtomicReference<MemberPort.Material> cert,
            MemberPort.JwkLookup jwks) throws Exception {
        return track(new MemberPort(SVC, TD, List.of(fleet.ca), tokens, jwks, cert::get));
    }

    MemberPort member(String namespace) throws Exception {
        return member(new Minter(fleet, namespace, 300, 60),
                new AtomicReference<>(fleet.leaf(namespace, SVC, "Ed25519")), fleet.jwks::get);
    }

    static InetSocketAddress loopback0() {
        return new InetSocketAddress(InetAddress.getLoopbackAddress(), 0);
    }

    /** peer client → A.forward → B.listen → peer server; returns the address the client dials. */
    InetSocketAddress tunnel(MemberPort a, MemberPort b, MemberportPeer.Server server) throws Exception {
        InetSocketAddress bAddr = b.listen(loopback0(), server.address());
        return a.forward(bAddr);
    }

    MemberportPeer.Server echo() throws IOException {
        return track(new MemberportPeer.Server(MemberportPeer.Mode.ECHO, 0, 0, 0));
    }

    MemberportPeer.Server push(long pushSeed, long pushBytes, long expectSeed) throws IOException {
        return track(new MemberportPeer.Server(MemberportPeer.Mode.PUSH, pushSeed, pushBytes, expectSeed));
    }

    static void waitFor(String what, long timeoutMillis, java.util.function.BooleanSupplier cond) throws InterruptedException {
        long deadline = System.currentTimeMillis() + timeoutMillis;
        while (!cond.getAsBoolean()) {
            if (System.currentTimeMillis() > deadline) {
                throw new AssertionError("timed out waiting for " + what);
            }
            Thread.sleep(20);
        }
    }

    // ---- Raw member-protocol client (for faults) ----

    SSLSocket rawTls(InetSocketAddress addr, boolean alpn) throws Exception {
        KeyStore ts = KeyStore.getInstance(KeyStore.getDefaultType());
        ts.load(null, null);
        ts.setCertificateEntry("ca", fleet.ca);
        TrustManagerFactory tmf = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm());
        tmf.init(ts);
        SSLContext ctx = SSLContext.getInstance("TLSv1.3");
        ctx.init(null, tmf.getTrustManagers(), null);
        SSLSocket s = (SSLSocket) ctx.getSocketFactory().createSocket();
        if (alpn) {
            SSLParameters p = s.getSSLParameters();
            p.setApplicationProtocols(new String[] {MemberPort.ALPN});
            s.setSSLParameters(p);
        }
        s.connect(addr, 5_000);
        s.setSoTimeout(10_000);
        s.startHandshake();
        return s;
    }

    static void frame(OutputStream out, int type, byte[] payload) throws IOException {
        frame(out, type, payload.length, payload);
    }

    static void frame(OutputStream out, int type, int declaredLength, byte[] payload) throws IOException {
        out.write(type);
        out.write(declaredLength >>> 24);
        out.write(declaredLength >>> 16);
        out.write(declaredLength >>> 8);
        out.write(declaredLength);
        out.write(payload);
        out.flush();
    }

    static byte[] auth(String token) {
        return token.getBytes(StandardCharsets.UTF_8);
    }

    /** Reads until the peer drops the connection; true when it did within the timeout. */
    static boolean dropped(Socket s, long timeoutMillis) throws IOException {
        s.setSoTimeout((int) timeoutMillis);
        InputStream in = s.getInputStream();
        byte[] buf = new byte[4096];
        try {
            while (in.read(buf) >= 0) {
                // Drain whatever arrived before the drop (the receiver's AUTH).
            }
            return true;
        } catch (java.net.SocketTimeoutException e) {
            return false;
        } catch (IOException e) {
            return true;
        }
    }

    // ==== Correctness ====

    @Test
    void bytesCrossIntactBothDirectionsFromOneByteToMultiMegabyte() throws Exception {
        MemberPort a = member(X);
        MemberPort b = member(X);
        for (long n : new long[] {1, 1000, 65_536, 65_537, 3L * MB + 17}) {
            MemberportPeer.Server server = push(2_000 + n, n, 1_000 + n);
            InetSocketAddress fwd = tunnel(a, b, server);
            MemberportPeer.Result client = MemberportPeer.exchange(fwd, 1_000 + n, n, 2_000 + n);
            assertTrue(client.ok(n), "client side of " + n + " bytes: " + client);
            waitFor("server result", 10_000, () -> server.results.size() == 1);
            assertTrue(server.results.get(1).ok(n), "server side of " + n + " bytes: " + server.results.get(1));
        }
        MemberportPeer.Server echo = echo();
        InetSocketAddress fwd = tunnel(a, b, echo);
        MemberportPeer.Result r = MemberportPeer.exchange(fwd, 77, 2L * MB, 77);
        assertTrue(r.ok(2L * MB), "echo: " + r);
    }

    @Test
    void manyConcurrentConnections() throws Exception {
        MemberPort a = member(X);
        MemberPort b = member(X);
        MemberportPeer.Server echo = echo();
        InetSocketAddress fwd = tunnel(a, b, echo);
        ExecutorService pool = Executors.newFixedThreadPool(32);
        try {
            List<Future<MemberportPeer.Result>> results = new ArrayList<>();
            for (int i = 0; i < 48; i++) {
                long seed = 10_000 + i;
                results.add(pool.submit(() -> MemberportPeer.exchange(fwd, seed, 256 * 1024 + seed, seed)));
            }
            for (int i = 0; i < results.size(); i++) {
                MemberportPeer.Result r = results.get(i).get(120, TimeUnit.SECONDS);
                assertTrue(r.ok(256 * 1024 + 10_000 + i), "connection " + i + ": " + r);
            }
            assertEquals(48, echo.accepted.get());
        } finally {
            pool.shutdownNow();
        }
    }

    @Test
    void halfCloseInEitherDirection() throws Exception {
        MemberPort a = member(X);
        MemberPort b = member(X);
        // The client half-closes at once; the server still pushes 1 MiB after.
        MemberportPeer.Server serverPushes = push(5, MB, 6);
        MemberportPeer.Result r1 = MemberportPeer.exchange(tunnel(a, b, serverPushes), 6, 0, 5);
        assertTrue(r1.ok(MB), "client half-closed first: " + r1);
        // The server half-closes at once; the client still sends 1 MiB after.
        MemberportPeer.Server serverDone = push(7, 0, 8);
        MemberportPeer.Result r2 = MemberportPeer.exchange(tunnel(a, b, serverDone), 8, MB, 7);
        assertTrue(r2.ok(0), "server half-closed first: " + r2);
        waitFor("server result", 10_000, () -> serverDone.results.size() == 1);
        assertTrue(serverDone.results.get(1).ok(MB), "server side: " + serverDone.results.get(1));
    }

    @Test
    void longStreamSpansSeveralInBandRefreshes() throws Exception {
        // 3-second tokens renewed with 2 seconds left: a new token every second.
        Minter am = new Minter(fleet, X, 3, 2);
        Minter bm = new Minter(fleet, X, 3, 2);
        MemberPort a = member(am, new AtomicReference<>(fleet.leaf(X, SVC, "Ed25519")), fleet.jwks::get);
        MemberPort b = member(bm, new AtomicReference<>(fleet.leaf(X, SVC, "Ed25519")), fleet.jwks::get);
        MemberportPeer.Server server = push(21, 600_000, 22);
        InetSocketAddress fwd = tunnel(a, b, server);
        long start = System.currentTimeMillis();
        // About 7 seconds of trickle: well past two token lifetimes.
        MemberportPeer.Result r = MemberportPeer.exchange(fwd, 22, 700_000, 21, 4096, 20, 0);
        long elapsed = System.currentTimeMillis() - start;
        assertTrue(r.ok(600_000), "client: " + r);
        waitFor("server result", 10_000, () -> server.results.size() == 1);
        assertTrue(server.results.get(1).ok(700_000), "server: " + server.results.get(1));
        assertTrue(elapsed > 6_000, "the stream must outlive two token lifetimes, took " + elapsed + "ms");
        assertEquals(1, server.accepted.get(), "the stream must not reconnect");
        assertTrue(am.mints.get() >= 4 && bm.mints.get() >= 4,
                "both sides refresh in-band: a=" + am.mints.get() + " b=" + bm.mints.get());
    }

    // ==== Backpressure ====

    @Test
    void slowReaderBoundsBufferingAndDropsNothing() throws Exception {
        MemberPort a = member(X);
        MemberPort b = member(X);
        MemberportPeer.Server server = push(31, 0, 32);
        server.readPauseMillis = 1_000_000; // reads one chunk, then stalls
        InetSocketAddress fwd = tunnel(a, b, server);
        long total = 64L * MB;
        AtomicLong written = new AtomicLong();
        try (Socket s = new Socket()) {
            s.connect(fwd, 5_000);
            Thread writer = new Thread(() -> {
                try {
                    MemberportPeer.Stream stream = new MemberportPeer.Stream(32);
                    byte[] buf = new byte[64 * 1024];
                    OutputStream out = s.getOutputStream();
                    while (written.get() < total) {
                        int len = (int) Math.min(buf.length, total - written.get());
                        stream.fill(buf, 0, len);
                        out.write(buf, 0, len);
                        written.addAndGet(len);
                    }
                    s.shutdownOutput();
                } catch (IOException ignored) {
                    // The assertion on the server's result reports it.
                }
            });
            writer.start();
            Thread.sleep(3_000);
            long buffered = written.get();
            Runtime rt = Runtime.getRuntime();
            assertTrue(buffered < 24L * MB,
                    "a stalled reader must stall the writer, not buffer: " + buffered + " bytes written of " + total);
            // Unstall the reader: every byte must still arrive, in order.
            server.readPauseMillis = 0;
            server.results.clear();
            writer.join(120_000);
            assertEquals(total, written.get());
            waitFor("server result", 120_000, () -> server.results.size() == 1);
            assertTrue(server.results.get(1).ok(total), "server: " + server.results.get(1));
            assertTrue(rt.totalMemory() - rt.freeMemory() < 512L * MB, "heap stays bounded");
        }
    }

    // ==== Faults: refused or dropped, nothing forwarded ====

    InetSocketAddress listenerOnX(MemberportPeer.Server behind) throws Exception {
        return member(X).listen(loopback0(), behind.address());
    }

    void assertRefusedBeforeForwarding(String what, byte[]... frames) throws Exception {
        MemberportPeer.Server behind = echo();
        InetSocketAddress port = listenerOnX(behind);
        try (SSLSocket s = rawTls(port, true)) {
            OutputStream out = s.getOutputStream();
            try {
                for (byte[] f : frames) {
                    out.write(f);
                }
                out.flush();
            } catch (IOException ignored) {
                // Already dropped.
            }
            assertTrue(dropped(s, 5_000), what + ": the connection must be dropped");
        }
        Thread.sleep(200);
        assertEquals(0, behind.accepted.get(), what + ": nothing may reach the peer behind the port");
    }

    static byte[] frameBytes(int type, int declaredLength, byte[] payload) throws IOException {
        java.io.ByteArrayOutputStream b = new java.io.ByteArrayOutputStream();
        frame(b, type, declaredLength, payload);
        return b.toByteArray();
    }

    static byte[] frameBytes(int type, byte[] payload) throws IOException {
        return frameBytes(type, payload.length, payload);
    }

    @Test
    void tokenOfAnotherTenantIsRefused() throws Exception {
        assertRefusedBeforeForwarding("tenant Y's token",
                frameBytes(MemberPort.FRAME_AUTH, auth(fleet.mint(SVC, Y, AUD, 300))),
                frameBytes(MemberPort.FRAME_DATA, new byte[] {1, 2, 3}));
    }

    @Test
    void noTokenIsRefused() throws Exception {
        assertRefusedBeforeForwarding("DATA before AUTH", frameBytes(MemberPort.FRAME_DATA, new byte[] {1, 2, 3}));
        assertRefusedBeforeForwarding("CLOSE_WRITE before AUTH", frameBytes(MemberPort.FRAME_CLOSE_WRITE, new byte[0]));
    }

    @Test
    void expiredTokenIsRefused() throws Exception {
        // Ten seconds past: inside the JWT clock-skew window, still refused.
        assertRefusedBeforeForwarding("expired token",
                frameBytes(MemberPort.FRAME_AUTH, auth(fleet.mint(SVC, X, AUD, -10))));
    }

    @Test
    void wrongAudienceServiceOrSigningKeyIsRefused() throws Exception {
        assertRefusedBeforeForwarding("gRPC audience", frameBytes(MemberPort.FRAME_AUTH, auth(fleet.mint(SVC, X, SVC, 300))));
        assertRefusedBeforeForwarding("another service",
                frameBytes(MemberPort.FRAME_AUTH, auth(fleet.mint("other", X, AUD, 300))));
        ImmutableKey rogue = ImmutableKey.from(KeyHelpers.newEd25519Key());
        assertRefusedBeforeForwarding("unknown signing key",
                frameBytes(MemberPort.FRAME_AUTH, auth(fleet.mint(rogue, SVC, X, AUD, 300))));
        assertRefusedBeforeForwarding("garbage token", frameBytes(MemberPort.FRAME_AUTH, auth("not-a-token")));
    }

    @Test
    void garbledOrOversizedFramesAreRefused() throws Exception {
        assertRefusedBeforeForwarding("unknown frame type", frameBytes(9, new byte[] {1}));
        assertRefusedBeforeForwarding("oversized AUTH", frameBytes(MemberPort.FRAME_AUTH, 1 << 20, new byte[64]));
        assertRefusedBeforeForwarding("empty AUTH", frameBytes(MemberPort.FRAME_AUTH, new byte[0]));
    }

    @Test
    void plaintextAndNonMemberTlsPeersAreRefused() throws Exception {
        MemberportPeer.Server behind = echo();
        InetSocketAddress port = listenerOnX(behind);
        try (Socket plain = new Socket()) {
            plain.connect(port, 5_000);
            OutputStream out = plain.getOutputStream();
            out.write(frameBytes(MemberPort.FRAME_AUTH, auth(fleet.mint(SVC, X, AUD, 300))));
            out.flush();
            assertTrue(dropped(plain, 5_000), "a plaintext peer must be dropped");
        }
        assertThrows(IOException.class, () -> {
            try (SSLSocket noAlpn = rawTls(port, false)) {
                OutputStream out = noAlpn.getOutputStream();
                out.write(frameBytes(MemberPort.FRAME_AUTH, auth(fleet.mint(SVC, X, AUD, 300))));
                out.flush();
                if (noAlpn.getInputStream().read() < 0) {
                    throw new IOException("dropped");
                }
            }
        }, "a TLS peer without the member ALPN must be dropped");
        Thread.sleep(200);
        assertEquals(0, behind.accepted.get(), "nothing may reach the peer behind the port");
    }

    @Test
    void garbledFrameMidStreamDropsAndForwardsNothingAfterIt() throws Exception {
        MemberportPeer.Server behind = echo();
        InetSocketAddress port = listenerOnX(behind);
        try (SSLSocket s = rawTls(port, true)) {
            OutputStream out = s.getOutputStream();
            frame(out, MemberPort.FRAME_AUTH, auth(fleet.mint(SVC, X, AUD, 300)));
            DataInputStream in = new DataInputStream(s.getInputStream());
            assertEquals(MemberPort.FRAME_AUTH, in.readUnsignedByte(), "the receiver answers with its own AUTH");
            byte[] theirs = new byte[in.readInt()];
            in.readFully(theirs);
            frame(out, MemberPort.FRAME_DATA, new byte[] {1, 2, 3, 4});
            assertEquals(MemberPort.FRAME_DATA, in.readUnsignedByte(), "echoed DATA");
            assertEquals(4, in.readInt());
            byte[] echoed = new byte[4];
            in.readFully(echoed);
            assertArrayEquals(new byte[] {1, 2, 3, 4}, echoed);
            frame(out, MemberPort.FRAME_DATA, MemberPort.MAX_DATA_BYTES + 1, new byte[0]);
            assertTrue(dropped(s, 5_000), "an oversized DATA frame drops the connection");
        }
        assertEquals(4, behind.bytesReceived.get(), "only the bytes before the bad frame were forwarded");
    }

    @Test
    void expiredTokenWithoutRefreshDropsTheConnection() throws Exception {
        MemberportPeer.Server behind = echo();
        InetSocketAddress port = listenerOnX(behind);
        try (SSLSocket s = rawTls(port, true)) {
            OutputStream out = s.getOutputStream();
            frame(out, MemberPort.FRAME_AUTH, auth(fleet.mint(SVC, X, AUD, 2)));
            long start = System.currentTimeMillis();
            assertTrue(dropped(s, 6_000), "a token that expires unrefreshed drops the connection");
            long after = System.currentTimeMillis() - start;
            assertTrue(after < 4_000, "dropped within the token's lifetime plus a beat, took " + after + "ms");
        }
        // And through a real member whose identity stops renewing.
        Minter stuck = new Minter(fleet, X, 2, 1);
        stuck.neverRenew = true;
        MemberPort a = member(stuck, new AtomicReference<>(fleet.leaf(X, SVC, "Ed25519")), fleet.jwks::get);
        MemberportPeer.Server echo = echo();
        InetSocketAddress fwd = tunnel(a, member(X), echo);
        MemberportPeer.Result r = MemberportPeer.exchange(fwd, 41, 400_000, 41, 2048, 20, 0);
        assertTrue(!r.ok(400_000), "the stream must be cut when the token lapses: " + r);
        assertTrue(r.firstMismatch < 0, "what did arrive is intact: " + r);
    }

    @Test
    void refreshedTokenOfAnotherTenantDropsTheConnection() throws Exception {
        MemberportPeer.Server behind = echo();
        InetSocketAddress port = listenerOnX(behind);
        try (SSLSocket s = rawTls(port, true)) {
            OutputStream out = s.getOutputStream();
            frame(out, MemberPort.FRAME_AUTH, auth(fleet.mint(SVC, X, AUD, 300)));
            frame(out, MemberPort.FRAME_AUTH, auth(fleet.mint(SVC, Y, AUD, 300)));
            frame(out, MemberPort.FRAME_DATA, new byte[] {9});
            assertTrue(dropped(s, 5_000), "a refresh into another tenant drops the connection");
        }
        assertEquals(0, behind.bytesReceived.get(), "nothing after the bad refresh is forwarded");
    }

    // ==== Failure modes ====

    @Test
    void stalledMintDropsTheConnectionAtExpiry() throws Exception {
        Minter am = new Minter(fleet, X, 3, 2);
        MemberPort a = member(am, new AtomicReference<>(fleet.leaf(X, SVC, "Ed25519")), fleet.jwks::get);
        MemberportPeer.Server echo = echo();
        InetSocketAddress fwd = tunnel(a, member(X), echo);
        am.token(); // the first token is in hand
        am.stall = new CountDownLatch(1);
        try {
            long start = System.currentTimeMillis();
            MemberportPeer.Result r = MemberportPeer.exchange(fwd, 51, 2_000_000, 51, 2048, 10, 0);
            long elapsed = System.currentTimeMillis() - start;
            assertTrue(!r.ok(2_000_000) && r.firstMismatch < 0, "cut, intact: " + r);
            assertTrue(elapsed < 8_000, "dropped at the token's expiry, took " + elapsed + "ms");
        } finally {
            am.stall.countDown();
        }
    }

    @Test
    void unreachableJwksRefusesEveryoneUntilKeysArrive() throws Exception {
        Map<String, ImmutableJwk> none = new ConcurrentHashMap<>();
        MemberPort b = member(new Minter(fleet, X, 300, 60), new AtomicReference<>(fleet.leaf(X, SVC, "Ed25519")), none::get);
        MemberportPeer.Server echo = echo();
        InetSocketAddress fwd = member(X).forward(b.listen(loopback0(), echo.address()));
        MemberportPeer.Result refused = MemberportPeer.exchange(fwd, 61, 1000, 61);
        assertTrue(!refused.ok(1000), "no JWKS, no admission: " + refused);
        assertEquals(0, echo.accepted.get());
        none.putAll(fleet.jwks);
        MemberportPeer.Result admitted = MemberportPeer.exchange(fwd, 62, 1000, 62);
        assertTrue(admitted.ok(1000), "admitted once the keys arrive: " + admitted);
    }

    @Test
    void certificateRotatesMidStream() throws Exception {
        AtomicReference<MemberPort.Material> bCert = new AtomicReference<>(fleet.leaf(X, SVC, "Ed25519"));
        MemberPort b = member(new Minter(fleet, X, 300, 60), bCert, fleet.jwks::get);
        MemberportPeer.Server server = push(71, 300_000, 72);
        InetSocketAddress bAddr = b.listen(loopback0(), server.address());
        InetSocketAddress fwd = member(X).forward(bAddr);
        BigInteger before = presentedSerial(bAddr);
        ExecutorService pool = Executors.newSingleThreadExecutor();
        try {
            Future<MemberportPeer.Result> stream = pool.submit(() -> MemberportPeer.exchange(fwd, 72, 300_000, 71, 2048, 10, 0));
            Thread.sleep(500);
            bCert.set(fleet.leaf(X, SVC, "EC"));
            MemberportPeer.Result r = stream.get(60, TimeUnit.SECONDS);
            assertTrue(r.ok(300_000), "the stream in flight survives a rotation: " + r);
        } finally {
            pool.shutdownNow();
        }
        assertNotEquals(before, presentedSerial(bAddr), "a new connection gets the new certificate");
        MemberportPeer.Server echo = echo();
        MemberportPeer.Result after = MemberportPeer.exchange(member(X).forward(b.listen(loopback0(), echo.address())), 73, 5000, 73);
        assertTrue(after.ok(5000), "members still connect after the rotation: " + after);
    }

    @Test
    void sameKeyTypeRotationsKeepEveryHandshakeWorking() throws Exception {
        // EC to EC, several times, with many handshakes in between: a key of one
        // snapshot served with the chain of another fails the handshake only
        // when the algorithms match, so this is the rotation that would show it.
        AtomicReference<MemberPort.Material> bCert = new AtomicReference<>(fleet.leaf(X, SVC, "EC"));
        MemberPort b = member(new Minter(fleet, X, 300, 60), bCert, fleet.jwks::get);
        MemberportPeer.Server echo = echo();
        InetSocketAddress bAddr = b.listen(loopback0(), echo.address());
        InetSocketAddress fwd = member(X).forward(bAddr);
        for (int round = 0; round < 4; round++) {
            BigInteger expected = ((X509Certificate) bCert.get().chain[0]).getSerialNumber();
            assertEquals(expected, presentedSerial(bAddr), "round " + round + " serves the current certificate");
            for (int i = 0; i < 12; i++) {
                long seed = round * 100 + i;
                MemberportPeer.Result r = MemberportPeer.exchange(fwd, seed, 1000, seed);
                assertTrue(r.ok(1000), "round " + round + " connection " + i + ": " + r);
            }
            bCert.set(fleet.leaf(X, SVC, "EC"));
        }
    }

    @Test
    void aTricklingPeerIsDroppedAtTheAdmissionDeadline() throws Exception {
        MemberportPeer.Server behind = echo();
        InetSocketAddress port = listenerOnX(behind);
        try (SSLSocket s = rawTls(port, true)) {
            byte[] frame = frameBytes(MemberPort.FRAME_AUTH, auth(fleet.mint(SVC, X, AUD, 300)));
            OutputStream out = s.getOutputStream();
            long start = System.currentTimeMillis();
            boolean dropped = false;
            // One byte every 2 seconds: each read is quick, the whole is not.
            for (int i = 0; i < frame.length && System.currentTimeMillis() - start < 20_000; i++) {
                try {
                    out.write(frame[i]);
                    out.flush();
                } catch (IOException e) {
                    dropped = true;
                    break;
                }
                s.setSoTimeout(2_000);
                try {
                    if (s.getInputStream().read() < 0) {
                        dropped = true;
                        break;
                    }
                } catch (java.net.SocketTimeoutException stillOpen) {
                    // Expected while the deadline has not passed.
                } catch (IOException e) {
                    dropped = true;
                    break;
                }
            }
            long elapsed = System.currentTimeMillis() - start;
            assertTrue(dropped, "a peer that trickles its AUTH must be dropped");
            assertTrue(elapsed < 14_000, "dropped at the 10-second admission deadline, took " + elapsed + "ms");
        }
        assertEquals(0, behind.accepted.get(), "nothing reached the peer behind the port");
    }

    BigInteger presentedSerial(InetSocketAddress port) throws Exception {
        try (SSLSocket s = rawTls(port, true)) {
            return ((X509Certificate) s.getSession().getPeerCertificates()[0]).getSerialNumber();
        }
    }

    // ==== The sender checks the receiver ====

    @Test
    void receiverCertificateOfAnotherTenantIsRefusedBySender() throws Exception {
        // B holds tenant X's tokens but presents a certificate naming tenant Y.
        MemberPort b = member(new Minter(fleet, X, 300, 60), new AtomicReference<>(fleet.leaf(Y, SVC, "Ed25519")), fleet.jwks::get);
        MemberportPeer.Server echo = echo();
        InetSocketAddress fwd = member(X).forward(b.listen(loopback0(), echo.address()));
        MemberportPeer.Result r = MemberportPeer.exchange(fwd, 81, 1000, 81);
        assertTrue(!r.ok(1000) && r.received == 0, "refused at the handshake: " + r);
        assertEquals(0, echo.accepted.get());
        // A certificate of another service is refused the same way.
        MemberPort c = member(new Minter(fleet, X, 300, 60), new AtomicReference<>(fleet.leaf(X, "other", "EC")), fleet.jwks::get);
        MemberportPeer.Server echo2 = echo();
        MemberportPeer.Result r2 = MemberportPeer.exchange(member(X).forward(c.listen(loopback0(), echo2.address())), 82, 1000, 82);
        assertTrue(!r2.ok(1000) && r2.received == 0, "another service's certificate: " + r2);
        assertEquals(0, echo2.accepted.get());
    }

    @Test
    void receiverAuthOfAnotherTenantIsRefusedBySender() throws Exception {
        // A fake receiver with a valid tenant-X certificate that answers with
        // tenant Y's token: the sender must refuse it and send no DATA.
        MemberPort.Material cert = fleet.leaf(X, SVC, "EC");
        KeyStore ks = KeyStore.getInstance("PKCS12");
        ks.load(null, null);
        ks.setKeyEntry("k", cert.key, "pw".toCharArray(), cert.chain);
        KeyManagerFactory kmf = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm());
        kmf.init(ks, "pw".toCharArray());
        SSLContext ctx = SSLContext.getInstance("TLSv1.3");
        ctx.init(kmf.getKeyManagers(), null, null);
        AtomicInteger dataFrames = new AtomicInteger();
        CountDownLatch done = new CountDownLatch(1);
        try (SSLServerSocket server = (SSLServerSocket) ctx.getServerSocketFactory().createServerSocket(0, 8, InetAddress.getLoopbackAddress())) {
            SSLParameters p = server.getSSLParameters();
            p.setApplicationProtocols(new String[] {MemberPort.ALPN});
            server.setSSLParameters(p);
            Thread t = new Thread(() -> {
                try (SSLSocket s = (SSLSocket) server.accept()) {
                    DataInputStream in = new DataInputStream(s.getInputStream());
                    in.readUnsignedByte();
                    in.readFully(new byte[in.readInt()]);
                    frame(s.getOutputStream(), MemberPort.FRAME_AUTH, auth(fleet.mint(SVC, Y, AUD, 300)));
                    while (true) {
                        int type = in.readUnsignedByte();
                        in.readFully(new byte[in.readInt()]);
                        if (type == MemberPort.FRAME_DATA) {
                            dataFrames.incrementAndGet();
                        }
                    }
                } catch (IOException ignored) {
                    // The sender hung up.
                } finally {
                    done.countDown();
                }
            });
            t.setDaemon(true);
            t.start();
            InetSocketAddress fwd = member(X).forward((InetSocketAddress) server.getLocalSocketAddress());
            MemberportPeer.Result r = MemberportPeer.exchange(fwd, 91, 1000, 91);
            assertTrue(!r.ok(1000) && r.received == 0, "refused: " + r);
            assertTrue(done.await(5, TimeUnit.SECONDS), "the sender hangs up");
            assertEquals(0, dataFrames.get(), "the sender sends no DATA to a receiver it refused");
        }
    }

    @Test
    void twoTenantsMembersNeverConnect() throws Exception {
        MemberPort x1 = member(X);
        MemberPort x2 = member(X);
        MemberPort y1 = member(Y);
        MemberportPeer.Server behindX2 = echo();
        InetSocketAddress x2Port = x2.listen(loopback0(), behindX2.address());
        assertTrue(MemberportPeer.exchange(x1.forward(x2Port), 101, 5000, 101).ok(5000), "same tenant connects");
        MemberportPeer.Result cross = MemberportPeer.exchange(y1.forward(x2Port), 102, 5000, 102);
        assertTrue(!cross.ok(5000) && cross.received == 0, "tenant Y cannot reach tenant X: " + cross);
        assertEquals(1, behindX2.accepted.get(), "only tenant X's connection reached the peer");
    }

    @Test
    void targetAndLocalForwardMustBeLoopback() throws Exception {
        MemberPort a = member(X);
        assertThrows(IllegalArgumentException.class,
                () -> a.listen(loopback0(), new InetSocketAddress("192.0.2.1", 5701)));
        assertThrows(IllegalArgumentException.class,
                () -> a.forward(new InetSocketAddress(InetAddress.getLoopbackAddress(), 1), new InetSocketAddress("0.0.0.0", 0)));
    }

    // ==== Soak (opt-in): overhead against a direct connection ====

    @Test
    @EnabledIfEnvironmentVariable(named = "MEMBERPORT_SOAK", matches = "1")
    void soakOverheadAgainstDirect() throws Exception {
        long bytes = 256L * MB;
        MemberportPeer.Server direct = echo();
        long t0 = System.nanoTime();
        assertTrue(MemberportPeer.exchange(direct.address(), 1, bytes, 1, 64 * 1024, 0, 0).ok(bytes));
        long directNs = System.nanoTime() - t0;
        MemberportPeer.Server behind = echo();
        InetSocketAddress fwd = tunnel(member(X), member(X), behind);
        long t1 = System.nanoTime();
        assertTrue(MemberportPeer.exchange(fwd, 1, bytes, 1, 64 * 1024, 0, 0).ok(bytes));
        long tunnelNs = System.nanoTime() - t1;
        long rttDirect = rtt(direct.address());
        long rttTunnel = rtt(fwd);
        Runtime rt = Runtime.getRuntime();
        System.out.printf("memberport soak: echo %d MiB direct %.1f MiB/s, tunnelled %.1f MiB/s; 1-byte RTT direct %d us, tunnelled %d us; heap used %d MiB%n",
                bytes / MB, bytes / (double) MB / (directNs / 1e9), bytes / (double) MB / (tunnelNs / 1e9),
                rttDirect / 1000, rttTunnel / 1000, (rt.totalMemory() - rt.freeMemory()) / MB);
    }

    static long rtt(InetSocketAddress addr) throws Exception {
        try (Socket s = new Socket()) {
            s.connect(addr);
            s.setTcpNoDelay(true);
            OutputStream out = s.getOutputStream();
            InputStream in = s.getInputStream();
            long best = Long.MAX_VALUE;
            for (int i = 0; i < 200; i++) {
                long t = System.nanoTime();
                out.write(1);
                out.flush();
                in.read();
                best = Math.min(best, System.nanoTime() - t);
            }
            return best;
        }
    }
}
