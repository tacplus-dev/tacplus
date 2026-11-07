// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package com.example.grid.model.chassis.memberport;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.example.grid.model.chassis.jwk.ImmutableJwk;
import com.example.grid.model.chassis.key.ImmutableKey;
import com.example.grid.model.chassis.key.KeyHelpers;
import com.example.grid.model.chassis.token.TokenWrapper;
import com.example.grid.model.types.sys.jwk.JwkOuterClass;
import com.example.grid.model.types.sys.key.KeyOuterClass;
import com.example.grid.model.types.sys.memberport.Memberport.MemberChunk;
import com.google.protobuf.ByteString;
import io.grpc.CallOptions;
import io.grpc.ClientCall;
import io.grpc.ManagedChannel;
import io.grpc.Metadata;
import io.grpc.Server;
import io.grpc.ServerCall;
import io.grpc.ServerCallHandler;
import io.grpc.ServerServiceDefinition;
import io.grpc.Status;
import io.grpc.netty.shaded.io.grpc.netty.GrpcSslContexts;
import io.grpc.netty.shaded.io.grpc.netty.NettyChannelBuilder;
import io.grpc.netty.shaded.io.grpc.netty.NettyServerBuilder;
import io.grpc.netty.shaded.io.netty.handler.ssl.SslContextBuilder;
import io.grpc.netty.shaded.io.netty.handler.ssl.SslProvider;
import io.grpc.netty.shaded.io.netty.handler.ssl.util.InsecureTrustManagerFactory;
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
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicReference;
import javax.net.ssl.SSLContext;
import javax.net.ssl.SSLParameters;
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
 * The generated member port (tacplus#212, on gRPC since #321), driven over
 * real sockets in-process: memberport-peer ↔ member port ↔ member port ↔
 * memberport-peer, with no Hazelcast, and hand-driven tunnel streams for what a
 * generated member port never sends. The fleet is faked: a kernel signing key and its JWK, a CA (the
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

    // ---- Raw member tunnel client (for faults) ----

    /** A TLS socket to a member port that trusts the fleet CA; {@code alpn} null offers none. */
    SSLSocket rawTls(InetSocketAddress addr, String alpn) throws Exception {
        KeyStore ts = KeyStore.getInstance(KeyStore.getDefaultType());
        ts.load(null, null);
        ts.setCertificateEntry("ca", fleet.ca);
        TrustManagerFactory tmf = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm());
        tmf.init(ts);
        SSLContext ctx = SSLContext.getInstance("TLSv1.3");
        ctx.init(null, tmf.getTrustManagers(), null);
        SSLSocket s = (SSLSocket) ctx.getSocketFactory().createSocket();
        if (alpn != null) {
            SSLParameters p = s.getSSLParameters();
            p.setApplicationProtocols(new String[] {alpn});
            s.setSSLParameters(p);
        }
        s.connect(addr, 5_000);
        s.setSoTimeout(10_000);
        s.startHandshake();
        return s;
    }

    /** A gRPC channel to a member port that skips the member port's own checks of the receiver. */
    ManagedChannel rawChannel(InetSocketAddress addr) throws Exception {
        ManagedChannel ch = NettyChannelBuilder.forAddress(addr)
                .sslContext(GrpcSslContexts.configure(SslContextBuilder.forClient(), SslProvider.JDK)
                        .trustManager(InsecureTrustManagerFactory.INSTANCE).build())
                .build();
        closers.add(ch::shutdownNow);
        return ch;
    }

    /** One member tunnel stream, driven by hand: what a peer that is not a generated member port can send. */
    static final class Raw {
        final ClientCall<MemberChunk, MemberChunk> call;
        final LinkedBlockingQueue<MemberChunk> received = new LinkedBlockingQueue<>();
        final CompletableFuture<Status> closed = new CompletableFuture<>();
        final CompletableFuture<Metadata> headers = new CompletableFuture<>();

        Raw(ManagedChannel ch, String token) {
            call = ch.newCall(MemberPort.OPEN_METHOD, CallOptions.DEFAULT);
            Metadata md = new Metadata();
            if (token != null) {
                md.put(MemberPort.AUTHORIZATION, "Bearer " + token);
            }
            call.start(new ClientCall.Listener<MemberChunk>() {
                @Override
                public void onHeaders(Metadata h) {
                    headers.complete(h);
                }

                @Override
                public void onMessage(MemberChunk m) {
                    received.add(m);
                }

                @Override
                public void onClose(Status status, Metadata trailers) {
                    closed.complete(status);
                }
            }, md);
            call.request(1_000_000);
        }

        synchronized Raw send(MemberChunk c) {
            call.sendMessage(c);
            return this;
        }

        synchronized void cancel() {
            call.cancel("test: cut off without last", null);
        }

        MemberChunk next() throws InterruptedException {
            MemberChunk m = received.poll(10, TimeUnit.SECONDS);
            if (m == null) {
                throw new AssertionError("no chunk within 10s; stream status " + closed.getNow(null));
            }
            return m;
        }

        Status status(long timeoutMillis) throws Exception {
            return closed.get(timeoutMillis, TimeUnit.MILLISECONDS);
        }
    }

    static MemberChunk open() {
        return MemberChunk.getDefaultInstance();
    }

    static MemberChunk join(String sessionId) {
        return MemberChunk.newBuilder().setSessionId(sessionId).build();
    }

    static MemberChunk data(byte[] b) {
        return MemberChunk.newBuilder().setPayload(ByteString.copyFrom(b)).build();
    }

    /** Opens a new session with {@code token} and returns its id. */
    static String openSession(Raw r) throws InterruptedException {
        MemberChunk first = r.send(open()).next();
        assertTrue(!first.getSessionId().isEmpty(), "the server's first chunk names the session: " + first);
        return first.getSessionId();
    }

    /** Reads until the peer drops the connection; true when it did within the timeout. */
    static boolean dropped(Socket s, long timeoutMillis) throws IOException {
        s.setSoTimeout((int) timeoutMillis);
        InputStream in = s.getInputStream();
        byte[] buf = new byte[4096];
        try {
            while (in.read(buf) >= 0) {
                // Drain whatever arrived before the drop.
            }
            return true;
        } catch (java.net.SocketTimeoutException e) {
            return false;
        } catch (IOException e) {
            return true;
        }
    }

    /**
     * The black-box client saw its connection reset: on a read, or on a write
     * when the writer met the RST first (a later read then sees EOF).
     */
    static boolean reset(MemberportPeer.Result r) {
        return r.error != null && (r.error.contains("reset") || r.error.contains("Broken pipe"));
    }

    /** The status of a refused stream: the code, and no reason text. */
    static void assertRefused(Status.Code code, Status status, String what) {
        assertEquals(code, status.getCode(), what + ": " + status);
        assertNull(status.getDescription(), what + ": a refusal carries no reason text");
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
    void byteSessionRunsAcrossSeveralTokenLifetimesBothWaysAtOnce() throws Exception {
        // 3-second tokens renewed with 2 seconds left: the client rotates at 75%
        // of each lifetime, about every 2.25 seconds.
        Minter am = new Minter(fleet, X, 3, 2);
        Minter bm = new Minter(fleet, X, 3, 2);
        MemberPort a = member(am, new AtomicReference<>(fleet.leaf(X, SVC, "Ed25519")), fleet.jwks::get);
        MemberPort b = member(bm, new AtomicReference<>(fleet.leaf(X, SVC, "Ed25519")), fleet.jwks::get);
        // Both directions trickle at once for about 8 seconds, so every cutover
        // has bytes in flight each way.
        MemberportPeer.Server server = track(new MemberportPeer.Server(MemberportPeer.Mode.PUSH, 21, 800_000, 22, 4096, 20));
        InetSocketAddress fwd = tunnel(a, b, server);
        long start = System.currentTimeMillis();
        MemberportPeer.Result r = MemberportPeer.exchange(fwd, 22, 800_000, 21, 4096, 20, 0);
        long elapsed = System.currentTimeMillis() - start;
        assertTrue(r.ok(800_000), "client: no loss, duplication or reordering: " + r);
        waitFor("server result", 10_000, () -> server.results.size() == 1);
        assertTrue(server.results.get(1).ok(800_000), "server: " + server.results.get(1));
        assertTrue(elapsed > 6_500, "the session must outlive two token lifetimes, took " + elapsed + "ms");
        assertEquals(1, server.accepted.get(), "one byte session: the target was dialled once");
        assertTrue(am.mints.get() >= 3, "the client rotated onto new tokens: " + am.mints.get() + " mints");
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

    // ==== Refusals: fail fast, nothing forwarded ====

    InetSocketAddress listenerOnX(MemberportPeer.Server behind) throws Exception {
        return member(X).listen(loopback0(), behind.address());
    }

    /** Opens a stream with {@code token} and asserts how it is refused, and that nothing was dialled. */
    void assertOpenRefused(Status.Code code, String what, String token) throws Exception {
        MemberportPeer.Server behind = echo();
        InetSocketAddress port = listenerOnX(behind);
        Raw r = new Raw(rawChannel(port), token);
        r.send(open()).send(data(new byte[] {1, 2, 3}));
        assertRefused(code, r.status(5_000), what);
        Thread.sleep(200);
        assertEquals(0, behind.accepted.get(), what + ": nothing may reach the peer behind the port");
    }

    @Test
    void badMissingOrExpiredTokensAreUnauthenticated() throws Exception {
        assertOpenRefused(Status.Code.UNAUTHENTICATED, "no token", null);
        assertOpenRefused(Status.Code.UNAUTHENTICATED, "garbage token", "not-a-token");
        // Ten seconds past: inside the JWT clock-skew window, still refused.
        assertOpenRefused(Status.Code.UNAUTHENTICATED, "expired token", fleet.mint(SVC, X, AUD, -10));
        assertOpenRefused(Status.Code.UNAUTHENTICATED, "gRPC audience", fleet.mint(SVC, X, SVC, 300));
        ImmutableKey rogue = ImmutableKey.from(KeyHelpers.newEd25519Key());
        assertOpenRefused(Status.Code.UNAUTHENTICATED, "unknown signing key", fleet.mint(rogue, SVC, X, AUD, 300));
    }

    @Test
    void validTokensNotAllowedHereArePermissionDenied() throws Exception {
        assertOpenRefused(Status.Code.PERMISSION_DENIED, "tenant Y's token", fleet.mint(SVC, Y, AUD, 300));
        assertOpenRefused(Status.Code.PERMISSION_DENIED, "another service", fleet.mint("other", X, AUD, 300));
    }

    @Test
    void aDataChunkCannotOpenAStream() throws Exception {
        MemberportPeer.Server behind = echo();
        Raw r = new Raw(rawChannel(listenerOnX(behind)), fleet.mint(SVC, X, AUD, 300));
        r.send(data(new byte[] {1, 2, 3}));
        assertRefused(Status.Code.PERMISSION_DENIED, r.status(5_000), "data before the session opens");
        assertEquals(0, behind.accepted.get());
    }

    @Test
    void plaintextAndNonGrpcTlsPeersAreRefused() throws Exception {
        MemberportPeer.Server behind = echo();
        InetSocketAddress port = listenerOnX(behind);
        try (Socket plain = new Socket()) {
            plain.connect(port, 5_000);
            OutputStream out = plain.getOutputStream();
            out.write("HZC".getBytes(StandardCharsets.US_ASCII));
            out.write(new byte[64]);
            out.flush();
            assertTrue(dropped(plain, 12_000), "a plaintext peer must be dropped");
        }
        assertThrows(IOException.class, () -> {
            try (SSLSocket old = rawTls(port, "tacplus-member/1")) {
                old.getOutputStream().write(new byte[] {1, 0, 0, 0, 1, 0});
                old.getOutputStream().flush();
                if (old.getInputStream().read() < 0) {
                    throw new IOException("dropped");
                }
            }
        }, "a #212 TLS-framing peer (ALPN tacplus-member/1) must be refused");
        Thread.sleep(200);
        assertEquals(0, behind.accepted.get(), "nothing may reach the peer behind the port");
    }

    @Test
    void aStreamThatNeverSendsItsFirstChunkIsDroppedAtTheAdmissionDeadline() throws Exception {
        MemberportPeer.Server behind = echo();
        Raw r = new Raw(rawChannel(listenerOnX(behind)), fleet.mint(SVC, X, AUD, 300));
        long start = System.currentTimeMillis();
        Status s = r.status(20_000);
        long elapsed = System.currentTimeMillis() - start;
        assertTrue(!s.isOk() && elapsed < 14_000, "dropped at the 10-second admission deadline: " + s + " after " + elapsed + "ms");
        assertEquals(0, behind.accepted.get(), "nothing reached the peer behind the port");
    }

    // ==== Sessions: binding and faults ====

    @Test
    void aRotationPresentingAnotherSubjectsTokenIsRefused() throws Exception {
        MemberportPeer.Server behind = echo();
        ManagedChannel ch = rawChannel(listenerOnX(behind));
        Raw first = new Raw(ch, fleet.mint(SVC, X, AUD, 300));
        String id = openSession(first);
        Raw otherTenant = new Raw(ch, fleet.mint(SVC, Y, AUD, 300));
        otherTenant.send(join(id));
        assertRefused(Status.Code.PERMISSION_DENIED, otherTenant.status(5_000), "tenant Y's token with X's session id");
        Raw otherService = new Raw(ch, fleet.mint("other", X, AUD, 300));
        otherService.send(join(id));
        assertRefused(Status.Code.PERMISSION_DENIED, otherService.status(5_000), "another service's token with the session id");
        Raw unknown = new Raw(ch, fleet.mint(SVC, X, AUD, 300));
        unknown.send(join("no-such-session"));
        assertRefused(Status.Code.PERMISSION_DENIED, unknown.status(5_000), "an unknown session id");
        // The session itself is untouched by the refused rotations.
        first.send(data(new byte[] {7, 8, 9}));
        assertArrayEquals(new byte[] {7, 8, 9}, first.next().getPayload().toByteArray(), "the session still echoes");
        assertEquals(1, behind.accepted.get());
    }

    @Test
    void anOldStreamCutOffWithoutLastDropsTheSessionAndDoesNotSplice() throws Exception {
        MemberportPeer.Server behind = echo();
        ManagedChannel ch = rawChannel(listenerOnX(behind));
        Raw old = new Raw(ch, fleet.mint(SVC, X, AUD, 300));
        String id = openSession(old);
        old.send(data(new byte[] {1, 2, 3}));
        assertArrayEquals(new byte[] {1, 2, 3}, old.next().getPayload().toByteArray());
        Raw next = new Raw(ch, fleet.mint(SVC, X, AUD, 300));
        assertEquals(id, next.send(join(id)).next().getSessionId(), "the rotation is attached");
        // The old stream ends with no last: a fault, not a cutover.
        old.cancel();
        next.send(data(new byte[] {4, 5, 6}));
        Status s = next.status(5_000);
        assertTrue(!s.isOk(), "the new stream is dropped with the session: " + s);
        waitFor("the connection behind the port to end", 5_000, () -> behind.results.size() == 1);
        assertEquals(3, behind.bytesReceived.get(), "nothing sent on the new stream was spliced on");
        String error = String.valueOf(behind.results.get(1).error);
        assertTrue(error.contains("reset"), "the connection behind the port is reset, not closed: " + error);
    }

    @Test
    void aReceiverEndingItsStreamWithoutLastResetsTheSendersLocalConnection() throws Exception {
        // A receiver that echoes one chunk, then ends the stream OK with no last:
        // the sender must not take that for a clean end.
        MemberPort.Material cert = fleet.leaf(X, SVC, "EC");
        String token = fleet.mint(SVC, X, AUD, 300);
        ServerCallHandler<MemberChunk, MemberChunk> handler = (call, headers) -> {
            Metadata h = new Metadata();
            h.put(MemberPort.MEMBER_TOKEN, token);
            call.sendHeaders(h);
            call.request(1_000_000);
            return new ServerCall.Listener<MemberChunk>() {
                boolean first = true;

                @Override
                public void onMessage(MemberChunk m) {
                    if (first) {
                        first = false;
                        call.sendMessage(join("cut-session"));
                    } else if (!m.getPayload().isEmpty()) {
                        call.sendMessage(data(m.getPayload().toByteArray()));
                        call.close(Status.OK, new Metadata());
                    }
                }
            };
        };
        Server server = NettyServerBuilder.forAddress(loopback0())
                .sslContext(GrpcSslContexts.configure(SslContextBuilder.forServer(cert.key, cert.chain), SslProvider.JDK).build())
                .addService(ServerServiceDefinition.builder(MemberPort.SERVICE_NAME).addMethod(MemberPort.OPEN_METHOD, handler).build())
                .build()
                .start();
        closers.add(server::shutdownNow);
        InetSocketAddress fwd = member(X).forward(new InetSocketAddress(InetAddress.getLoopbackAddress(), server.getPort()));
        try (Socket s = new Socket()) {
            s.connect(fwd, 5_000);
            s.setSoTimeout(5_000);
            s.getOutputStream().write(new byte[] {1, 2, 3});
            byte[] echoed = new byte[3];
            new DataInputStream(s.getInputStream()).readFully(echoed);
            assertArrayEquals(new byte[] {1, 2, 3}, echoed);
            IOException e = assertThrows(IOException.class, () -> s.getInputStream().read(),
                    "a stream that ends without last is a fault, not an EOF");
            assertTrue(String.valueOf(e.getMessage()).contains("reset"), "the local connection is reset: " + e);
        }
    }

    @Test
    void cutoverWithLastInBothDirectionsLosesNothing() throws Exception {
        MemberportPeer.Server behind = echo();
        ManagedChannel ch = rawChannel(listenerOnX(behind));
        Raw old = new Raw(ch, fleet.mint(SVC, X, AUD, 300));
        String id = openSession(old);
        old.send(data(new byte[] {1, 2, 3}));
        Raw next = new Raw(ch, fleet.mint(SVC, X, AUD, 300));
        assertEquals(id, next.send(join(id)).next().getSessionId());
        // Bytes sent on the new stream before the old one's last wait for it.
        next.send(data(new byte[] {7, 8}));
        old.send(data(new byte[] {4, 5, 6})).send(MemberChunk.newBuilder().setLast(true).build());
        java.io.ByteArrayOutputStream onOld = new java.io.ByteArrayOutputStream();
        while (true) {
            MemberChunk c = old.next();
            onOld.write(c.getPayload().toByteArray());
            if (c.getLast()) {
                break;
            }
        }
        assertEquals(Status.Code.OK, old.status(5_000).getCode(), "the server ends the old stream after its own last");
        java.io.ByteArrayOutputStream onNew = new java.io.ByteArrayOutputStream();
        while (onOld.size() + onNew.size() < 8) {
            onNew.write(next.next().getPayload().toByteArray());
        }
        byte[] all = new byte[8];
        System.arraycopy(onOld.toByteArray(), 0, all, 0, onOld.size());
        System.arraycopy(onNew.toByteArray(), 0, all, onOld.size(), onNew.size());
        assertArrayEquals(new byte[] {1, 2, 3, 4, 5, 6, 7, 8}, all, "in order across the cutover, each way");
    }

    // ==== Failure modes ====

    @Test
    void aMintFailureDropsTheSessionAtExpiry() throws Exception {
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
            assertTrue(reset(r), "the local connection is reset: " + r);
            assertTrue(elapsed < 5_000, "dropped at the token's expiry, took " + elapsed + "ms");
        } finally {
            am.stall.countDown();
        }
    }

    @Test
    void aRevokedCallerIsCutOffWithinOneLifetime() throws Exception {
        // The identity is revoked: it gets no new token, only the one in hand.
        Minter revoked = new Minter(fleet, X, 3, 1);
        MemberPort a = member(revoked, new AtomicReference<>(fleet.leaf(X, SVC, "Ed25519")), fleet.jwks::get);
        MemberportPeer.Server echo = echo();
        InetSocketAddress fwd = tunnel(a, member(X), echo);
        revoked.token();
        revoked.neverRenew = true;
        long start = System.currentTimeMillis();
        MemberportPeer.Result r = MemberportPeer.exchange(fwd, 41, 2_000_000, 41, 2048, 10, 0);
        long elapsed = System.currentTimeMillis() - start;
        assertTrue(!r.ok(2_000_000) && r.firstMismatch < 0, "cut, intact: " + r);
        assertTrue(elapsed < 4_500, "cut off within one token lifetime, took " + elapsed + "ms");
        // And a new connection with the expired token in hand is refused at once.
        MemberportPeer.Result again = MemberportPeer.exchange(fwd, 42, 1000, 42);
        assertTrue(!again.ok(1000) && again.received == 0, "refused: " + again);
    }

    @Test
    void aRefusedTunnelResetsItsLocalConnectionWellUnderASecond() throws Exception {
        MemberPort x = member(X);
        MemberportPeer.Server behind = echo();
        InetSocketAddress xPort = x.listen(loopback0(), behind.address());
        // Warm up the JVM's TLS and gRPC paths on an admitted tunnel first.
        assertTrue(MemberportPeer.exchange(member(X).forward(xPort), 1, 1000, 1).ok(1000));
        InetSocketAddress fromY = member(Y).forward(xPort);
        try (Socket s = new Socket()) {
            s.connect(fromY, 5_000);
            s.getOutputStream().write(new byte[] {1, 2, 3});
            s.setSoTimeout(5_000);
            long start = System.currentTimeMillis();
            IOException e = assertThrows(IOException.class, () -> s.getInputStream().read(),
                    "the black-box client must see the connection reset");
            long elapsed = System.currentTimeMillis() - start;
            assertTrue(!(e instanceof java.net.SocketTimeoutException) && String.valueOf(e.getMessage()).contains("reset"),
                    "ECONNRESET, not a timeout or EOF: " + e);
            assertTrue(elapsed < 1_000, "reset well under a second, took " + elapsed + "ms");
        }
        assertEquals(1, behind.accepted.get(), "the refused tunnel opened no local connection behind the port");
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
        for (int round = 0; round < 4; round++) {
            BigInteger expected = ((X509Certificate) bCert.get().chain[0]).getSerialNumber();
            assertEquals(expected, presentedSerial(bAddr), "round " + round + " serves the current certificate");
            // A new forward is a new channel: its handshake sees this round's certificate.
            InetSocketAddress fwd = member(X).forward(bAddr);
            for (int i = 0; i < 12; i++) {
                long seed = round * 100 + i;
                MemberportPeer.Result r = MemberportPeer.exchange(fwd, seed, 1000, seed);
                assertTrue(r.ok(1000), "round " + round + " connection " + i + ": " + r);
            }
            bCert.set(fleet.leaf(X, SVC, "EC"));
        }
    }

    BigInteger presentedSerial(InetSocketAddress port) throws Exception {
        try (SSLSocket s = rawTls(port, "h2")) {
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

    /**
     * A fake receiver with a valid tenant-X certificate: it answers each stream
     * with {@code token} in its headers and attaches it, then counts the data
     * chunks it gets.
     */
    InetSocketAddress fakeReceiver(String token, AtomicInteger dataChunks) throws Exception {
        MemberPort.Material cert = fleet.leaf(X, SVC, "EC");
        ServerCallHandler<MemberChunk, MemberChunk> handler = (call, headers) -> {
            Metadata h = new Metadata();
            h.put(MemberPort.MEMBER_TOKEN, token);
            call.sendHeaders(h);
            call.request(1_000_000);
            return new ServerCall.Listener<MemberChunk>() {
                boolean first = true;

                @Override
                public void onMessage(MemberChunk m) {
                    if (first) {
                        first = false;
                        call.sendMessage(join("fake-session"));
                    } else if (!m.getPayload().isEmpty()) {
                        dataChunks.incrementAndGet();
                    }
                }
            };
        };
        Server server = NettyServerBuilder.forAddress(loopback0())
                .sslContext(GrpcSslContexts.configure(SslContextBuilder.forServer(cert.key, cert.chain), SslProvider.JDK).build())
                .addService(ServerServiceDefinition.builder(MemberPort.SERVICE_NAME).addMethod(MemberPort.OPEN_METHOD, handler).build())
                .build()
                .start();
        closers.add(server::shutdownNow);
        return new InetSocketAddress(InetAddress.getLoopbackAddress(), server.getPort());
    }

    @Test
    void receiverTokenOfAnotherTenantIsRefusedBySender() throws Exception {
        AtomicInteger dataChunks = new AtomicInteger();
        InetSocketAddress fwd = member(X).forward(fakeReceiver(fleet.mint(SVC, Y, AUD, 300), dataChunks));
        MemberportPeer.Result r = MemberportPeer.exchange(fwd, 91, 1000, 91);
        assertTrue(!r.ok(1000) && r.received == 0, "refused: " + r);
        Thread.sleep(300);
        assertEquals(0, dataChunks.get(), "the sender sends no bytes to a receiver it refused");
        // The receiver's own valid member token is admitted: the same fake then works.
        AtomicInteger admittedChunks = new AtomicInteger();
        InetSocketAddress ok = member(X).forward(fakeReceiver(fleet.mint(SVC, X, AUD, 300), admittedChunks));
        try (Socket s = new Socket()) {
            s.connect(ok, 5_000);
            s.getOutputStream().write(new byte[] {1, 2, 3});
            waitFor("bytes to reach the admitted receiver", 5_000, () -> admittedChunks.get() > 0);
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
