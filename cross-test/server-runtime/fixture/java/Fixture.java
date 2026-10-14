// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

// server-runtime JAVA fixture server: hosts the REAL generated ContractApi
// server wrapper (s2sm/.../contract/contractclient/ContractApi.java) on a real
// TCP port so another language's generated client can drive it over the wire.
//
// Nothing here re-implements the gRPC surface: this file supplies only a
// ContractApiHandler (the clean handler interface the generator emits for host
// services to implement), the mandatory Authorizer, and an auth_any_user
// Authenticator. Request/response validation, collection PAGE token encoding,
// egress sanitization, auth gating and dispatch all live in the generated
// wrapper.
//
// Startup contract: prints a single JSON line to stdout
//
//     {"grpc":"127.0.0.1:N"}
//
// then serves until SIGINT/SIGTERM.
package fixture;

import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.collection.CollectionRuntime;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.grpcserver.Authorizer;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.servicemanager.Caller;
import com.gitlab.example.tacplus.consumer.contractclient.model.s2sm.internal.grpc.servers.contract.contractclient.ContractApi;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.contract.contractclient.Contractclient;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.sys.errors.ProtoException;

import io.grpc.Server;
import io.grpc.ServerBuilder;
import io.grpc.stub.StreamObserver;

import java.util.Iterator;
import java.util.List;

public final class Fixture {

    private static final String BEARER = "Bearer fixture-token";

    private Fixture() {}

    private static Contractclient.ContractResponse validResponse() {
        return Contractclient.ContractResponse.newBuilder()
        .setResultId("result-123")
        .setAccepted(true)
        .build();
    }

    private static Contractclient.ContractEvent event(String id) {
        return Contractclient.ContractEvent.newBuilder()
        .setEventId(id)
        .setTenantNamespace("tenant.alpha")
        .setPayload("ok")
        .build();
    }

    /** Handler half only — the generated wrapper owns everything else. */
    static final class Handler implements ContractApi.ContractApiHandler {

        @Override
        public Contractclient.ContractResponse getContract(Caller caller, Contractclient.ContractRequest request) {
            // Never reachable: the generated wrapper authorizes GetContract
            // before dispatch and the fixture wires no verifying key. If a
            // client ever sees this marker, auth was bypassed.
            return Contractclient.ContractResponse.newBuilder()
            .setResultId("HANDLER-REACHED")
            .setAccepted(true)
            .build();
        }

        @Override
        public Contractclient.ContractResponse getPublicContract(Caller caller, Contractclient.ContractRequest request) {
            return validResponse();
        }

        @Override
        public Iterator<Contractclient.ContractEvent> listEvents(Caller caller, Contractclient.ContractRequest request) {
            return List.of(event("event-1")).iterator();
        }

        @Override
        public CollectionRuntime.WatchSource<Contractclient.ContractEvent, Contractclient.ContractWatchChange> watch(
        Caller caller, Contractclient.ContractWatchRequest request) throws ProtoException {
            throw ProtoException.newErrGeneral(null, "watch is out of scope for the server-runtime gate");
        }

        @Override
        public CollectionRuntime.Page<Contractclient.ContractEvent> listEventsPaged(
        Caller caller, Contractclient.ContractPageRequest request, CollectionRuntime.PageParams page)
        throws ProtoException {
            // TWO keyset pages, matching the go runtime-smoke fixture: the first
            // call returns a resume cursor so the client's auto-pager is forced
            // through the token-CONTINUATION branch on the wire.
            if (page.after == null) {
                return new CollectionRuntime.Page<>(
                List.of(event("event-1")), new CollectionRuntime.Cursor(List.of("event-1")), 2);
            }
            if (page.after.keys.size() != 1 || !"event-1".equals(page.after.keys.get(0))) {
                throw ProtoException.newErrMissingInvalidData(null, "unexpected resume cursor %s", page.after.keys);
            }
            return new CollectionRuntime.Page<>(List.of(event("event-2")), null, 2);
        }

        @Override
        public StreamObserver<Contractclient.ContractEvent> sendEvents(
        Caller caller, StreamObserver<Contractclient.ContractResponse> responseObserver) {
            return new StreamObserver<>() {
                @Override public void onNext(Contractclient.ContractEvent value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {
                    responseObserver.onNext(validResponse());
                    responseObserver.onCompleted();
                }
            };
        }

        @Override
        public StreamObserver<Contractclient.ContractEvent> assistEvents(
        Caller caller, StreamObserver<Contractclient.ContractEvent> responseObserver) {
            return new StreamObserver<>() {
                @Override public void onNext(Contractclient.ContractEvent value) {
                    responseObserver.onNext(event("event-reply"));
                }
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() { responseObserver.onCompleted(); }
            };
        }
    }

    public static void main(String[] args) throws Exception {
        ContractApi wrapper = new ContractApi(new Handler(), Authorizer.create());
        // auth_any_user seam: the generated wrapper calls this before ListEvents /
        // ListEventsPaged / Watch. The authorization header reaches us through
        // the generated Authorizer.headerCaptureInterceptor() context key.
        wrapper.setAuthenticateFunc((ContractApi.Authenticator) targetNamespace -> {
            String header = Authorizer.AUTH_HEADER_CTX_KEY.get();
            if (!BEARER.equals(header)) {
                throw ProtoException.newErrUnauthorized(null, "unexpected authorization header");
            }
            if (!"/".equals(targetNamespace) && !targetNamespace.isEmpty()) {
                throw ProtoException.newErrPermissionDenied(null, "unexpected target namespace " + targetNamespace);
            }
        });

        ServerBuilder<?> builder = ServerBuilder.forPort(0).intercept(Authorizer.headerCaptureInterceptor());
        wrapper.registerServices(builder);
        Server server = builder.build().start();

        System.out.println("{\"grpc\":\"127.0.0.1:" + server.getPort() + "\"}");
        System.out.flush();

        Runtime.getRuntime().addShutdownHook(new Thread(server::shutdownNow));
        server.awaitTermination();
    }
}
