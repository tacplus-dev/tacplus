// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for tacplus#273. NOT generated: run.sh copies this file into
// the accounting-sink fixture's generated AgentApi wrapper package and runs it
// as a main class.
//
// It drives the real generated AgentApi wrapper over a real gRPC socket, with
// the real generated headerCaptureInterceptor, Emitter and EventBusSink, and
// reads the AccountingRecords the sink hands the publisher:
//   - a query that returns N records produces exactly one record, PERMITTED,
//     code 0, records_returned=N, and no request or response value;
//   - a denied call produces exactly one record, DENIED_AUTHZ / NOT_ENTITLED
//     (code 7, WARN) and no error text; an unauthenticated one DENIED_AUTHN /
//     TOKEN_REJECTED;
//   - a call carrying the accounting_internal marker made by a service caller
//     produces no record, while a user's call with the marker is still recorded.
//
// Only the token check is stood in for (as the Go self-test does with its
// authorize func): the Authorizer returns the token the case names, recording it
// as verified exactly as the real verifyBearer does, or refuses with the case's
// status.

package com.gitlab.example.dummy.agentsensor.model.s2sm.internal.grpc.servers.services.agentsensor;

import com.gitlab.example.dummy.agentsensor.model.chassis.am.AccountingManager;
import com.gitlab.example.dummy.agentsensor.model.chassis.grpcserver.Authorizer;
import com.gitlab.example.dummy.agentsensor.model.chassis.servicemanager.Auth;
import com.gitlab.example.dummy.agentsensor.model.chassis.servicemanager.Caller;
import com.gitlab.example.dummy.agentsensor.model.chassis.token.TokenWrapper;
import com.gitlab.example.dummy.agentsensor.model.types.services.agentsensor.AgentApiGrpc;
import com.gitlab.example.dummy.agentsensor.model.types.services.agentsensor.Agentsensor;
import com.gitlab.example.dummy.agentsensor.model.types.sys.accounting.Accounting;
import com.gitlab.example.dummy.agentsensor.model.types.sys.errors.ProtoException;
import com.gitlab.example.dummy.agentsensor.model.types.vservices.accountingrecord.Accountingrecord;

import com.google.protobuf.ByteString;
import io.grpc.ClientInterceptors;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.Server;
import io.grpc.ServerBuilder;
import io.grpc.Status;
import io.grpc.StatusRuntimeException;
import io.grpc.stub.MetadataUtils;

import java.nio.charset.StandardCharsets;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.TimeUnit;

public final class AccountingOutcomeTest {
    private static final String SECRET_FILTER = "SECRET-FILTER-VALUE";
    private static final String SECRET_ID = "SECRET-EVENT-ID";
    private static final String SECRET_DENIAL = "SECRET-DENIAL-DETAIL";
    private static final int QUERY_N = 3;

    private static void check(boolean ok, String message) {
        if (!ok) {
            throw new AssertionError(message);
        }
    }

    private static final class QueryHandler implements AgentApi.AgentApiHandler {
        @Override
        public Agentsensor.AgentEvent ping(Caller caller, Agentsensor.AgentEvent request) {
            return Agentsensor.AgentEvent.newBuilder().setId(SECRET_ID).build();
        }

        @Override
        public Agentsensor.GetAgentEventsResponse getAgentEvents(Caller caller, Agentsensor.GetAgentEventsRequest request) {
            Agentsensor.GetAgentEventsResponse.Builder resp = Agentsensor.GetAgentEventsResponse.newBuilder().setTotal(QUERY_N);
            for (int i = 0; i < QUERY_N; i++) {
                resp.addEvents(Agentsensor.AgentEvent.newBuilder()
                        .setId(SECRET_ID)
                        .setBody(ByteString.copyFrom(request.getFilter(), StandardCharsets.UTF_8)));
            }
            AccountingManager.Metrics.setRecordsReturned(resp.getEventsCount());
            return resp.build();
        }
    }

    /** The Authorizer of one case: it verifies tok, or refuses with refusal. */
    private static final class CaseAuthorizer extends Authorizer {
        private final TokenWrapper tok;
        private final ProtoException refusal;

        CaseAuthorizer(TokenWrapper tok, ProtoException refusal) {
            super(sink -> { });
            this.tok = tok;
            this.refusal = refusal;
        }

        @Override
        public TokenWrapper authorize(String fullMethod, Object request) throws ProtoException {
            if (refusal != null) {
                throw refusal;
            }
            Auth.recordVerifiedToken(tok);
            return tok;
        }
    }

    /** One accounted server: the real wrapper, the real sink, a real socket. */
    private static final class Fixture implements AutoCloseable {
        final BlockingQueue<Accountingrecord.AccountingRecord> tx = new ArrayBlockingQueue<>(16);
        private volatile boolean stopped;
        private final Server server;
        private final ManagedChannel channel;

        Fixture(TokenWrapper tok, ProtoException refusal) throws Exception {
            AgentApi wrapper = new AgentApi(new QueryHandler(), new CaseAuthorizer(tok, refusal));
            wrapper.setEmitter(new AccountingManager.Emitter("agentsensor-test",
                    new AccountingManager.EventBusSink(tx, "agentsensor-test", () -> stopped)));
            ServerBuilder<?> builder = ServerBuilder.forPort(0).intercept(Authorizer.headerCaptureInterceptor());
            wrapper.registerServices(builder);
            server = builder.build().start();
            channel = ManagedChannelBuilder.forAddress("localhost", server.getPort()).usePlaintext().build();
        }

        Agentsensor.GetAgentEventsResponse query(boolean marked) {
            io.grpc.Channel ch = channel;
            if (marked) {
                ch = ClientInterceptors.intercept(channel, AccountingManager.internalMarker());
            }
            return AgentApiGrpc.newBlockingStub(ch)
                    .withDeadlineAfter(15, TimeUnit.SECONDS)
                    .getAgentEvents(Agentsensor.GetAgentEventsRequest.newBuilder().setFilter(SECRET_FILTER).build());
        }

        @Override
        public void close() throws Exception {
            channel.shutdownNow();
            server.shutdownNow();
            server.awaitTermination(5, TimeUnit.SECONDS);
            stopped = true;
        }
    }

    private static Accountingrecord.AccountingRecord exactlyOne(Fixture f) throws InterruptedException {
        Accountingrecord.AccountingRecord rec = f.tx.poll(5, TimeUnit.SECONDS);
        check(rec != null, "the call produced no accounting record");
        Accountingrecord.AccountingRecord extra = f.tx.poll(300, TimeUnit.MILLISECONDS);
        check(extra == null, "one call produced a second accounting record: " + extra);
        return rec;
    }

    private static void noRecord(Fixture f) throws InterruptedException {
        Accountingrecord.AccountingRecord rec = f.tx.poll(500, TimeUnit.MILLISECONDS);
        check(rec == null, "a service's marked call produced an accounting record: " + rec);
    }

    private static void noData(Accountingrecord.AccountingRecord rec) {
        String raw = new String(rec.toByteArray(), StandardCharsets.ISO_8859_1);
        for (String v : new String[] {SECRET_FILTER, SECRET_ID, SECRET_DENIAL}) {
            check(!raw.contains(v), "accounting record carries data \"" + v + "\": " + rec);
        }
    }

    private static TokenWrapper token(String subType, String sub) {
        return TokenWrapper.create().setSubType(subType).setSubject(sub);
    }

    private static void checkQueryRecordsOutcomeAndRecordsReturned() throws Exception {
        try (Fixture f = new Fixture(token("user", "alice"), null)) {
            Agentsensor.GetAgentEventsResponse resp = f.query(false);
            check(resp.getEventsCount() == QUERY_N, "query returned " + resp.getEventsCount() + " events");
            Accountingrecord.AccountingRecord rec = exactlyOne(f);
            check(rec.getOutcome() == Accounting.RpcOutcome.RPC_OUTCOME_PERMITTED
                    && rec.getCode() == 0
                    && rec.getReason() == Accounting.RpcReason.RPC_REASON_UNDEFINED
                    && rec.getRecordsReturned() == QUERY_N,
                    "query record: want PERMITTED, code 0, no reason, records_returned=" + QUERY_N + "; got " + rec);
            check("GetAgentEvents".equals(rec.getMethod()) && rec.getLevel() == Accounting.EventLevel.EVENT_LEVEL_INFO,
                    "query record: method " + rec.getMethod() + " level " + rec.getLevel());
            check("alice".equals(rec.getPrincipalSub()), "query record attributed to " + rec.getPrincipalSub());
            noData(rec);
        }
    }

    private static void checkDenied(String name, ProtoException refusal, Status.Code code,
                                    Accounting.RpcOutcome outcome, Accounting.RpcReason reason) throws Exception {
        try (Fixture f = new Fixture(null, refusal)) {
            try {
                f.query(false);
                throw new AssertionError(name + ": denied call was served");
            } catch (StatusRuntimeException e) {
                check(e.getStatus().getCode() == code, name + ": denied call returned " + e.getStatus() + ", want " + code);
            }
            Accountingrecord.AccountingRecord rec = exactlyOne(f);
            check(rec.getOutcome() == outcome && rec.getReason() == reason && rec.getCode() == code.value()
                    && rec.getLevel() == Accounting.EventLevel.EVENT_LEVEL_WARN && rec.getRecordsReturned() == 0
                    && rec.getPrincipalSub().isEmpty(),
                    name + ": want " + outcome + " " + reason + " code " + code.value() + " WARN, no identity; got " + rec);
            noData(rec);
        }
    }

    private static void checkMarker() throws Exception {
        // A service storing accounting carries the marker: no record.
        try (Fixture f = new Fixture(token("service", "stratum"), null)) {
            f.query(true);
            noRecord(f);
        }
        // A user cannot opt out of its audit trail with the marker.
        try (Fixture f = new Fixture(token("user", "mallory"), null)) {
            f.query(true);
            Accountingrecord.AccountingRecord rec = exactlyOne(f);
            check("mallory".equals(rec.getPrincipalSub())
                    && rec.getOutcome() == Accounting.RpcOutcome.RPC_OUTCOME_PERMITTED,
                    "a user's call with the marker must still be recorded as theirs: " + rec);
        }
        // A service without the marker is recorded.
        try (Fixture f = new Fixture(token("service", "stratum"), null)) {
            f.query(false);
            exactlyOne(f);
        }
    }

    public static void main(String[] args) throws Exception {
        checkQueryRecordsOutcomeAndRecordsReturned();
        checkDenied("authz", ProtoException.newErrPermissionDenied(null, SECRET_DENIAL), Status.Code.PERMISSION_DENIED,
                Accounting.RpcOutcome.RPC_OUTCOME_DENIED_AUTHZ, Accounting.RpcReason.RPC_REASON_NOT_ENTITLED);
        checkDenied("authn", ProtoException.newErrUnauthorized(null, SECRET_DENIAL), Status.Code.UNAUTHENTICATED,
                Accounting.RpcOutcome.RPC_OUTCOME_DENIED_AUTHN, Accounting.RpcReason.RPC_REASON_TOKEN_REJECTED);
        checkMarker();
        System.out.println("AccountingOutcomeTest: all checks passed");
    }
}
