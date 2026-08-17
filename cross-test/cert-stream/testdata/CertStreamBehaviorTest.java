// Copyright (c) 2026 Second Star Cavalier LLC

package com.example.certstream.model.s2sm;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertInstanceOf;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.example.certstream.model.chassis.service_im.IdentityManager;
import com.example.certstream.model.chassis.service_im.CloudMinter;
import com.example.certstream.model.chassis.service_im.CloudDetect;
import com.example.certstream.model.chassis.service_im.KubernetesMinter;
import com.example.certstream.model.chassis.serviceconnection.ServiceConnection;
import com.example.certstream.model.s2sm.internal.grpc.servers.svc.certstream.StreamMemberService.StreamMemberServiceHandler;
import com.example.certstream.model.s2sm.internal.grpc.servers.svc.certstream.StreamStateService.StreamStateServiceHandler;
import com.example.certstream.model.types.sys.grpcconfig.Grpcconfig.Config;
import com.example.certstream.model.types.sys.key.KeyOuterClass;
import com.example.certstream.model.types.sys.smbcert.SmbCertServiceGrpc;
import com.example.certstream.model.types.sys.smbcert.Smbcert;
import com.example.certstream.model.types.sys.tlscert.TlsCertServiceGrpc;
import com.example.certstream.model.types.sys.tlscert.Tlscert;
import com.example.certstream.model.types.sys.x509.X509;
import io.grpc.CallOptions;
import io.grpc.Channel;
import io.grpc.ClientCall;
import io.grpc.ClientInterceptor;
import io.grpc.ForwardingClientCall;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.grpc.MethodDescriptor;
import io.grpc.Server;
import io.grpc.ServerBuilder;
import io.grpc.Status;
import io.grpc.netty.shaded.io.netty.handler.ssl.SslContext;
import io.grpc.protobuf.services.HealthStatusManager;
import io.grpc.stub.StreamObserver;
import java.lang.reflect.Constructor;
import java.lang.reflect.Field;
import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Method;
import java.math.BigInteger;
import java.nio.charset.StandardCharsets;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Base64;
import java.util.Date;
import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicReference;
import org.bouncycastle.asn1.ASN1Primitive;
import org.bouncycastle.asn1.x500.X500Name;
import org.bouncycastle.asn1.x509.SubjectPublicKeyInfo;
import org.bouncycastle.cert.X509v3CertificateBuilder;
import org.bouncycastle.operator.ContentSigner;
import org.bouncycastle.operator.jcajce.JcaContentSignerBuilder;
import org.junit.jupiter.api.Test;

class CertStreamBehaviorTest {
    private enum AuthorityMode {
        FIRST_SUCCESS,
        RENEWAL_SUCCESS,
        CLEAN_BEFORE_FIRST,
        SERVER_ERROR_BEFORE,
        SERVER_ERROR_AFTER,
        REORDERED_RENEWAL,
        RENEWAL_CSR_SEND_FAILS,
        DELIVERY_HANDLING_FAILS
    }

    @Test
    void javaTlsCertStreamReportsMissingAuthorityWithoutReadySignal() throws Exception {
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = invokeExpectingFailure(
                privateMethod(
                        "runTlsCertStream",
                        ServiceManager.class,
                        ServiceConnection.class,
                        IdentityManager.class,
                        CountDownLatch.class),
                null,
                emptyAuthorityConnection(),
                null,
                ready);

        assertInstanceOf(IllegalStateException.class, err);
        assertTrue(err.getMessage().contains("no active connection for ManageCert"));
        assertEquals(1, ready.getCount(), "first-delivery signal should remain pending");
    }

    @Test
    void javaSmbCertStreamReportsMissingAuthorityWithoutReadySignal() throws Exception {
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = invokeExpectingFailure(
                privateMethod(
                        "runSmbCertStream",
                        ServiceManager.class,
                        ServiceConnection.class,
                        IdentityManager.class,
                        CountDownLatch.class),
                null,
                emptyAuthorityConnection(),
                null,
                ready);

        assertInstanceOf(IllegalStateException.class, err);
        assertTrue(err.getMessage().contains("no active connection for ManageSmbCert"));
        assertEquals(1, ready.getCount(), "first-delivery signal should remain pending");
    }

    @Test
    void javaTlsFirstDeliveryAndRenewalUseDistinctLocalKeys() throws Exception {
        try (AuthorityFixture authority = startAuthority()) {
            IdentityManager identity = fixtureIdentity();
            ServiceManager sm = fixtureServiceManager(authority.connection(), identity);
            RecordingHandler handler = new RecordingHandler();
            installHandlerSlots(sm, handler);
            CountDownLatch ready = new CountDownLatch(1);

            Throwable reconnectable = invokeAllowingFailure(
                    privateMethod(
                            "runTlsCertStream",
                            ServiceManager.class,
                            ServiceConnection.class,
                            IdentityManager.class,
                            CountDownLatch.class),
                    sm,
                    authority.connection(),
                    identity,
                    ready);

            assertReconnectableCleanEnd(reconnectable, "ManageCert");
            assertEquals(0, ready.getCount(), "TLS ready signal should be published");
            List<byte[]> keys = handler.tlsKeys();
            assertEquals(2, keys.size(), "expected initial + renewal TLS deliveries");
            assertFalse(Arrays.equals(keys.get(0), keys.get(1)), "TLS renewal must use a fresh local key");
        }
    }

    @Test
    void javaTlsCleanEndBeforeFirstDeliveryIsStartupFailure() throws Exception {
        RecordingHandler handler = new RecordingHandler();
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runTlsFixture(AuthorityMode.CLEAN_BEFORE_FIRST, handler, ready);

        assertErrorContains(err, "ManageCert stream completed before first cert delivery");
        assertEquals(1, ready.getCount(), "first-delivery signal should remain pending");
        assertEquals(0, handler.tlsKeys().size(), "handler should not be called before first delivery");
    }

    @Test
    void javaTlsServerErrorBeforeFirstDeliveryIsStartupFailure() throws Exception {
        RecordingHandler handler = new RecordingHandler();
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runTlsFixture(AuthorityMode.SERVER_ERROR_BEFORE, handler, ready);

        assertErrorContains(err, "server error: fixture before first delivery");
        assertEquals(1, ready.getCount(), "first-delivery signal should remain pending");
        assertEquals(0, handler.tlsKeys().size(), "handler should not be called before first delivery");
    }

    @Test
    void javaTlsServerErrorAfterFirstDeliveryIsReconnectable() throws Exception {
        RecordingHandler handler = new RecordingHandler();
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runTlsFixture(AuthorityMode.SERVER_ERROR_AFTER, handler, ready);

        assertErrorContains(err, "server error: fixture after first delivery");
        assertEquals(0, ready.getCount(), "TLS ready signal should be published");
        assertEquals(1, handler.tlsKeys().size(), "expected exactly one TLS delivery before server error");
    }

    @Test
    void javaTlsFirstHandlerFailureIsFatal() throws Exception {
        RecordingHandler handler = RecordingHandler.failTlsAt(1);
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runTlsFixture(AuthorityMode.FIRST_SUCCESS, handler, ready);

        assertHandlerFailure(
                err,
                "FatalTlsHandlerException",
                "handler newTlsCert failed on first cert delivery",
                "reject TLS cert delivery 1");
        assertEquals(1, ready.getCount(), "direct stream helper should not publish readiness after rejection");
        assertEquals(1, handler.tlsKeys().size(), "first TLS cert should be attempted once");
    }

    @Test
    void javaTlsRenewalHandlerFailureKeepsLoopNonfatal() throws Exception {
        RecordingHandler handler = RecordingHandler.failTlsAt(2);
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runTlsFixture(AuthorityMode.RENEWAL_SUCCESS, handler, ready);

        assertReconnectableCleanEnd(err, "ManageCert");
        assertEquals(0, ready.getCount(), "TLS ready signal should be published");
        assertEquals(2, handler.tlsKeys().size(), "expected renewal handler attempt");
    }

    @Test
    void javaTlsStaleRenewalCertIsIgnoredUntilFreshCsrIsSigned() throws Exception {
        RecordingHandler handler = new RecordingHandler();
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runTlsFixture(AuthorityMode.REORDERED_RENEWAL, handler, ready);

        assertReconnectableCleanEnd(err, "ManageCert");
        assertEquals(0, ready.getCount(), "TLS ready signal should be published");
        List<byte[]> keys = handler.tlsKeys();
        assertEquals(2, keys.size(), "expected initial + valid renewal TLS deliveries");
        assertFalse(Arrays.equals(keys.get(0), keys.get(1)), "fresh TLS renewal should install a new key");
    }

    // PC-L-6: a renewal CSR that fails to SEND leaves no in-flight request, so
    // the staged pending key must be dropped. If it is retained, the next cert
    // delivery is verified against the never-requested pending public key,
    // mismatches, and a perfectly good cert is thrown away.
    @Test
    void javaTlsFailedRenewalCsrSendClearsPendingKey() throws Exception {
        RecordingHandler handler = new RecordingHandler();
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runTlsFixture(AuthorityMode.RENEWAL_CSR_SEND_FAILS, handler, ready);

        assertReconnectableCleanEnd(err, "ManageCert");
        assertEquals(0, ready.getCount(), "TLS ready signal should be published");
        List<byte[]> keys = handler.tlsKeys();
        assertEquals(
                2,
                keys.size(),
                "cert delivered after a failed renewal CSR send must still be accepted; "
                        + "a retained pending key would fail public-key verification and drop it");
        assertTrue(
                Arrays.equals(keys.get(0), keys.get(1)),
                "delivery after a failed renewal CSR send must pair with the ACTIVE key, "
                        + "proving the pending renewal key was cleared");
    }

    @Test
    void javaSmbFailedRenewalCsrSendClearsPendingKey() throws Exception {
        RecordingHandler handler = new RecordingHandler();
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runSmbFixture(AuthorityMode.RENEWAL_CSR_SEND_FAILS, handler, ready);

        assertReconnectableCleanEnd(err, "ManageSmbCert");
        assertEquals(0, ready.getCount(), "SMB ready signal should be published");
        List<byte[]> keys = handler.smbKeys();
        assertEquals(
                2,
                keys.size(),
                "cert delivered after a failed SMB renewal CSR send must still be accepted; "
                        + "a retained pending key would fail public-key verification and drop it");
        assertTrue(
                Arrays.equals(keys.get(0), keys.get(1)),
                "delivery after a failed SMB renewal CSR send must pair with the ACTIVE key, "
                        + "proving the pending renewal key was cleared");
    }

    // Same leak class as PC-L-6, on the DELIVERY path instead of the renewal
    // path: the SMB delivery handler's catch-all must clear the pending renewal
    // refs the way its TLS twin does. If an unexpected exception escapes
    // mid-delivery, the pending key stays staged, and every later cert is then
    // verified against a public key the authority was never asked to sign — so a
    // perfectly good cert is discarded as a mismatch.
    //
    // The injection seam is a null ServiceManager: {@code sm.smbHandlerSlot.get()}
    // is the only dereference inside the delivery try block, so it raises the
    // unexpected exception at exactly the point a real one would land. That also
    // means readiness is never published, so the stranded-key mismatch surfaces
    // as a fatal stream error rather than a warning.
    @Test
    void javaSmbFailedDeliveryHandlingClearsPendingKey() throws Exception {
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runSmbFixtureWithoutServiceManager(AuthorityMode.DELIVERY_HANDLING_FAILS, ready);

        assertErrorContains(
                err,
                "ManageSmbCert stream completed before first member cert delivery");
        assertFalse(
                err.getMessage().contains("public key mismatch"),
                "cert delivered after a failed delivery-handling attempt must still verify; "
                        + "a retained pending key rejects it as a public-key mismatch");
        assertEquals(1, ready.getCount(), "no delivery was handled, so readiness must stay pending");
    }

    @Test
    void javaSmbFirstDeliveryAndRenewalUseDistinctLocalKeys() throws Exception {
        try (AuthorityFixture authority = startAuthority()) {
            IdentityManager identity = fixtureIdentity();
            ServiceManager sm = fixtureServiceManager(authority.connection(), identity);
            RecordingHandler handler = new RecordingHandler();
            installHandlerSlots(sm, handler);
            CountDownLatch ready = new CountDownLatch(1);

            Throwable reconnectable = invokeAllowingFailure(
                    privateMethod(
                            "runSmbCertStream",
                            ServiceManager.class,
                            ServiceConnection.class,
                            IdentityManager.class,
                            CountDownLatch.class),
                    sm,
                    authority.connection(),
                    identity,
                    ready);

            assertReconnectableCleanEnd(reconnectable, "ManageSmbCert");
            assertEquals(0, ready.getCount(), "SMB ready signal should be published");
            List<byte[]> keys = handler.smbKeys();
            assertEquals(2, keys.size(), "expected initial + renewal SMB deliveries");
            assertFalse(Arrays.equals(keys.get(0), keys.get(1)), "SMB renewal must use a fresh local key");
        }
    }

    @Test
    void javaSmbCleanEndBeforeFirstDeliveryIsStartupFailure() throws Exception {
        RecordingHandler handler = new RecordingHandler();
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runSmbFixture(AuthorityMode.CLEAN_BEFORE_FIRST, handler, ready);

        assertErrorContains(err, "ManageSmbCert stream completed before first member cert delivery");
        assertEquals(1, ready.getCount(), "first-delivery signal should remain pending");
        assertEquals(0, handler.smbKeys().size(), "handler should not be called before first delivery");
    }

    @Test
    void javaSmbServerErrorBeforeFirstDeliveryIsStartupFailure() throws Exception {
        RecordingHandler handler = new RecordingHandler();
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runSmbFixture(AuthorityMode.SERVER_ERROR_BEFORE, handler, ready);

        assertErrorContains(err, "server error: fixture before first delivery");
        assertEquals(1, ready.getCount(), "first-delivery signal should remain pending");
        assertEquals(0, handler.smbKeys().size(), "handler should not be called before first delivery");
    }

    @Test
    void javaSmbServerErrorAfterFirstDeliveryIsReconnectable() throws Exception {
        RecordingHandler handler = new RecordingHandler();
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runSmbFixture(AuthorityMode.SERVER_ERROR_AFTER, handler, ready);

        assertErrorContains(err, "server error: fixture after first delivery");
        assertEquals(0, ready.getCount(), "SMB ready signal should be published");
        assertEquals(1, handler.smbKeys().size(), "expected exactly one SMB delivery before server error");
    }

    @Test
    void javaSmbFirstHandlerFailureIsFatal() throws Exception {
        RecordingHandler handler = RecordingHandler.failSmbAt(1);
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runSmbFixture(AuthorityMode.FIRST_SUCCESS, handler, ready);

        assertHandlerFailure(
                err,
                "FatalSmbHandlerException",
                "handler newSmbCert failed on first member cert delivery",
                "reject SMB cert delivery 1");
        assertEquals(1, ready.getCount(), "direct stream helper should not publish readiness after rejection");
        assertEquals(1, handler.smbKeys().size(), "first SMB cert should be attempted once");
    }

    @Test
    void javaSmbRenewalHandlerFailureKeepsLoopNonfatal() throws Exception {
        RecordingHandler handler = RecordingHandler.failSmbAt(2);
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runSmbFixture(AuthorityMode.RENEWAL_SUCCESS, handler, ready);

        assertReconnectableCleanEnd(err, "ManageSmbCert");
        assertEquals(0, ready.getCount(), "SMB ready signal should be published");
        assertEquals(2, handler.smbKeys().size(), "expected renewal handler attempt");
    }

    @Test
    void javaSmbStaleRenewalCertIsIgnoredUntilFreshCsrIsSigned() throws Exception {
        RecordingHandler handler = new RecordingHandler();
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runSmbFixture(AuthorityMode.REORDERED_RENEWAL, handler, ready);

        assertReconnectableCleanEnd(err, "ManageSmbCert");
        assertEquals(0, ready.getCount(), "SMB ready signal should be published");
        List<byte[]> keys = handler.smbKeys();
        assertEquals(2, keys.size(), "expected initial + valid renewal SMB deliveries");
        assertFalse(Arrays.equals(keys.get(0), keys.get(1)), "fresh SMB renewal should install a new key");
    }

    @Test
    void javaTlsStartupBackoffExitsOnInterrupt() throws Exception {
        assertLoopExitsOnInterrupt(
                privateMethod(
                        "tlsCertStreamLoop",
                        ServiceManager.class,
                        ServiceConnection.class,
                        IdentityManager.class,
                        CountDownLatch.class,
                        AtomicReference.class),
                new CountDownLatch(1),
                1,
                "TLS startup backoff");
    }

    @Test
    void javaSmbStartupBackoffExitsOnInterrupt() throws Exception {
        assertLoopExitsOnInterrupt(
                privateMethod(
                        "smbCertStreamLoop",
                        ServiceManager.class,
                        ServiceConnection.class,
                        IdentityManager.class,
                        CountDownLatch.class,
                        AtomicReference.class),
                new CountDownLatch(1),
                1,
                "SMB startup backoff");
    }

    @Test
    void javaTlsRenewalBackoffExitsOnInterrupt() throws Exception {
        assertLoopExitsOnInterrupt(
                privateMethod(
                        "tlsCertStreamLoop",
                        ServiceManager.class,
                        ServiceConnection.class,
                        IdentityManager.class,
                        CountDownLatch.class,
                        AtomicReference.class),
                new CountDownLatch(0),
                0,
                "TLS renewal backoff");
    }

    @Test
    void javaSmbRenewalBackoffExitsOnInterrupt() throws Exception {
        assertLoopExitsOnInterrupt(
                privateMethod(
                        "smbCertStreamLoop",
                        ServiceManager.class,
                        ServiceConnection.class,
                        IdentityManager.class,
                        CountDownLatch.class,
                        AtomicReference.class),
                new CountDownLatch(0),
                0,
                "SMB renewal backoff");
    }

    @Test
    void javaPublicKeyValidationRejectsEmptyCertChain() throws Exception {
        X509.X509CertChain chain = X509.X509CertChain.newBuilder().build();

        Throwable err = invokeExpectingFailure(
                privateMethod("verifyLeafMatchesPublicKey", X509.X509CertChain.class, byte[].class),
                chain,
                new byte[] {1, 2, 3});

        assertInstanceOf(IllegalArgumentException.class, err);
        assertTrue(err.getMessage().contains("certificate chain is empty"));
    }

    @Test
    void javaPublicKeyValidationRejectsEmptyExpectedKey() throws Exception {
        X509.X509CertChain chain = X509.X509CertChain.newBuilder()
                .addChain(X509.X509Cert.newBuilder().setBase64("unused-before-key-validation"))
                .build();

        Throwable err = invokeExpectingFailure(
                privateMethod("verifyLeafMatchesPublicKey", X509.X509CertChain.class, byte[].class),
                chain,
                new byte[0]);

        assertInstanceOf(IllegalArgumentException.class, err);
        assertTrue(err.getMessage().contains("expected public key is empty"));
    }

    private static ServiceConnection emptyAuthorityConnection() {
        return new ServiceConnection(
                "cert-stream-authority",
                (serviceId, target) -> {
                    throw new AssertionError("the empty authority fixture should not dial");
                });
    }

    private static AuthorityFixture startAuthority() throws Exception {
        return startAuthority(AuthorityMode.RENEWAL_SUCCESS);
    }

    private static AuthorityFixture startAuthority(AuthorityMode mode) throws Exception {
        Server server = ServerBuilder.forPort(0)
                .addService(new JavaTlsAuthority(mode))
                .addService(new JavaSmbAuthority(mode))
                .build()
                .start();
        ManagedChannelBuilder<?> channelBuilder = ManagedChannelBuilder.forAddress("127.0.0.1", server.getPort())
                .usePlaintext();
        if (mode == AuthorityMode.RENEWAL_CSR_SEND_FAILS) {
            channelBuilder = channelBuilder.intercept(failSecondCsrSendInterceptor());
        }
        ManagedChannel channel = channelBuilder.build();
        return new AuthorityFixture(
                ServiceConnection.fromChannel("cert-stream-authority", channel),
                channel,
                server);
    }

    /**
     * Makes the SECOND CSR the client sends on a cert stream (i.e. the renewal
     * CSR — the initial CSR is the first) throw synchronously out of
     * {@code outbound.onNext}, without cancelling the stream. That is the only
     * seam that reproduces "the renewal CSR failed to send" from outside the
     * generated code.
     */
    private static ClientInterceptor failSecondCsrSendInterceptor() {
        return new ClientInterceptor() {
            @Override
            public <ReqT, RespT> ClientCall<ReqT, RespT> interceptCall(
                    MethodDescriptor<ReqT, RespT> method, CallOptions callOptions, Channel next) {
                ClientCall<ReqT, RespT> call = next.newCall(method, callOptions);
                String name = method.getFullMethodName();
                if (!name.endsWith("/ManageCert") && !name.endsWith("/ManageSmbCert")) {
                    return call;
                }
                return new ForwardingClientCall.SimpleForwardingClientCall<ReqT, RespT>(call) {
                    private int csrSends;

                    @Override
                    public void sendMessage(ReqT message) {
                        if (isCsr(message)) {
                            csrSends++;
                            if (csrSends == 2) {
                                throw new RuntimeException("fixture renewal CSR send failure");
                            }
                        }
                        super.sendMessage(message);
                    }
                };
            }
        };
    }

    private static boolean isCsr(Object message) {
        if (message instanceof Tlscert.TlsCertClientMsg tls) {
            return tls.hasCsr();
        }
        if (message instanceof Smbcert.SmbCertClientMsg smb) {
            return smb.hasCsr();
        }
        return false;
    }

    private record AuthorityFixture(
            ServiceConnection connection,
            ManagedChannel channel,
            Server server) implements AutoCloseable {
        @Override
        public void close() {
            channel.shutdownNow();
            server.shutdownNow();
        }
    }

    private static ServiceManager fixtureServiceManager(
            ServiceConnection connection,
            IdentityManager identity) throws Exception {
        Constructor<ServiceManager> ctor = ServiceManager.class.getDeclaredConstructor(
                Config.class,
                IdentityManager.class,
                ServiceConnection.class,
                ServiceConnection.ChannelFactory.class,
                HealthStatusManager.class,
                Server.class);
        ctor.setAccessible(true);
        ServiceConnection.ChannelFactory channelFactory = (serviceId, target) -> connection.conn();
        return ctor.newInstance(
                Config.getDefaultInstance(),
                identity,
                connection,
                channelFactory,
                null,
                null);
    }

    private static IdentityManager fixtureIdentity() throws Exception {
        Constructor<IdentityManager> ctor = IdentityManager.class.getDeclaredConstructor(
                KubernetesMinter.class,
                CloudMinter.class,
                ManagedChannel.class,
                List.class,
                SslContext.class,
                int.class);
        ctor.setAccessible(true);
        return ctor.newInstance(null, new FixtureMinter(), null, new ArrayList<String>(), null, 0);
    }

    private static Throwable runTlsFixture(
            AuthorityMode mode,
            RecordingHandler handler,
            CountDownLatch ready) throws Exception {
        try (AuthorityFixture authority = startAuthority(mode)) {
            IdentityManager identity = fixtureIdentity();
            ServiceManager sm = fixtureServiceManager(authority.connection(), identity);
            installHandlerSlots(sm, handler);
            return invokeAllowingFailure(
                    privateMethod(
                            "runTlsCertStream",
                            ServiceManager.class,
                            ServiceConnection.class,
                            IdentityManager.class,
                            CountDownLatch.class),
                    sm,
                    authority.connection(),
                    identity,
                    ready);
        }
    }

    private static Throwable runSmbFixture(
            AuthorityMode mode,
            RecordingHandler handler,
            CountDownLatch ready) throws Exception {
        try (AuthorityFixture authority = startAuthority(mode)) {
            IdentityManager identity = fixtureIdentity();
            ServiceManager sm = fixtureServiceManager(authority.connection(), identity);
            installHandlerSlots(sm, handler);
            return invokeAllowingFailure(
                    privateMethod(
                            "runSmbCertStream",
                            ServiceManager.class,
                            ServiceConnection.class,
                            IdentityManager.class,
                            CountDownLatch.class),
                    sm,
                    authority.connection(),
                    identity,
                    ready);
        }
    }

    /**
     * Runs the SMB cert stream with a null {@link ServiceManager}, so that
     * handling a delivered cert throws inside the delivery handler's catch-all.
     * No handler slots exist, so nothing else in the stream depends on it.
     */
    private static Throwable runSmbFixtureWithoutServiceManager(
            AuthorityMode mode,
            CountDownLatch ready) throws Exception {
        try (AuthorityFixture authority = startAuthority(mode)) {
            return invokeAllowingFailure(
                    privateMethod(
                            "runSmbCertStream",
                            ServiceManager.class,
                            ServiceConnection.class,
                            IdentityManager.class,
                            CountDownLatch.class),
                    null,
                    authority.connection(),
                    fixtureIdentity(),
                    ready);
        }
    }

    private static final class FixtureMinter extends CloudMinter {
        FixtureMinter() {
            super(CloudDetect.CloudVendor.GCP, "certstream");
        }

        @Override
        public String getToken() {
            return "fixture-token";
        }

        @Override
        public String getTokenFor(String audience) {
            return "fixture-token";
        }

        @Override
        public String getInstanceId() {
            return "fixture-instance";
        }
    }

    private static void installHandlerSlots(ServiceManager sm, Handler handler) throws Exception {
        installHandlerSlot(sm, "tlsHandlerSlot", handler);
        installHandlerSlot(sm, "smbHandlerSlot", handler);
    }

    @SuppressWarnings("unchecked")
    private static void installHandlerSlot(ServiceManager sm, String fieldName, Handler handler) throws Exception {
        Field field = ServiceManager.class.getDeclaredField(fieldName);
        field.setAccessible(true);
        ((AtomicReference<Handler>) field.get(sm)).set(handler);
    }

    private static final class RecordingHandler implements Handler {
        private final List<byte[]> tlsKeys = new ArrayList<>();
        private final List<byte[]> smbKeys = new ArrayList<>();
        private final int failTlsAt;
        private final int failSmbAt;

        RecordingHandler() {
            this(0, 0);
        }

        private RecordingHandler(int failTlsAt, int failSmbAt) {
            this.failTlsAt = failTlsAt;
            this.failSmbAt = failSmbAt;
        }

        static RecordingHandler failTlsAt(int call) {
            return new RecordingHandler(call, 0);
        }

        static RecordingHandler failSmbAt(int call) {
            return new RecordingHandler(0, call);
        }

        @Override
        public StreamMemberServiceHandler getStreamMemberGrpcImpl() {
            return null;
        }

        @Override
        public StreamStateServiceHandler getStreamStateGrpcImpl() {
            return null;
        }

        @Override
        public void newTlsCert(X509.X509CertChain certChain, byte[] privateKeyDer) {
            synchronized (tlsKeys) {
                tlsKeys.add(privateKeyDer.clone());
                if (failTlsAt == tlsKeys.size()) {
                    throw new RuntimeException("reject TLS cert delivery " + tlsKeys.size());
                }
            }
        }

        @Override
        public void newSmbCert(X509.X509CertChain memberChain, byte[] privateKeyDer, X509.X509Cert ca) {
            synchronized (smbKeys) {
                smbKeys.add(privateKeyDer.clone());
                if (failSmbAt == smbKeys.size()) {
                    throw new RuntimeException("reject SMB cert delivery " + smbKeys.size());
                }
            }
        }

        @Override
        public void shutdown() {}

        List<byte[]> tlsKeys() {
            synchronized (tlsKeys) {
                return new ArrayList<>(tlsKeys);
            }
        }

        List<byte[]> smbKeys() {
            synchronized (smbKeys) {
                return new ArrayList<>(smbKeys);
            }
        }
    }

    private static final class JavaTlsAuthority extends TlsCertServiceGrpc.TlsCertServiceImplBase {
        private final AuthorityMode mode;

        JavaTlsAuthority(AuthorityMode mode) {
            this.mode = mode;
        }

        @Override
        public StreamObserver<Tlscert.TlsCertClientMsg> manageCert(
                StreamObserver<Tlscert.TlsCertServerMsg> responseObserver) {
            return new StreamObserver<>() {
                private int deliveryCount;
                private boolean closed;
                private KeyOuterClass.PublicKey initialPublicKey;

                @Override
                public void onNext(Tlscert.TlsCertClientMsg msg) {
                    if (!msg.hasCsr()) {
                        return;
                    }
                    try {
                        deliveryCount++;
                        KeyOuterClass.PublicKey publicKey = msg.getCsr().getPublicKey();
                        if (deliveryCount == 1) {
                            initialPublicKey = publicKey;
                            if (mode == AuthorityMode.CLEAN_BEFORE_FIRST) {
                                close();
                                return;
                            }
                            if (mode == AuthorityMode.SERVER_ERROR_BEFORE) {
                                sendError("fixture before first delivery");
                                return;
                            }
                            sendCert(publicKey, "java-tls-initial");
                            if (mode == AuthorityMode.SERVER_ERROR_AFTER) {
                                sendError("fixture after first delivery");
                                return;
                            }
                            if (mode == AuthorityMode.FIRST_SUCCESS) {
                                close();
                                return;
                            }
                            if (mode == AuthorityMode.RENEWAL_CSR_SEND_FAILS) {
                                // The client's renewal CSR never reaches us: the
                                // fixture interceptor throws on that send. Re-deliver
                                // the CURRENT cert right after the renew request —
                                // the client may only accept it if the failed
                                // renewal's pending key was dropped.
                                sendRenew("fixture renewal");
                                sendCert(publicKey, "java-tls-post-failed-csr");
                                close();
                                return;
                            }
                            sendRenew("fixture renewal");
                        } else if (mode == AuthorityMode.REORDERED_RENEWAL && deliveryCount == 2) {
                            sendCert(initialPublicKey, "java-tls-stale-renewal");
                            sendRenew("fixture renewal retry");
                        } else {
                            sendCert(publicKey, "java-tls-renewal");
                            close();
                        }
                    } catch (Exception e) {
                        fail(e);
                    }
                }

                @Override
                public void onError(Throwable t) {
                    closed = true;
                }

                @Override
                public void onCompleted() {
                    close();
                }

                private void sendCert(KeyOuterClass.PublicKey publicKey, String id) throws Exception {
                    responseObserver.onNext(Tlscert.TlsCertServerMsg.newBuilder()
                            .setCert(Tlscert.TlsCertDelivery.newBuilder()
                                    .setChain(certChainForPublicKey(publicKey, id)))
                            .build());
                }

                private void sendRenew(String reason) {
                    responseObserver.onNext(Tlscert.TlsCertServerMsg.newBuilder()
                            .setRenew(Tlscert.TlsCertRenew.newBuilder()
                                    .setReason(reason))
                            .build());
                }

                private void sendError(String error) {
                    responseObserver.onNext(Tlscert.TlsCertServerMsg.newBuilder()
                            .setError(error)
                            .build());
                    close();
                }

                private void close() {
                    if (!closed) {
                        closed = true;
                        responseObserver.onCompleted();
                    }
                }

                private void fail(Exception e) {
                    if (!closed) {
                        closed = true;
                        responseObserver.onError(Status.INTERNAL
                                .withDescription(e.getMessage())
                                .withCause(e)
                                .asRuntimeException());
                    }
                }
            };
        }
    }

    private static final class JavaSmbAuthority extends SmbCertServiceGrpc.SmbCertServiceImplBase {
        private final AuthorityMode mode;

        JavaSmbAuthority(AuthorityMode mode) {
            this.mode = mode;
        }

        @Override
        public StreamObserver<Smbcert.SmbCertClientMsg> manageSmbCert(
                StreamObserver<Smbcert.SmbCertServerMsg> responseObserver) {
            return new StreamObserver<>() {
                private int deliveryCount;
                private boolean closed;
                private KeyOuterClass.PublicKey initialPublicKey;

                @Override
                public void onNext(Smbcert.SmbCertClientMsg msg) {
                    if (!msg.hasCsr()) {
                        return;
                    }
                    try {
                        deliveryCount++;
                        KeyOuterClass.PublicKey publicKey = msg.getCsr().getPublicKey();
                        if (deliveryCount == 1) {
                            initialPublicKey = publicKey;
                            if (mode == AuthorityMode.CLEAN_BEFORE_FIRST) {
                                close();
                                return;
                            }
                            if (mode == AuthorityMode.SERVER_ERROR_BEFORE) {
                                sendError("fixture before first delivery");
                                return;
                            }
                            sendCert(publicKey, "java-smb-initial");
                            if (mode == AuthorityMode.SERVER_ERROR_AFTER) {
                                sendError("fixture after first delivery");
                                return;
                            }
                            if (mode == AuthorityMode.FIRST_SUCCESS) {
                                close();
                                return;
                            }
                            if (mode == AuthorityMode.RENEWAL_CSR_SEND_FAILS) {
                                // See the TLS authority: the renewal CSR never
                                // arrives, and the re-delivered current cert can
                                // only be accepted if pending was dropped.
                                sendRenew("fixture renewal");
                                sendCert(publicKey, "java-smb-post-failed-csr");
                                close();
                                return;
                            }
                            sendRenew("fixture renewal");
                        } else if (mode == AuthorityMode.REORDERED_RENEWAL && deliveryCount == 2) {
                            sendCert(initialPublicKey, "java-smb-stale-renewal");
                            sendRenew("fixture renewal retry");
                        } else if (mode == AuthorityMode.DELIVERY_HANDLING_FAILS && deliveryCount == 2) {
                            // Deliver the renewal cert — handling it throws in the
                            // client — then re-deliver a cert for the key that was
                            // active before the renewal. The client can only accept
                            // that second cert if the thrown delivery dropped its
                            // pending renewal key.
                            sendCert(publicKey, "java-smb-renewal-throwing");
                            sendCert(initialPublicKey, "java-smb-post-throw");
                            close();
                        } else {
                            sendCert(publicKey, "java-smb-renewal");
                            close();
                        }
                    } catch (Exception e) {
                        fail(e);
                    }
                }

                @Override
                public void onError(Throwable t) {
                    closed = true;
                }

                @Override
                public void onCompleted() {
                    close();
                }

                private void sendCert(KeyOuterClass.PublicKey publicKey, String id) throws Exception {
                    responseObserver.onNext(Smbcert.SmbCertServerMsg.newBuilder()
                            .setCert(Smbcert.SmbCertDelivery.newBuilder()
                                    .setMemberChain(certChainForPublicKey(publicKey, id))
                                    .setCa(protoCert("java-smb-ca", new byte[] {1, 2, 3})))
                            .build());
                }

                private void sendRenew(String reason) {
                    responseObserver.onNext(Smbcert.SmbCertServerMsg.newBuilder()
                            .setRenew(Smbcert.SmbCertRenew.newBuilder()
                                    .setReason(reason))
                            .build());
                }

                private void sendError(String error) {
                    responseObserver.onNext(Smbcert.SmbCertServerMsg.newBuilder()
                            .setError(error)
                            .build());
                    close();
                }

                private void close() {
                    if (!closed) {
                        closed = true;
                        responseObserver.onCompleted();
                    }
                }

                private void fail(Exception e) {
                    if (!closed) {
                        closed = true;
                        responseObserver.onError(Status.INTERNAL
                                .withDescription(e.getMessage())
                                .withCause(e)
                                .asRuntimeException());
                    }
                }
            };
        }
    }

    private static X509.X509CertChain certChainForPublicKey(
            KeyOuterClass.PublicKey publicKey,
            String id) throws Exception {
        return X509.X509CertChain.newBuilder()
                .setId(id)
                .addChain(protoCert(id, certificateDerForPublicKey(publicKey, id)))
                .build();
    }

    private static X509.X509Cert protoCert(String id, byte[] der) {
        return X509.X509Cert.newBuilder()
                .setId(id)
                .setBase64(Base64.getUrlEncoder().withoutPadding().encodeToString(der))
                .build();
    }

    private static byte[] certificateDerForPublicKey(KeyOuterClass.PublicKey publicKey, String id) throws Exception {
        KeyPairGenerator signerKeyGen = KeyPairGenerator.getInstance("Ed25519");
        KeyPair signerKey = signerKeyGen.generateKeyPair();
        SubjectPublicKeyInfo subjectPublicKeyInfo =
                SubjectPublicKeyInfo.getInstance(ASN1Primitive.fromByteArray(publicKeyDer(publicKey)));
        long now = System.currentTimeMillis();
        BigInteger serial = BigInteger.valueOf(Math.max(1L, System.nanoTime()));
        X509v3CertificateBuilder builder = new X509v3CertificateBuilder(
                new X500Name("CN=java-cert-stream-fixture-ca"),
                serial,
                new Date(now - 1_000L),
                new Date(now + 3_600_000L),
                new X500Name("CN=" + id),
                subjectPublicKeyInfo);
        ContentSigner signer = new JcaContentSignerBuilder("Ed25519").build(signerKey.getPrivate());
        return builder.build(signer).getEncoded();
    }

    private static byte[] publicKeyDer(KeyOuterClass.PublicKey publicKey) {
        byte[] raw = decodeBase64(publicKey.getBase64());
        String text = new String(raw, StandardCharsets.UTF_8);
        if (!text.contains("-----BEGIN")) {
            return raw;
        }
        StringBuilder body = new StringBuilder();
        for (String line : text.split("\\R")) {
            if (!line.startsWith("-----")) {
                body.append(line.trim());
            }
        }
        return Base64.getDecoder().decode(body.toString());
    }

    private static byte[] decodeBase64(String value) {
        String padded = value + "=".repeat((4 - value.length() % 4) % 4);
        try {
            return Base64.getUrlDecoder().decode(padded);
        } catch (IllegalArgumentException urlErr) {
            return Base64.getDecoder().decode(padded);
        }
    }

    private static Method privateMethod(String name, Class<?>... parameterTypes) throws Exception {
        Method method = ServiceManager.class.getDeclaredMethod(name, parameterTypes);
        method.setAccessible(true);
        return method;
    }

    private static Throwable invokeExpectingFailure(Method method, Object... args) {
        InvocationTargetException ex =
                assertThrows(InvocationTargetException.class, () -> method.invoke(null, args));
        Throwable cause = ex.getCause();
        assertNotNull(cause);
        return cause;
    }

    private static Throwable invokeAllowingFailure(Method method, Object... args) throws Exception {
        try {
            method.invoke(null, args);
            return null;
        } catch (InvocationTargetException ex) {
            Throwable cause = ex.getCause();
            assertNotNull(cause);
            return cause;
        }
    }

    private static void assertReconnectableCleanEnd(Throwable err, String streamName) {
        if (err != null) {
            assertTrue(
                    err.getMessage().contains(streamName + " stream completed after first"),
                    "unexpected " + streamName + " stream error: " + err);
        }
    }

    private static void assertErrorContains(Throwable err, String expected) {
        assertNotNull(err, "expected stream failure containing " + expected);
        assertTrue(
                err.getMessage().contains(expected),
                "expected stream failure containing " + expected + ", got " + err);
    }

    private static void assertHandlerFailure(
            Throwable err,
            String exceptionName,
            String expectedMessage,
            String expectedCauseMessage) {
        assertNotNull(err, "expected handler failure");
        assertEquals(exceptionName, err.getClass().getSimpleName(), "unexpected handler failure type");
        assertTrue(
                err.getMessage().contains(expectedMessage),
                "expected handler failure containing " + expectedMessage + ", got " + err);
        assertNotNull(err.getCause(), "handler failure should preserve the original exception");
        assertTrue(
                err.getCause().getMessage().contains(expectedCauseMessage),
                "expected handler cause containing " + expectedCauseMessage + ", got " + err.getCause());
    }

    private static void assertLoopExitsOnInterrupt(
            Method method,
            CountDownLatch ready,
            long expectedReadyCount,
            String label) throws Exception {
        AtomicReference<Exception> readyError = new AtomicReference<>();
        AtomicReference<Throwable> failure = new AtomicReference<>();
        Thread thread = new Thread(
                () -> {
                    try {
                        method.invoke(null, null, emptyAuthorityConnection(), null, ready, readyError);
                    } catch (InvocationTargetException ex) {
                        failure.set(ex.getCause());
                    } catch (Throwable err) {
                        failure.set(err);
                    }
                },
                label);
        thread.start();

        waitForRetrySleep(thread, label);
        thread.interrupt();
        thread.join(1_000);

        assertFalse(thread.isAlive(), label + " did not exit after interrupt");
        assertNull(failure.get(), label + " loop returned an unexpected failure");
        assertNull(readyError.get(), label + " should not publish a startup error on interrupt");
        assertEquals(expectedReadyCount, ready.getCount(), label + " ready count changed");
    }

    private static void waitForRetrySleep(Thread thread, String label) throws InterruptedException {
        long deadline = System.nanoTime() + 1_000_000_000L;
        while (System.nanoTime() < deadline) {
            if (thread.getState() == Thread.State.TIMED_WAITING) {
                return;
            }
            Thread.sleep(10);
        }
        assertEquals(Thread.State.TIMED_WAITING, thread.getState(), label + " did not enter retry sleep");
    }
}
