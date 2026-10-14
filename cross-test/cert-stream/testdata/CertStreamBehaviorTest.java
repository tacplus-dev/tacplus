// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

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
import io.grpc.Status;
import io.grpc.netty.shaded.io.grpc.netty.NettyServerBuilder;
import io.grpc.netty.shaded.io.netty.handler.ssl.SslContext;
import io.grpc.protobuf.services.HealthStatusManager;
import io.grpc.stub.ServerCallStreamObserver;
import io.grpc.stub.StreamObserver;
import java.lang.reflect.Constructor;
import java.lang.reflect.Field;
import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Method;
import java.math.BigInteger;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Base64;
import java.util.Date;
import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
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
        RENEWAL_CSR_SEND_FAILS_STREAM_STAYS_OPEN,
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
    // the staged pending key must be dropped AND the session must END.
    //
    // Java used to log a warning and keep receiving on a stream with no renewal
    // in flight: the authority had asked for a renewal, the client silently
    // answered nothing, and the serving cert ran to expiry. go returns the
    // session error ("send renewal Csr" / "generate renewal keypair",
    // service_manager.go.tmpl) and rust propagates with `?`, so in both the
    // stream loop reconnects and re-CSRs. These tests pin java to that contract.
    @Test
    void javaTlsFailedRenewalCsrSendEndsSession() throws Exception {
        RecordingHandler handler = new RecordingHandler();
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runTlsFixture(AuthorityMode.RENEWAL_CSR_SEND_FAILS, handler, ready);

        assertErrorContains(err, "send renewal Csr");
        assertEquals(0, ready.getCount(), "TLS ready signal should be published");
        assertEquals(
                1,
                handler.tlsKeys().size(),
                "the session must end at the failed renewal CSR send, so only the "
                        + "first delivery reaches the handler");
    }

    @Test
    void javaSmbFailedRenewalCsrSendEndsSession() throws Exception {
        RecordingHandler handler = new RecordingHandler();
        CountDownLatch ready = new CountDownLatch(1);

        Throwable err = runSmbFixture(AuthorityMode.RENEWAL_CSR_SEND_FAILS, handler, ready);

        assertErrorContains(err, "send SMB renewal Csr");
        assertEquals(0, ready.getCount(), "SMB ready signal should be published");
        assertEquals(
                1,
                handler.smbKeys().size(),
                "the session must end at the failed SMB renewal CSR send, so only the "
                        + "first delivery reaches the handler");
    }

    // Ending the session must end the CALL. Java used to leave the ManageCert /
    // ManageSmbCert call open on the shared channel after the loop had already
    // failed: the authority kept sending, and onNext kept installing certs for a
    // session that no longer existed — including a message already in flight
    // when the loop decided to fail. That is a fail-OPEN on the cert path. These
    // tests seed a SECOND server message that arrives strictly after the failed
    // renewal CSR send and pin both halves of the fix: the call is cancelled,
    // and the late delivery never reaches the handler.
    @Test
    void javaTlsEndedSessionCancelsTheCallAndIgnoresLateDeliveries() throws Exception {
        RecordingHandler handler = new RecordingHandler();
        CountDownLatch ready = new CountDownLatch(1);

        try (AuthorityFixture authority = startAuthority(
                AuthorityMode.RENEWAL_CSR_SEND_FAILS_STREAM_STAYS_OPEN)) {
            IdentityManager identity = fixtureIdentity();
            ServiceManager sm = fixtureServiceManager(authority.connection(), identity);
            installHandlerSlots(sm, handler);

            Throwable err = invokeAllowingFailure(
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

            assertErrorContains(err, "send renewal Csr");
            assertEquals(1, handler.tlsKeys().size(), "only the first delivery is handled");
            authority.tlsAuthority.sendLateCert("java-tls-late-delivery");
            assertEquals(
                    1,
                    awaitDeliveryCount(() -> handler.tlsKeys().size(), 2),
                    "a cert delivered after the session ended must not reach the handler");
            assertTrue(
                    authority.tlsAuthority.awaitCancelled(5, TimeUnit.SECONDS),
                    "ending the TLS cert-stream session must cancel the ManageCert call so "
                            + "the authority stops sending");
        }
    }

    @Test
    void javaSmbEndedSessionCancelsTheCallAndIgnoresLateDeliveries() throws Exception {
        RecordingHandler handler = new RecordingHandler();
        CountDownLatch ready = new CountDownLatch(1);

        try (AuthorityFixture authority = startAuthority(
                AuthorityMode.RENEWAL_CSR_SEND_FAILS_STREAM_STAYS_OPEN)) {
            IdentityManager identity = fixtureIdentity();
            ServiceManager sm = fixtureServiceManager(authority.connection(), identity);
            installHandlerSlots(sm, handler);

            Throwable err = invokeAllowingFailure(
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

            assertErrorContains(err, "send SMB renewal Csr");
            assertEquals(1, handler.smbKeys().size(), "only the first delivery is handled");
            authority.smbAuthority.sendLateCert("java-smb-late-delivery");
            assertEquals(
                    1,
                    awaitDeliveryCount(() -> handler.smbKeys().size(), 2),
                    "a member cert delivered after the session ended must not reach the handler");
            assertTrue(
                    authority.smbAuthority.awaitCancelled(5, TimeUnit.SECONDS),
                    "ending the SMB cert-stream session must cancel the ManageSmbCert call so "
                            + "the authority stops sending");
        }
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

    // PC-L-3, anti-channel-capture leg. docs/API.md: "Reconnect loops
    // re-resolve the authority endpoint on every attempt; a certificate stream
    // must not pin a dead channel, stub, observer, or connection forever after
    // the authority endpoint changes." The template pin for this lives in
    // pkg/internal/common/cert_stream_reconnect_parity_test.go, which proves the
    // channel is acquired INSIDE the per-attempt function for all three
    // languages. These two tests are the behavioural half for java: they move
    // the target between attempts and check where the retry actually lands.
    //
    // The first attempt talks to a stale authority that ends the stream before
    // delivering, so the loop retries; by then the ServiceConnection resolves to
    // a different target. The CSR counters are the direct evidence — the stale
    // authority must see exactly the one CSR of the failed first attempt, and
    // the moved authority must see the retry's. A loop that captured the channel
    // from the first attempt inverts both, keeps re-dialling a target that will
    // never deliver, and never drops the first-delivery latch.
    @Test
    void javaTlsCertStreamReResolvesTheAuthorityAfterTheTargetMoves() throws Exception {
        try (AuthorityFixture stale = startAuthority(AuthorityMode.CLEAN_BEFORE_FIRST);
                AuthorityFixture moved = startAuthority(AuthorityMode.FIRST_SUCCESS)) {
            MovingAuthorityConnection connection =
                    new MovingAuthorityConnection(stale.channel(), moved.channel());
            IdentityManager identity = fixtureIdentity();
            // The ServiceManager gets a plain connection, NOT the moving one: its
            // constructor resolves a channel of its own for the state clients, and
            // that resolution would be indistinguishable from a reconnect attempt.
            // The loop takes the connection it re-resolves as an argument, and that
            // argument is the seam under test.
            ServiceManager sm = fixtureServiceManager(moved.connection(), identity);
            RecordingHandler handler = new RecordingHandler();
            installHandlerSlots(sm, handler);
            CountDownLatch ready = new CountDownLatch(1);
            AtomicReference<Exception> readyError = new AtomicReference<>();
            AtomicReference<Throwable> failure = new AtomicReference<>();

            Thread loop = startCertStreamLoop(
                    "tlsCertStreamLoop",
                    sm,
                    connection,
                    identity,
                    ready,
                    readyError,
                    failure,
                    "TLS moved-authority");
            boolean delivered;
            try {
                delivered = ready.await(30, TimeUnit.SECONDS);
            } finally {
                stopCertStreamLoop(loop);
            }

            assertNull(failure.get(), "TLS loop returned an unexpected failure");
            assertTrue(delivered, "TLS first delivery must arrive once the authority target moves");
            assertNull(readyError.get(), "TLS startup must succeed against the moved authority");
            assertEquals(1, handler.tlsKeys().size(), "expected one TLS delivery, from the moved authority");
            assertEquals(
                    1,
                    stale.tlsCsrs(),
                    "the stale TLS authority must be dialled on the first attempt and never again");
            assertTrue(
                    moved.tlsCsrs() >= 1,
                    "the TLS retry must reach the moved authority, not a captured channel to the stale one");
            assertTrue(
                    connection.resolutions() >= 2,
                    "the TLS loop must re-resolve the authority connection on every attempt");
        }
    }

    @Test
    void javaSmbCertStreamReResolvesTheAuthorityAfterTheTargetMoves() throws Exception {
        try (AuthorityFixture stale = startAuthority(AuthorityMode.CLEAN_BEFORE_FIRST);
                AuthorityFixture moved = startAuthority(AuthorityMode.FIRST_SUCCESS)) {
            MovingAuthorityConnection connection =
                    new MovingAuthorityConnection(stale.channel(), moved.channel());
            IdentityManager identity = fixtureIdentity();
            // The ServiceManager gets a plain connection, NOT the moving one: its
            // constructor resolves a channel of its own for the state clients, and
            // that resolution would be indistinguishable from a reconnect attempt.
            // The loop takes the connection it re-resolves as an argument, and that
            // argument is the seam under test.
            ServiceManager sm = fixtureServiceManager(moved.connection(), identity);
            RecordingHandler handler = new RecordingHandler();
            installHandlerSlots(sm, handler);
            CountDownLatch ready = new CountDownLatch(1);
            AtomicReference<Exception> readyError = new AtomicReference<>();
            AtomicReference<Throwable> failure = new AtomicReference<>();

            Thread loop = startCertStreamLoop(
                    "smbCertStreamLoop",
                    sm,
                    connection,
                    identity,
                    ready,
                    readyError,
                    failure,
                    "SMB moved-authority");
            boolean delivered;
            try {
                delivered = ready.await(30, TimeUnit.SECONDS);
            } finally {
                stopCertStreamLoop(loop);
            }

            assertNull(failure.get(), "SMB loop returned an unexpected failure");
            assertTrue(delivered, "SMB first delivery must arrive once the authority target moves");
            assertNull(readyError.get(), "SMB startup must succeed against the moved authority");
            assertEquals(1, handler.smbKeys().size(), "expected one SMB delivery, from the moved authority");
            assertEquals(
                    1,
                    stale.smbCsrs(),
                    "the stale SMB authority must be dialled on the first attempt and never again");
            assertTrue(
                    moved.smbCsrs() >= 1,
                    "the SMB retry must reach the moved authority, not a captured channel to the stale one");
            assertTrue(
                    connection.resolutions() >= 2,
                    "the SMB loop must re-resolve the authority connection on every attempt");
        }
    }

    /**
     * Drives the give-up-before-first-delivery path in real time. The production
     * budget (MAX_INIT_RETRIES = 30) takes ~812s of backoff to spend honestly,
     * which is why this path had no behavioral pin; tlsCertStreamLoop now takes
     * the budget as an overload parameter so a test can spend it in seconds
     * (PC-L-3).
     */
    @Test
    void javaTlsCertStreamGivesUpAfterTheInjectedBudget() throws Exception {
        // The empty authority never yields a connection, so every attempt fails
        // before first delivery and the loop can only exit by exhausting its
        // budget. A budget of 3 costs 2s + 4s of backoff instead of ~812s.
        // The ServiceManager gets a live connection because its constructor resolves
        // a channel of its own for the state clients; the loop under test gets the
        // empty one, which is the argument that decides the retry outcome.
        try (AuthorityFixture authority = startAuthority()) {
        ServiceConnection connection = emptyAuthorityConnection();
        IdentityManager identity = fixtureIdentity();
        ServiceManager sm = fixtureServiceManager(authority.connection(), identity);
        CountDownLatch ready = new CountDownLatch(1);
        AtomicReference<Exception> readyError = new AtomicReference<>();
        AtomicReference<Throwable> failure = new AtomicReference<>();
        Method loop = privateMethod(
                "tlsCertStreamLoop",
                ServiceManager.class,
                ServiceConnection.class,
                IdentityManager.class,
                CountDownLatch.class,
                AtomicReference.class,
                int.class);

        Thread thread = new Thread(
                () -> {
                    try {
                        loop.invoke(null, sm, connection, identity, ready, readyError, 3);
                    } catch (InvocationTargetException ex) {
                        failure.set(ex.getCause());
                    } catch (Throwable err) {
                        failure.set(err);
                    }
                },
                "TLS retry exhaustion");
        thread.setDaemon(true);
        long start = System.nanoTime();
        thread.start();
        boolean gaveUp = ready.await(60, TimeUnit.SECONDS);
        long elapsedMs = (System.nanoTime() - start) / 1_000_000L;
        try {
            assertTrue(
                    gaveUp,
                    "the TLS loop must give up after the injected budget of 3 attempts, but it did"
                            + " not signal within 60s — the injected budget was ignored");
        } finally {
            stopCertStreamLoop(thread);
        }

        assertNull(failure.get(), "TLS exhaustion loop returned an unexpected failure");
        assertTrue(
                elapsedMs < 30_000L,
                "a budget of 3 costs ~6s of backoff, but give-up took " + elapsedMs
                        + "ms — the injected budget was ignored");
        assertNotNull(readyError.get(), "give-up must publish a startup error");
        assertFalse(
                readyError.get().getClass().getSimpleName().contains("FatalTlsHandler"),
                "give-up must not be reported as a handler rejection: " + readyError.get());
        List<String> degraded = tlsDegradeReasons(sm);
        assertEquals(1, degraded.size(), "give-up must degrade the instance exactly once: " + degraded);
        assertTrue(
                degraded.get(0).contains("TLS renewal stream gave up before first delivery"),
                "unexpected degrade reason: " + degraded.get(0));
        }
    }

    /**
     * Guards the hard constraint behind the seam: the production entry point (the
     * 5-arg overload) still spends the full MAX_INIT_RETRIES budget. A budget of
     * 3 gives up after ~6s, so silence well past that window proves the default
     * is materially larger.
     */
    @Test
    void javaTlsCertStreamDefaultBudgetOutlastsATestBudget() throws Exception {
        try (AuthorityFixture authority = startAuthority()) {
        ServiceConnection connection = emptyAuthorityConnection();
        IdentityManager identity = fixtureIdentity();
        ServiceManager sm = fixtureServiceManager(authority.connection(), identity);
        CountDownLatch ready = new CountDownLatch(1);
        AtomicReference<Exception> readyError = new AtomicReference<>();
        AtomicReference<Throwable> failure = new AtomicReference<>();

        Thread thread = startCertStreamLoop(
                "tlsCertStreamLoop",
                sm,
                connection,
                identity,
                ready,
                readyError,
                failure,
                "TLS default budget");
        try {
            assertFalse(
                    ready.await(15, TimeUnit.SECONDS),
                    "the default-budget TLS loop gave up inside a window sized for a test budget");
        } finally {
            stopCertStreamLoop(thread);
        }
        assertNull(failure.get(), "default-budget TLS loop returned an unexpected failure");
        assertNull(readyError.get(), "default-budget TLS loop must not publish a startup error yet");
        assertTrue(tlsDegradeReasons(sm).isEmpty(), "default-budget TLS loop must not degrade yet");
        }
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

    /**
     * Waits up to two seconds for the handler's delivery count to reach
     * {@code unwanted}. Returns as soon as it does, so a late delivery that IS
     * applied fails fast and only the passing case spends the whole budget.
     */
    private static int awaitDeliveryCount(
            java.util.function.Supplier<Integer> counter,
            int unwanted) throws InterruptedException {
        long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(2);
        int count = counter.get();
        while (count < unwanted && System.nanoTime() < deadline) {
            Thread.sleep(25);
            count = counter.get();
        }
        return count;
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
        JavaTlsAuthority tlsAuthority = new JavaTlsAuthority(mode);
        JavaSmbAuthority smbAuthority = new JavaSmbAuthority(mode);
        // Bind the authority to the loopback address SPECIFICALLY, not the
        // wildcard address `ServerBuilder.forPort` uses. A wildcard bind does
        // not reserve 127.0.0.1:<that port>: another socket may afterwards bind
        // that exact address with SO_REUSEADDR and succeed, and every connect to
        // 127.0.0.1 then goes to the MORE SPECIFIC bind - i.e. to the other
        // process, not to this fixture. Whatever answers instead is not an
        // HTTP/2 peer, so the client's first inbound bytes are not a SETTINGS
        // frame, netty raises an Http2Exception, and grpc-netty's
        // Utils.statusFromThrowable turns that into `INTERNAL: http2 exception`
        // in place of whatever error the test asserted - which is exactly how
        // javaSmbFailedDeliveryHandlingClearsPendingKey has been seen to fail.
        // A port already bound loopback-specifically cannot be bound
        // loopback-specifically again ("Address already in use"), so binding
        // this way closes that hole. (The port cannot be lost through EPHEMERAL
        // allocation - the kernel never hands out a port another socket holds -
        // so this shuts the door on a peer that names the port explicitly.)
        Server server = NettyServerBuilder
                .forAddress(new InetSocketAddress(InetAddress.getLoopbackAddress(), 0))
                .addService(tlsAuthority)
                .addService(smbAuthority)
                .build()
                .start();
        ManagedChannelBuilder<?> channelBuilder = ManagedChannelBuilder.forAddress("127.0.0.1", server.getPort())
                .usePlaintext();
        if (mode == AuthorityMode.RENEWAL_CSR_SEND_FAILS
                || mode == AuthorityMode.RENEWAL_CSR_SEND_FAILS_STREAM_STAYS_OPEN) {
            channelBuilder = channelBuilder.intercept(failSecondCsrSendInterceptor());
        }
        ManagedChannel channel = channelBuilder.build();
        return new AuthorityFixture(
                ServiceConnection.fromChannel("cert-stream-authority", channel),
                channel,
                server,
                tlsAuthority,
                smbAuthority);
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
            Server server,
            JavaTlsAuthority tlsAuthority,
            JavaSmbAuthority smbAuthority) implements AutoCloseable {
        /** How many ManageCert CSRs this authority instance has received. */
        int tlsCsrs() {
            return tlsAuthority.csrs();
        }

        /** How many ManageSmbCert CSRs this authority instance has received. */
        int smbCsrs() {
            return smbAuthority.csrs();
        }

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
    private static List<String> tlsDegradeReasons(ServiceManager sm) throws Exception {
        Field field = ServiceManager.class.getDeclaredField("tlsDegradeSeen");
        field.setAccessible(true);
        return new ArrayList<>((List<String>) field.get(sm));
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
        // Counted per AUTHORITY, not per stream, so a test can tell which
        // authority instance a reconnect attempt actually landed on.
        private final AtomicInteger csrs = new AtomicInteger();
        // Server side of the still-open stream, so a test can send AFTER the
        // client loop has already ended its session, plus the signal that the
        // client actually cancelled the call.
        private final AtomicReference<StreamObserver<Tlscert.TlsCertServerMsg>> live = new AtomicReference<>();
        private final AtomicReference<KeyOuterClass.PublicKey> lastPublicKey = new AtomicReference<>();
        private final CountDownLatch cancelled = new CountDownLatch(1);

        JavaTlsAuthority(AuthorityMode mode) {
            this.mode = mode;
        }

        int csrs() {
            return csrs.get();
        }

        boolean awaitCancelled(long timeout, TimeUnit unit) throws InterruptedException {
            return cancelled.await(timeout, unit);
        }

        void sendLateCert(String id) {
            StreamObserver<Tlscert.TlsCertServerMsg> observer = live.get();
            KeyOuterClass.PublicKey publicKey = lastPublicKey.get();
            assertNotNull(observer, "no ManageCert stream to send a late cert on");
            assertNotNull(publicKey, "no CSR public key to sign a late cert for");
            try {
                observer.onNext(Tlscert.TlsCertServerMsg.newBuilder()
                        .setCert(Tlscert.TlsCertDelivery.newBuilder()
                                .setChain(certChainForPublicKey(publicKey, id)))
                        .build());
            } catch (Exception ignored) {
                // Once the call is cancelled this send is dropped (silently,
                // because a cancel handler is installed above) or throws,
                // depending on how far the cancellation has propagated. Either
                // way the client must not see the cert — that is what the
                // caller asserts.
            }
        }

        @Override
        public StreamObserver<Tlscert.TlsCertClientMsg> manageCert(
                StreamObserver<Tlscert.TlsCertServerMsg> responseObserver) {
            live.set(responseObserver);
            // Observe cancellation through the call's own cancel callback, not
            // through this request observer's onError. gRPC only synthesises
            // that onError when the client has NOT already half-closed
            // (ServerCalls' listener guards it with `if (!halfClosed)`), and
            // the generated client ends a session by counting its done latch
            // down and THEN cancelling: the loop thread it releases half-closes
            // from `finally` at the same moment the callback thread cancels. So
            // whether the half-close or the cancel reaches the server first is a
            // thread race, and every run that lost it left the latch below
            // untripped even though the call really was cancelled. The cancel
            // handler fires either way.
            ((ServerCallStreamObserver<Tlscert.TlsCertServerMsg>) responseObserver)
                    .setOnCancelHandler(cancelled::countDown);
            return new StreamObserver<>() {
                private int deliveryCount;
                private boolean closed;
                private KeyOuterClass.PublicKey initialPublicKey;

                @Override
                public void onNext(Tlscert.TlsCertClientMsg msg) {
                    if (!msg.hasCsr()) {
                        return;
                    }
                    csrs.incrementAndGet();
                    try {
                        deliveryCount++;
                        KeyOuterClass.PublicKey publicKey = msg.getCsr().getPublicKey();
                        lastPublicKey.set(publicKey);
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
                            if (mode == AuthorityMode.RENEWAL_CSR_SEND_FAILS_STREAM_STAYS_OPEN) {
                                // Same failed renewal CSR, but the authority then
                                // leaves the stream open and sends nothing more, so
                                // the test controls what arrives after the client
                                // has already ended its session.
                                sendRenew("fixture renewal");
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
                    cancelled.countDown();
                }

                @Override
                public void onCompleted() {
                    if (mode == AuthorityMode.RENEWAL_CSR_SEND_FAILS_STREAM_STAYS_OPEN) {
                        // The client half-closed when its loop ended. A real
                        // authority keeps sending on the still-open call, which
                        // is exactly what this mode exists to reproduce.
                        return;
                    }
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
        // See JavaTlsAuthority.csrs.
        private final AtomicInteger csrs = new AtomicInteger();
        // See JavaTlsAuthority.live/lastPublicKey/cancelled.
        private final AtomicReference<StreamObserver<Smbcert.SmbCertServerMsg>> live = new AtomicReference<>();
        private final AtomicReference<KeyOuterClass.PublicKey> lastPublicKey = new AtomicReference<>();
        private final CountDownLatch cancelled = new CountDownLatch(1);

        JavaSmbAuthority(AuthorityMode mode) {
            this.mode = mode;
        }

        int csrs() {
            return csrs.get();
        }

        boolean awaitCancelled(long timeout, TimeUnit unit) throws InterruptedException {
            return cancelled.await(timeout, unit);
        }

        void sendLateCert(String id) {
            StreamObserver<Smbcert.SmbCertServerMsg> observer = live.get();
            KeyOuterClass.PublicKey publicKey = lastPublicKey.get();
            assertNotNull(observer, "no ManageSmbCert stream to send a late cert on");
            assertNotNull(publicKey, "no CSR public key to sign a late cert for");
            try {
                observer.onNext(Smbcert.SmbCertServerMsg.newBuilder()
                        .setCert(Smbcert.SmbCertDelivery.newBuilder()
                                .setMemberChain(certChainForPublicKey(publicKey, id))
                                .setCa(protoCert("java-smb-ca", new byte[] {1, 2, 3})))
                        .build());
            } catch (Exception ignored) {
                // Once the call is cancelled this send is dropped (silently,
                // because a cancel handler is installed above) or throws,
                // depending on how far the cancellation has propagated. Either
                // way the client must not see the cert — that is what the
                // caller asserts.
            }
        }

        @Override
        public StreamObserver<Smbcert.SmbCertClientMsg> manageSmbCert(
                StreamObserver<Smbcert.SmbCertServerMsg> responseObserver) {
            live.set(responseObserver);
            // Observe cancellation through the call's own cancel callback, not
            // through this request observer's onError. gRPC only synthesises
            // that onError when the client has NOT already half-closed
            // (ServerCalls' listener guards it with `if (!halfClosed)`), and
            // the generated client ends a session by counting its done latch
            // down and THEN cancelling: the loop thread it releases half-closes
            // from `finally` at the same moment the callback thread cancels. So
            // whether the half-close or the cancel reaches the server first is a
            // thread race, and every run that lost it left the latch below
            // untripped even though the call really was cancelled. The cancel
            // handler fires either way.
            ((ServerCallStreamObserver<Smbcert.SmbCertServerMsg>) responseObserver)
                    .setOnCancelHandler(cancelled::countDown);
            return new StreamObserver<>() {
                private int deliveryCount;
                private boolean closed;
                private KeyOuterClass.PublicKey initialPublicKey;

                @Override
                public void onNext(Smbcert.SmbCertClientMsg msg) {
                    if (!msg.hasCsr()) {
                        return;
                    }
                    csrs.incrementAndGet();
                    try {
                        deliveryCount++;
                        KeyOuterClass.PublicKey publicKey = msg.getCsr().getPublicKey();
                        lastPublicKey.set(publicKey);
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
                            if (mode == AuthorityMode.RENEWAL_CSR_SEND_FAILS_STREAM_STAYS_OPEN) {
                                // See the TLS authority: leave the stream open so
                                // the test owns what arrives after the client has
                                // ended its session.
                                sendRenew("fixture renewal");
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
                    cancelled.countDown();
                }

                @Override
                public void onCompleted() {
                    if (mode == AuthorityMode.RENEWAL_CSR_SEND_FAILS_STREAM_STAYS_OPEN) {
                        // The client half-closed when its loop ended. A real
                        // authority keeps sending on the still-open call, which
                        // is exactly what this mode exists to reproduce.
                        return;
                    }
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

    /**
     * A ServiceConnection whose resolved target MOVES: the first attempt gets
     * the first channel, every later attempt gets the next one. This is the
     * fixture stand-in for an authority endpoint that was re-resolved to a new
     * address while a cert stream was retrying.
     */
    private static final class MovingAuthorityConnection extends ServiceConnection {
        private final ManagedChannel[] targets;
        private final AtomicInteger resolutions = new AtomicInteger();

        MovingAuthorityConnection(ManagedChannel... targets) {
            super(
                    "cert-stream-authority",
                    (serviceId, target) -> {
                        throw new AssertionError("the moving authority fixture resolves its own channels");
                    });
            this.targets = targets;
        }

        @Override
        public ManagedChannel conn() {
            int resolution = resolutions.getAndIncrement();
            return targets[Math.min(resolution, targets.length - 1)];
        }

        int resolutions() {
            return resolutions.get();
        }
    }

    /**
     * Runs a cert-stream LOOP (not a single session) on its own daemon thread.
     * The loop only returns on a fatal first delivery or an interrupt, so the
     * caller waits on the ready latch and then interrupts it.
     */
    private static Thread startCertStreamLoop(
            String loopName,
            ServiceManager sm,
            ServiceConnection connection,
            IdentityManager identity,
            CountDownLatch ready,
            AtomicReference<Exception> readyError,
            AtomicReference<Throwable> failure,
            String label) throws Exception {
        Method method = privateMethod(
                loopName,
                ServiceManager.class,
                ServiceConnection.class,
                IdentityManager.class,
                CountDownLatch.class,
                AtomicReference.class);
        Thread thread = new Thread(
                () -> {
                    try {
                        method.invoke(null, sm, connection, identity, ready, readyError);
                    } catch (InvocationTargetException ex) {
                        failure.set(ex.getCause());
                    } catch (Throwable err) {
                        failure.set(err);
                    }
                },
                label);
        thread.setDaemon(true);
        thread.start();
        return thread;
    }

    /**
     * Stops a cert-stream loop thread. One interrupt is not enough: if it lands
     * while the loop is inside a stream session, the session throws, the loop
     * catches it as a normal disconnect and clears the interrupt flag on its way
     * into the retry sleep. Re-interrupting until the thread exits stops it at
     * the sleep instead of waiting the full backoff out.
     */
    private static void stopCertStreamLoop(Thread loop) throws InterruptedException {
        for (int attempt = 0; attempt < 50 && loop.isAlive(); attempt++) {
            loop.interrupt();
            loop.join(100);
        }
        assertFalse(loop.isAlive(), loop.getName() + " loop did not exit after interrupt");
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
