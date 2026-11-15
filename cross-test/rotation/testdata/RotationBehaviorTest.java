package com.example.rotationfixture.model.chassis.grpcserver;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assertions.fail;

import com.example.rotationfixture.model.chassis.key.ImmutableKey;
import com.example.rotationfixture.model.chassis.key.KeyHelpers;
import com.example.rotationfixture.model.chassis.servicemanager.Caller;
import com.example.rotationfixture.model.chassis.serviceconnection.ServiceConnection;
import com.example.rotationfixture.model.chassis.token.TokenWrapper;
import com.example.rotationfixture.model.s2sm.internal.grpc.clients.svc.rotationfixture.TickerServiceGrpcClient;
import com.example.rotationfixture.model.s2sm.internal.grpc.servers.svc.rotationfixture.TickerService;
import com.example.rotationfixture.model.types.svc.rotationfixture.Rotationfixture.Tick;
import com.example.rotationfixture.model.types.sys.grpcconfig.Grpcconfig;
import com.example.rotationfixture.model.types.sys.host.HostOuterClass;
import com.example.rotationfixture.model.types.sys.jwk.JwkOuterClass;
import com.example.rotationfixture.model.types.sys.key.KeyOuterClass;
import com.example.rotationfixture.model.types.sys.rotation.Rotation.Envelope;
import com.example.rotationfixture.model.types.sys.x509.X509;
import io.grpc.CallOptions;
import io.grpc.Channel;
import io.grpc.ClientCall;
import io.grpc.Context;
import io.grpc.ManagedChannel;
import io.grpc.Metadata;
import io.grpc.MethodDescriptor;
import io.grpc.Server;
import io.grpc.ServerBuilder;
import io.grpc.ServerInterceptors;
import io.grpc.Status;
import io.grpc.netty.shaded.io.grpc.netty.GrpcSslContexts;
import io.grpc.netty.shaded.io.grpc.netty.NettyChannelBuilder;
import io.grpc.netty.shaded.io.grpc.netty.NettyServerBuilder;
import io.grpc.protobuf.ProtoUtils;
import io.grpc.stub.ClientCallStreamObserver;
import io.grpc.stub.ClientResponseObserver;
import io.grpc.stub.StreamObserver;
import io.javalin.Javalin;
import java.io.InputStream;
import java.io.OutputStream;
import java.math.BigInteger;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.PrivateKey;
import java.security.cert.X509Certificate;
import java.security.spec.ECGenParameterSpec;
import java.util.ArrayList;
import java.util.Base64;
import java.util.Collections;
import java.util.Date;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;
import java.util.function.Supplier;
import org.bouncycastle.asn1.x500.X500Name;
import org.bouncycastle.asn1.x509.Extension;
import org.bouncycastle.asn1.x509.GeneralName;
import org.bouncycastle.asn1.x509.GeneralNames;
import org.bouncycastle.cert.jcajce.JcaX509CertificateConverter;
import org.bouncycastle.cert.jcajce.JcaX509v3CertificateBuilder;
import org.bouncycastle.operator.jcajce.JcaContentSignerBuilder;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

/**
 * Make-before-break stream rotation (tacplus#322), Java leg: the twin of
 * cross-test/rotation/testdata/rotation_behavior_test.go.
 *
 * <p>Everything is the REAL generated code: the generated TickerService wrapper
 * registered with its {@code registerServices} (the plain service and its
 * TickerServiceRotating sibling) behind the generated Authorizer, served by the
 * generated GrpcServer (TLS, #260's expiry interceptor), and called by the
 * generated TickerServiceGrpcClient over the generated
 * ServiceConnection.ChannelProvider. Tokens live 3 s. Neither the handler nor
 * the caller code below mentions rotation.
 */
class RotationBehaviorTest {
    /** Every token's lifetime; exp is whole seconds, so a token may expire up to a second sooner. */
    private static final long TOKEN_LIFETIME_MS = 3_000;
    /** How long the transparency tests run a stream: several lifetimes. */
    private static final long STREAM_FOR_MS = 9_000;
    private static final Metadata.Key<String> AUTHORIZATION =
            Metadata.Key.of("authorization", Metadata.ASCII_STRING_MARSHALLER);

    // ---------------------------------------------------------------------
    // The handler: plain handler code, nothing about rotation.
    // ---------------------------------------------------------------------

    static final class HandlerEnd {
        final String method;
        volatile Status status; // null: a clean end
        volatile boolean cancelled;
        /** The handler's Context stayed live for the stream's whole life (never cancelled by a leg's end). */
        volatile boolean contextHeld = true;
        final List<Long> received = new CopyOnWriteArrayList<>();

        HandlerEnd(String method) {
            this.method = method;
        }

        void check(Context start) {
            if (start.isCancelled() || Context.current().isCancelled()) {
                contextHeld = false;
            }
        }
    }

    static boolean cancelledSoon(Context ctx) {
        long until = System.currentTimeMillis() + 1_000;
        while (System.currentTimeMillis() < until) {
            if (ctx.isCancelled()) {
                return true;
            }
            try {
                Thread.sleep(10);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return ctx.isCancelled();
            }
        }
        return ctx.isCancelled();
    }

    static final class TickerHandler implements TickerService.TickerServiceHandler {
        final AtomicInteger calls = new AtomicInteger();
        final LinkedBlockingQueue<HandlerEnd> ended = new LinkedBlockingQueue<>();

        /** Receives ticks until the caller closes, and answers with how many arrived, in order. */
        @Override
        public StreamObserver<Tick> sendTicks(Caller caller, StreamObserver<Tick> responseObserver) {
            calls.incrementAndGet();
            Context ctx = Context.current();
            HandlerEnd end = new HandlerEnd("SendTicks");
            return new StreamObserver<Tick>() {
                @Override
                public void onNext(Tick m) {
                    end.check(ctx);
                    end.received.add(m.getSeq());
                }

                @Override
                public void onError(Throwable t) {
                    end.status = Status.fromThrowable(t);
                    end.cancelled = cancelledSoon(ctx);
                    ended.add(end);
                }

                @Override
                public void onCompleted() {
                    end.check(ctx);
                    String verdict = "in-order";
                    for (int i = 0; i < end.received.size(); i++) {
                        if (end.received.get(i) != i + 1) {
                            verdict = "out of order at " + i + ": " + end.received.get(i);
                            break;
                        }
                    }
                    responseObserver.onNext(Tick.newBuilder().setSeq(end.received.size()).setM(verdict).build());
                    responseObserver.onCompleted();
                    ended.add(end);
                }
            };
        }

        /** Sends ticks 1..req.seq every 50 ms (forever when req.seq is 0). */
        @Override
        public void generateTicks(Caller caller, Tick req, StreamObserver<Tick> responseObserver) {
            calls.incrementAndGet();
            Context ctx = Context.current();
            HandlerEnd end = new HandlerEnd("GenerateTicks");
            try {
                for (long seq = 1; req.getSeq() == 0 || seq <= req.getSeq(); seq++) {
                    Thread.sleep(50);
                    if (ctx.isCancelled()) {
                        end.status = Status.fromThrowable(ctx.cancellationCause());
                        end.cancelled = true;
                        ended.add(end);
                        return;
                    }
                    end.check(ctx);
                    responseObserver.onNext(Tick.newBuilder().setSeq(seq).setM("tick").build());
                }
                responseObserver.onCompleted();
                ended.add(end);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                end.status = Status.CANCELLED;
                end.cancelled = cancelledSoon(ctx);
                ended.add(end);
            } catch (RuntimeException e) {
                end.status = Status.fromThrowable(e);
                end.cancelled = cancelledSoon(ctx);
                ended.add(end);
            }
        }

        /** Echoes every tick back. */
        @Override
        public StreamObserver<Tick> assistTicks(Caller caller, StreamObserver<Tick> responseObserver) {
            calls.incrementAndGet();
            Context ctx = Context.current();
            HandlerEnd end = new HandlerEnd("AssistTicks");
            return new StreamObserver<Tick>() {
                @Override
                public void onNext(Tick m) {
                    end.check(ctx);
                    end.received.add(m.getSeq());
                    try {
                        responseObserver.onNext(Tick.newBuilder().setSeq(m.getSeq()).setM("echo").build());
                    } catch (RuntimeException e) {
                        end.status = Status.fromThrowable(e);
                    }
                }

                @Override
                public void onError(Throwable t) {
                    end.status = Status.fromThrowable(t);
                    end.cancelled = cancelledSoon(ctx);
                    ended.add(end);
                }

                @Override
                public void onCompleted() {
                    responseObserver.onCompleted();
                    ended.add(end);
                }
            };
        }
    }

    // ---------------------------------------------------------------------
    // Fixture
    // ---------------------------------------------------------------------

    private static final class SelfSigned {
        final KeyPair keyPair;
        final X509Certificate cert;

        SelfSigned() throws Exception {
            KeyPairGenerator gen = KeyPairGenerator.getInstance("EC");
            gen.initialize(new ECGenParameterSpec("secp256r1"));
            keyPair = gen.generateKeyPair();
            X500Name name = new X500Name("CN=localhost");
            long now = System.currentTimeMillis();
            JcaX509v3CertificateBuilder b = new JcaX509v3CertificateBuilder(
                    name, BigInteger.valueOf(now), new Date(now - 60_000), new Date(now + 3_600_000),
                    name, keyPair.getPublic());
            b.addExtension(Extension.subjectAlternativeName, false, new GeneralNames(new GeneralName[] {
                    new GeneralName(GeneralName.dNSName, "localhost"),
                    new GeneralName(GeneralName.iPAddress, "127.0.0.1"),
            }));
            cert = new JcaX509CertificateConverter().getCertificate(
                    b.build(new JcaContentSignerBuilder("SHA256withECDSA").build(keyPair.getPrivate())));
        }

        X509.X509CertChain chain() throws Exception {
            return X509.X509CertChain.newBuilder()
                    .addChain(X509.X509Cert.newBuilder()
                            .setBase64(Base64.getEncoder().encodeToString(cert.getEncoded())))
                    .build();
        }
    }

    private static int freePort() throws Exception {
        try (ServerSocket s = new ServerSocket(0)) {
            s.setReuseAddress(true);
            return s.getLocalPort();
        }
    }

    private static void awaitListening(int port) throws Exception {
        long deadline = System.currentTimeMillis() + 20_000;
        while (System.currentTimeMillis() < deadline) {
            try (Socket s = new Socket()) {
                s.connect(new InetSocketAddress("127.0.0.1", port), 200);
                return;
            } catch (Exception notYet) {
                Thread.sleep(50);
            }
        }
        fail("the server did not start listening on port " + port);
    }

    private final List<AutoCloseable> cleanups = new ArrayList<>();

    @AfterEach
    void cleanUp() {
        Collections.reverse(cleanups);
        for (AutoCloseable c : cleanups) {
            try {
                c.close();
            } catch (Exception ignored) {
                // best effort
            }
        }
        cleanups.clear();
    }

    final class Fixture {
        final ImmutableKey key = ImmutableKey.from(KeyHelpers.newEd25519Key());
        final TickerHandler handler = new TickerHandler();
        final Authorizer authorizer;
        final TickerService wrapper;
        SelfSigned tls;
        int port;

        Fixture() throws Exception {
            byte[] spki = key.getPublicKey().getEncoded();
            byte[] raw = new byte[32];
            System.arraycopy(spki, spki.length - 32, raw, 0, 32);
            JwkOuterClass.Jwk jwk = JwkOuterClass.Jwk.newBuilder()
                    .setKid(key.getProto().getId())
                    .setKty(KeyOuterClass.KeyType.KEY_TYPE_OKP)
                    .setAlg(key.getAlg())
                    .setCrv("Ed25519")
                    .setX(Base64.getUrlEncoder().withoutPadding().encodeToString(raw))
                    .build();
            // ServiceManager.run's wiring, with the JWK feed seeded locally.
            authorizer = new Authorizer(sink -> sink.update(Map.of(jwk.getKid(), jwk)));
            cleanups.add(authorizer::close);
            TickerService.registerMethodAuth(authorizer);
            wrapper = new TickerService(handler, authorizer);
            wrapper.setAuthenticateFunc((TickerService.Authenticator) target -> Authorizer.identityClaims(
                    authorizer.authenticateWithToken(Authorizer.AUTH_HEADER_CTX_KEY.get())));
        }

        /** Signs a service token for subject, audience rotationfixture, living TOKEN_LIFETIME_MS. */
        String mint(String subject) {
            TokenWrapper tok = TokenWrapper.create()
                    .setIssuer("test-issuer")
                    .setAudience(Authorizer.ACCEPTED_AUDIENCES.get(0))
                    .setExpiration((System.currentTimeMillis() + TOKEN_LIFETIME_MS) / 1000)
                    .setNamespace("/system")
                    .setSubType("service")
                    .setSubject(subject);
            tok.sign(key);
            return tok.getBase64();
        }

        /** Serves the generated TickerService and its sibling on the generated GrpcServer. */
        Fixture serve() throws Exception {
            tls = new SelfSigned();
            X509.X509CertChain chain = tls.chain();
            port = freePort();
            Grpcconfig.Config config = Grpcconfig.Config.newBuilder()
                    .setListenAddress(HostOuterClass.Host.newBuilder().setHost("127.0.0.1").setPort(port))
                    .setShutdownTimeoutSeconds(1)
                    .build();
            ServiceImplementation impl = new ServiceImplementation() {
                @Override
                public void registerServices(ServerBuilder<?> grpcServerBuilder) {
                    wrapper.registerServices(grpcServerBuilder);
                }

                @Override
                public void registerRESTEndpoints(Javalin app, String apiPrefix) {}
            };
            GrpcServer srv = new GrpcServer(config, new GrpcServer.CertProvider() {
                @Override public X509.X509CertChain getChain() { return chain; }
                @Override public PrivateKey getPrivateKey() { return tls.keyPair.getPrivate(); }
            }, impl, null);
            srv.setReady(true);
            Thread t = new Thread(() -> {
                try {
                    srv.listen();
                } catch (Exception e) {
                    e.printStackTrace();
                }
            }, "rotation-grpc-server");
            t.setDaemon(true);
            t.start();
            cleanups.add(() -> {
                srv.shutdown();
                t.join(10_000);
            });
            awaitListening(port);
            return this;
        }

        ManagedChannel dial(int toPort) throws Exception {
            ManagedChannel ch = NettyChannelBuilder.forAddress("127.0.0.1", toPort)
                    .overrideAuthority("localhost")
                    .sslContext(GrpcSslContexts.forClient().trustManager(tls.cert).build())
                    .build();
            cleanups.add(() -> {
                ch.shutdownNow();
                ch.awaitTermination(5, TimeUnit.SECONDS);
            });
            return ch;
        }

        /** The generated S2S client, over the generated ChannelProvider. */
        TickerServiceGrpcClient client(ManagedChannel ch, TokenSource src) {
            ServiceConnection conn = ServiceConnection.fromChannel("TickerService", ch);
            return new TickerServiceGrpcClient(new ServiceConnection.ChannelProvider(conn), src);
        }

        HandlerEnd handlerEnd(long withinMs) throws InterruptedException {
            HandlerEnd e = handler.ended.poll(withinMs, TimeUnit.MILLISECONDS);
            assertNotNull(e, "the handler did not return within " + withinMs + " ms");
            return e;
        }
    }

    /** The caller's token provider: caller-svc tokens, counted, until revoked (then the mint fails) or impersonating. */
    static final class TokenSource implements Supplier<String> {
        final Fixture f;
        final AtomicInteger mints = new AtomicInteger();
        final AtomicBoolean revoked = new AtomicBoolean();
        final AtomicReference<String> impersonate = new AtomicReference<>();

        TokenSource(Fixture f) {
            this.f = f;
        }

        @Override
        public String get() {
            if (revoked.get()) {
                throw new IllegalStateException("identity: caller revoked");
            }
            mints.incrementAndGet();
            String other = impersonate.get();
            return f.mint(other != null ? other : "caller-svc");
        }
    }

    /** A caller's view of a stream: what arrived and how it ended. */
    static final class Recorder implements ClientResponseObserver<Tick, Tick> {
        final List<Long> got = new CopyOnWriteArrayList<>();
        final List<String> texts = new CopyOnWriteArrayList<>();
        final CompletableFuture<Status> ended = new CompletableFuture<>();
        volatile long endedAt;
        volatile ClientCallStreamObserver<Tick> requests;
        final AtomicInteger starts = new AtomicInteger();

        @Override
        public void beforeStart(ClientCallStreamObserver<Tick> requestStream) {
            starts.incrementAndGet();
            requests = requestStream;
        }

        @Override
        public void onNext(Tick m) {
            got.add(m.getSeq());
            texts.add(m.getM());
        }

        @Override
        public void onError(Throwable t) {
            endedAt = System.currentTimeMillis();
            ended.complete(Status.fromThrowable(t));
        }

        @Override
        public void onCompleted() {
            endedAt = System.currentTimeMillis();
            ended.complete(Status.OK);
        }

        Status await(long ms) throws Exception {
            try {
                return ended.get(ms, TimeUnit.MILLISECONDS);
            } catch (TimeoutException e) {
                fail("the stream was still open after " + ms + " ms");
                return null;
            }
        }
    }

    static void checkSequence(String what, List<Long> got, int n) {
        assertEquals(n, got.size(), what + ": got " + got.size() + " messages, want " + n + " (loss or duplication)");
        for (int i = 0; i < got.size(); i++) {
            if (got.get(i) != i + 1) {
                fail(what + ": message " + i + " is seq " + got.get(i) + ", want " + (i + 1)
                        + " (reordering, loss or duplication)");
            }
        }
    }

    // ---------------------------------------------------------------------
    // Transparency: one stream, several token lifetimes, plain code on both ends.
    // ---------------------------------------------------------------------

    /**
     * The caller sends a tick every 2 ms for STREAM_FOR_MS while the handler
     * echoes each back, both directions at once, across several 3 s token
     * lifetimes. Every tick arrives at the handler and back exactly once, in
     * order, on one handler call with one live Context; the tokens were renewed
     * several times.
     */
    @Test
    void bidiBothWaysAcrossLifetimes() throws Exception {
        Fixture f = new Fixture().serve();
        TokenSource src = new TokenSource(f);
        TickerServiceGrpcClient c = f.client(f.dial(f.port), src);
        Recorder rec = new Recorder();
        StreamObserver<Tick> up = c.assistTicks(rec);
        long start = System.currentTimeMillis();
        int n = 0;
        while (System.currentTimeMillis() - start < STREAM_FOR_MS) {
            n++;
            up.onNext(Tick.newBuilder().setSeq(n).setM("up").build());
            Thread.sleep(2);
        }
        long deadline = System.currentTimeMillis() + 5_000;
        while (rec.got.size() < n && System.currentTimeMillis() < deadline && !rec.ended.isDone()) {
            Thread.sleep(10);
        }
        assertFalse(rec.ended.isDone(), "the stream ended mid-way: " + rec.ended.getNow(null));
        up.onCompleted();
        assertEquals(Status.Code.OK, rec.await(5_000).getCode(), "the stream's end");
        checkSequence("echoed", rec.got, n);
        HandlerEnd end = f.handlerEnd(5_000);
        checkSequence("received by the handler", end.received, n);
        assertNull(end.status, "handler ended with " + end.status + ", want a clean end");
        assertTrue(end.contextHeld, "the handler's Context was cancelled during the stream");
        assertEquals(1, f.handler.calls.get(), "handler calls (one logical stream)");
        assertTrue(src.mints.get() >= 4, src.mints.get() + " tokens minted over " + STREAM_FOR_MS
                + " ms of " + TOKEN_LIFETIME_MS + " ms tokens, want at least 4 (the stream did not rotate)");
    }

    /** 160 ticks at 50 ms, across several lifetimes, all delivered in order, then a clean end. */
    @Test
    void serverStreamingAcrossLifetimes() throws Exception {
        Fixture f = new Fixture().serve();
        TokenSource src = new TokenSource(f);
        TickerServiceGrpcClient c = f.client(f.dial(f.port), src);
        int n = 160;
        Recorder rec = new Recorder();
        c.generateTicks(Tick.newBuilder().setSeq(n).build(), rec);
        assertEquals(Status.Code.OK, rec.await(20_000).getCode(), "the stream's end");
        checkSequence("ticks", rec.got, n);
        HandlerEnd end = f.handlerEnd(5_000);
        assertNull(end.status, "handler ended with " + end.status);
        assertTrue(end.contextHeld, "the handler's Context was cancelled during the stream");
        assertTrue(src.mints.get() >= 3, src.mints.get() + " tokens minted, want at least 3 (the stream did not rotate)");
    }

    /** 160 ticks at 50 ms, across several lifetimes; the handler counts them all, in order. */
    @Test
    void clientStreamingAcrossLifetimes() throws Exception {
        Fixture f = new Fixture().serve();
        TokenSource src = new TokenSource(f);
        TickerServiceGrpcClient c = f.client(f.dial(f.port), src);
        int n = 160;
        Recorder rec = new Recorder();
        StreamObserver<Tick> up = c.sendTicks(rec);
        for (int i = 1; i <= n; i++) {
            assertFalse(rec.ended.isDone(), "the stream ended at tick " + i + ": " + rec.ended.getNow(null));
            up.onNext(Tick.newBuilder().setSeq(i).setM("up").build());
            Thread.sleep(50);
        }
        up.onCompleted();
        assertEquals(Status.Code.OK, rec.await(5_000).getCode(), "the stream's end");
        assertEquals(List.of((long) n), rec.got, "the handler's count");
        assertEquals(List.of("in-order"), rec.texts, "the handler's verdict");
        assertTrue(src.mints.get() >= 3, src.mints.get() + " tokens minted, want at least 3 (the stream did not rotate)");
    }

    // ---------------------------------------------------------------------
    // Fail closed
    // ---------------------------------------------------------------------

    /**
     * The caller gets its opening token and no other. The stream is not
     * rotated, and ends at the token's exp with UNAUTHENTICATED; the handler's
     * Context is cancelled.
     */
    @Test
    void mintFailureEndsAtExpiry() throws Exception {
        Fixture f = new Fixture().serve();
        TokenSource src = new TokenSource(f);
        TickerServiceGrpcClient c = f.client(f.dial(f.port), src);
        long opened = System.currentTimeMillis();
        Recorder rec = new Recorder();
        c.generateTicks(Tick.newBuilder().setSeq(0).build(), rec);
        src.revoked.set(true); // the opening token was minted above
        Status st = rec.await(4 * TOKEN_LIFETIME_MS);
        assertEquals(Status.Code.UNAUTHENTICATED, st.getCode(), "the stream's end: " + st);
        long took = rec.endedAt - opened;
        assertTrue(took >= TOKEN_LIFETIME_MS - 1_500 && took <= TOKEN_LIFETIME_MS + 3_000,
                "the stream ended " + took + " ms after open, want about one token lifetime");
        assertTrue(f.handlerEnd(5_000).cancelled, "the handler's Context was not cancelled when its stream ended");
    }

    /** A caller revoked mid-stream, after some rotations, gets no new token and is cut off within one lifetime. */
    @Test
    void revokedCallerCutOff() throws Exception {
        Fixture f = new Fixture().serve();
        TokenSource src = new TokenSource(f);
        TickerServiceGrpcClient c = f.client(f.dial(f.port), src);
        Recorder rec = new Recorder();
        c.generateTicks(Tick.newBuilder().setSeq(0).build(), rec);
        long until = System.currentTimeMillis() + 6 * TOKEN_LIFETIME_MS;
        while (src.mints.get() < 3 && System.currentTimeMillis() < until && !rec.ended.isDone()) {
            Thread.sleep(1);
        }
        assertFalse(rec.ended.isDone(), "the stream ended before the caller was revoked: " + rec.ended.getNow(null));
        src.revoked.set(true);
        long revokedAt = System.currentTimeMillis();
        Status st = rec.await(3 * TOKEN_LIFETIME_MS);
        assertEquals(Status.Code.UNAUTHENTICATED, st.getCode(), "the stream's end: " + st);
        long after = rec.endedAt - revokedAt;
        assertTrue(after <= TOKEN_LIFETIME_MS + 1_000,
                "a revoked caller was cut off " + after + " ms after revocation, want within one token lifetime");
        f.handlerEnd(5_000);
    }

    /**
     * A rotation presenting another subject's token (one the method's policy
     * admits) is refused, and the caller's stream ends PERMISSION_DENIED, not
     * continued under the other name.
     */
    @Test
    void impersonationEndsTheStream() throws Exception {
        Fixture f = new Fixture().serve();
        TokenSource src = new TokenSource(f);
        TickerServiceGrpcClient c = f.client(f.dial(f.port), src);
        Recorder rec = new Recorder();
        c.generateTicks(Tick.newBuilder().setSeq(0).build(), rec);
        src.impersonate.set("other-svc");
        Status st = rec.await(4 * TOKEN_LIFETIME_MS);
        assertEquals(Status.Code.PERMISSION_DENIED, st.getCode(), "the stream's end: " + st);
        assertTrue(f.handlerEnd(5_000).cancelled, "the handler's Context was not cancelled");
    }

    // ---------------------------------------------------------------------
    // The wire
    // ---------------------------------------------------------------------

    private static final MethodDescriptor<Envelope, Envelope> ASSIST_ROTATING =
            MethodDescriptor.<Envelope, Envelope>newBuilder()
                    .setType(MethodDescriptor.MethodType.BIDI_STREAMING)
                    .setFullMethodName("rotationfixture.TickerServiceRotating/AssistTicks")
                    .setRequestMarshaller(ProtoUtils.marshaller(Envelope.getDefaultInstance()))
                    .setResponseMarshaller(ProtoUtils.marshaller(Envelope.getDefaultInstance()))
                    .build();

    /** A hand-driven leg of AssistTicks' sibling. */
    static final class RawLeg {
        final ClientCall<Envelope, Envelope> call;
        final LinkedBlockingQueue<Object> in = new LinkedBlockingQueue<>();

        RawLeg(Channel ch, String token, String joining) {
            call = ch.newCall(ASSIST_ROTATING, CallOptions.DEFAULT);
            Metadata h = new Metadata();
            h.put(AUTHORIZATION, "Bearer " + token);
            call.start(new ClientCall.Listener<Envelope>() {
                @Override
                public void onMessage(Envelope e) {
                    in.add(e);
                }

                @Override
                public void onClose(Status status, Metadata trailers) {
                    in.add(status);
                }
            }, h);
            call.request(10_000);
            send(Envelope.newBuilder().setSessionId(joining).build());
        }

        synchronized void send(Envelope e) {
            call.sendMessage(e);
        }

        Object next() throws InterruptedException {
            Object o = in.poll(10, TimeUnit.SECONDS);
            assertNotNull(o, "nothing arrived on the leg in 10 s");
            return o;
        }

        /** The server's first envelope, or the status that refused the leg. */
        Object first() throws InterruptedException {
            return next();
        }
    }

    static void refusedWithoutReason(String what, Object got, Status.Code want) {
        assertTrue(got instanceof Status, what + ": got " + got + ", want a refusal");
        Status st = (Status) got;
        assertEquals(want, st.getCode(), what + ": " + st);
        assertNull(st.getDescription(), what + ": the refusal carries a description \"" + st.getDescription()
                + "\"; it must carry none");
    }

    /**
     * A rotation presenting a valid session id with another subject's token
     * (one the policy admits) is refused PERMISSION_DENIED with no description,
     * as is an unknown id; a caller the policy does not admit is refused by the
     * method's own wrapper with no description; the same subject's new token is
     * attached.
     */
    @Test
    void anotherSubjectRefused() throws Exception {
        Fixture f = new Fixture().serve();
        ManagedChannel ch = f.dial(f.port);
        RawLeg open = new RawLeg(ch, f.mint("caller-svc"), "");
        Object first = open.first();
        assertTrue(first instanceof Envelope, "open: " + first);
        Envelope e = (Envelope) first;
        String id = e.getSessionId();
        assertFalse(id.isEmpty() || !e.getPayload().isEmpty() || e.getLast() || e.getFin(),
                "the server's first envelope is " + e + ", want a session id and nothing else");
        refusedWithoutReason("another subject's rotation", new RawLeg(ch, f.mint("other-svc"), id).first(),
                Status.Code.PERMISSION_DENIED);
        refusedWithoutReason("an unknown session's rotation", new RawLeg(ch, f.mint("caller-svc"), "no-such-session").first(),
                Status.Code.PERMISSION_DENIED);
        RawLeg intruder = new RawLeg(ch, f.mint("intruder-svc"), "");
        Object got = intruder.first();
        if (got instanceof Envelope) {
            got = intruder.next();
        }
        refusedWithoutReason("a caller the policy does not admit", got, Status.Code.PERMISSION_DENIED);
        Object echoed = new RawLeg(ch, f.mint("caller-svc"), id).first();
        assertTrue(echoed instanceof Envelope && ((Envelope) echoed).getSessionId().equals(id),
                "the same subject's rotation: " + echoed + ", want attached");
        open.call.cancel("done", null);
    }

    /**
     * A cutover driven by hand: messages before and after a last both ways,
     * with the server's sends still in flight on the old leg; the handler sees
     * every message once, in order, on one call.
     */
    @Test
    void cutoverLosesNothing() throws Exception {
        Fixture f = new Fixture().serve();
        ManagedChannel ch = f.dial(f.port);
        RawLeg old = new RawLeg(ch, f.mint("caller-svc"), "");
        Object first = old.first();
        assertTrue(first instanceof Envelope, "open: " + first);
        String id = ((Envelope) first).getSessionId();
        for (long i = 1; i <= 50; i++) {
            old.send(Envelope.newBuilder().setPayload(Tick.newBuilder().setSeq(i).build().toByteString()).build());
        }
        RawLeg next = new RawLeg(ch, f.mint("caller-svc"), id);
        Object echoed = next.first();
        assertTrue(echoed instanceof Envelope && ((Envelope) echoed).getSessionId().equals(id), "rotation: " + echoed);
        old.send(Envelope.newBuilder().setLast(true).build());
        old.call.halfClose();
        for (long i = 51; i <= 100; i++) {
            next.send(Envelope.newBuilder().setPayload(Tick.newBuilder().setSeq(i).build().toByteString()).build());
        }
        next.send(Envelope.newBuilder().setFin(true).build());
        List<Long> got = new ArrayList<>();
        while (true) {
            Object o = old.next();
            assertTrue(o instanceof Envelope, "the old leg ended (" + o + ") before the server's last");
            Envelope env = (Envelope) o;
            if (!env.getPayload().isEmpty()) {
                got.add(Tick.parseFrom(env.getPayload()).getSeq());
            }
            if (env.getLast()) {
                break;
            }
        }
        Object oldEnd = old.next();
        assertTrue(oldEnd instanceof Status && ((Status) oldEnd).isOk(), "the old leg ended with " + oldEnd + " after its last, want OK");
        while (true) {
            Object o = next.next();
            if (o instanceof Status st) {
                assertTrue(st.isOk(), "the new leg ended with " + st + ", want OK");
                break;
            }
            Envelope env = (Envelope) o;
            if (!env.getPayload().isEmpty()) {
                got.add(Tick.parseFrom(env.getPayload()).getSeq());
            }
        }
        checkSequence("echoed across the cutover", got, 100);
        HandlerEnd end = f.handlerEnd(5_000);
        checkSequence("received across the cutover", end.received, 100);
        assertEquals(1, f.handler.calls.get(), "handler calls");
    }

    /** Forwards TCP connections to a target until cut, when it closes them all. */
    static final class Proxy implements AutoCloseable {
        final ServerSocket lis;
        final List<Socket> conns = new CopyOnWriteArrayList<>();

        Proxy(int target) throws Exception {
            lis = new ServerSocket(0, 50, java.net.InetAddress.getByName("127.0.0.1"));
            Thread t = new Thread(() -> {
                while (!lis.isClosed()) {
                    try {
                        Socket in = lis.accept();
                        Socket out = new Socket("127.0.0.1", target);
                        conns.add(in);
                        conns.add(out);
                        pipe(in, out);
                        pipe(out, in);
                    } catch (Exception e) {
                        return;
                    }
                }
            }, "rotation-proxy");
            t.setDaemon(true);
            t.start();
        }

        private static void pipe(Socket from, Socket to) {
            Thread t = new Thread(() -> {
                byte[] buf = new byte[16384];
                try (InputStream i = from.getInputStream(); OutputStream o = to.getOutputStream()) {
                    int n;
                    while ((n = i.read(buf)) >= 0) {
                        o.write(buf, 0, n);
                        o.flush();
                    }
                } catch (Exception ignored) {
                    // cut
                } finally {
                    try {
                        to.close();
                    } catch (Exception ignored) {
                        // cut
                    }
                }
            }, "rotation-proxy-pipe");
            t.setDaemon(true);
            t.start();
        }

        int port() {
            return lis.getLocalPort();
        }

        void cut() {
            for (Socket s : conns) {
                try {
                    s.close();
                } catch (Exception ignored) {
                    // cut
                }
            }
            conns.clear();
        }

        @Override
        public void close() throws Exception {
            lis.close();
            cut();
        }
    }

    /**
     * A leg that ends without its last (here the caller's connection is cut)
     * ends the logical stream: the caller gets the status, the handler's
     * Context is cancelled, and nothing is spliced onto another leg.
     */
    @Test
    void legCutWithoutLastEndsTheStream() throws Exception {
        Fixture f = new Fixture().serve();
        Proxy proxy = new Proxy(f.port);
        cleanups.add(proxy);
        TokenSource src = new TokenSource(f);
        TickerServiceGrpcClient c = f.client(f.dial(proxy.port()), src);
        Recorder rec = new Recorder();
        c.generateTicks(Tick.newBuilder().setSeq(0).build(), rec);
        long until = System.currentTimeMillis() + 10_000;
        while (rec.got.size() < 5 && System.currentTimeMillis() < until) {
            Thread.sleep(10);
        }
        assertTrue(rec.got.size() >= 5, "no ticks arrived before the cut: " + rec.ended.getNow(null));
        proxy.cut();
        Status st = rec.await(5_000);
        assertFalse(st.isOk(), "the cut stream ended OK, want a failure status");
        assertTrue(f.handlerEnd(5_000).cancelled, "the handler's Context was not cancelled by the cut");
    }

    /**
     * Against a server that serves only the plain service (no sibling), the
     * generated client's stream goes to the plain method, which works, and ends
     * at its token's exp with UNAUTHENTICATED (#260).
     */
    @Test
    void fallsBackToPlainMethod() throws Exception {
        Fixture f = new Fixture();
        Server srv = NettyServerBuilder.forAddress(new InetSocketAddress("127.0.0.1", 0))
                .addService(ServerInterceptors.intercept(f.wrapper,
                        Authorizer.headerCaptureInterceptor(), Authorizer.streamCredentialExpiryInterceptor()))
                .build()
                .start();
        cleanups.add(() -> {
            srv.shutdownNow();
            srv.awaitTermination(5, TimeUnit.SECONDS);
        });
        ManagedChannel ch = NettyChannelBuilder.forAddress("127.0.0.1", srv.getPort()).usePlaintext().build();
        cleanups.add(() -> {
            ch.shutdownNow();
            ch.awaitTermination(5, TimeUnit.SECONDS);
        });
        TokenSource src = new TokenSource(f);
        TickerServiceGrpcClient c = f.client(ch, src);
        long opened = System.currentTimeMillis();
        Recorder rec = new Recorder();
        c.generateTicks(Tick.newBuilder().setSeq(0).build(), rec);
        Status st = rec.await(4 * TOKEN_LIFETIME_MS);
        assertFalse(rec.got.isEmpty(), "no tick arrived on the plain method: " + st);
        assertEquals(Status.Code.UNAUTHENTICATED, st.getCode(), "the plain stream's end: " + st);
        long took = rec.endedAt - opened;
        assertTrue(took <= TOKEN_LIFETIME_MS + 3_000, "the plain stream lasted " + took + " ms, want it ended at its token's exp");
    }
}
