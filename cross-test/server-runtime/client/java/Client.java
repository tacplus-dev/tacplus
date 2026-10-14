// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

// server-runtime JAVA client driver (LED-2 phase 3): the GENERATED JAVA C2S
// CLIENT (c2sm/.../contract/contractclient/ContractApiGrpcClient.java), dialed
// at a generated server hosted by ANOTHER language's fixture (rust). Nothing
// here speaks gRPC by hand: request validation, bearer-token attachment, error
// mapping, the PAGE auto-pager and response validation are all generated code.
//
// Mirrors client/go/main.go leg for leg so the two driving clients assert the
// same wire facts about the same hosted servers.
//
// Reads TACPLUS_SERVER_ADDR (host:port) and TACPLUS_SERVER_LANG (label used in
// failure messages only).
package client;

import com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.ServiceManager;
import com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.internal.grpc.clients.contract.contractclient.ContractApiGrpcClient;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.contract.contractclient.Contractclient;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.sys.errors.ProtoException;

import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;

import java.util.ArrayList;
import java.util.Iterator;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;

public final class Client {

    private static final String FIXTURE_TOKEN = "fixture-token";

    private Client() {}

    private static Contractclient.ContractRequest validRequest() {
        return Contractclient.ContractRequest.newBuilder()
        .setTenantNamespace("tenant.alpha")
        .setItemId("item-123")
        .setMode(Contractclient.ContractMode.CONTRACT_MODE_READ)
        .addTags("one")
        .putAllMetadata(Map.of("owner", "server-runtime"))
        .build();
    }

    public static void main(String[] args) throws Exception {
        String addr = System.getenv("TACPLUS_SERVER_ADDR");
        if (addr == null || addr.isBlank()) {
            fail("TACPLUS_SERVER_ADDR is required");
        }
        String lang = System.getenv("TACPLUS_SERVER_LANG");
        if (lang == null || lang.isBlank()) {
            fail("TACPLUS_SERVER_LANG is required");
        }

        ManagedChannel channel = ManagedChannelBuilder.forTarget(addr).usePlaintext().build();
        try {
            ServiceManager sm = new ServiceManager(channel, () -> FIXTURE_TOKEN);
            ContractApiGrpcClient api = sm.grpc().contract().contractApi();

            checkUnary(api, lang);
            checkServerStream(api, lang);
            checkPage(api, lang);
            checkFailClosed(api, lang);
        } finally {
            channel.shutdownNow();
            channel.awaitTermination(5, TimeUnit.SECONDS);
        }

        System.out.printf("java generated client OK against %s generated server (%s)%n", lang, addr);
    }

    // checkUnary: auth_disabled unary. The remote generated wrapper validates
    // the request, dispatches, validates the response and marshals it back.
    private static void checkUnary(ContractApiGrpcClient api, String lang) {
        Contractclient.ContractResponse resp = api.getPublicContract(validRequest());
        if (!"result-123".equals(resp.getResultId())) {
            fail("%s GetPublicContract result_id = %s, want result-123", lang, resp.getResultId());
        }
        if (!resp.getAccepted()) {
            fail("%s GetPublicContract accepted = false, want true", lang);
        }
        System.out.printf("  %s: GetPublicContract unary round-trip ok%n", lang);
    }

    // checkServerStream: auth_any_user server-streaming (SNAPSHOT).
    private static void checkServerStream(ContractApiGrpcClient api, String lang) {
        List<String> ids = new ArrayList<>();
        try (ContractApiGrpcClient.CloseableIterator<Contractclient.ContractEvent> it =
        api.listEvents(validRequest())) {
            while (it.hasNext()) {
                Contractclient.ContractEvent event = it.next();
                if (!"tenant.alpha".equals(event.getTenantNamespace())) {
                    fail("%s ListEvents tenant_namespace = %s, want tenant.alpha", lang, event.getTenantNamespace());
                }
                ids.add(event.getEventId());
            }
        }
        if (ids.size() != 1 || !"event-1".equals(ids.get(0))) {
            fail("%s ListEvents event ids = %s, want [event-1]", lang, ids);
        }
        System.out.printf("  %s: ListEvents SNAPSHOT stream round-trip ok%n", lang);
    }

    // checkPage: auth_any_user PAGE. The generated auto-pager follows the remote
    // server's next_page_token, so two pages prove the remote server encoded a
    // resume cursor and decoded it back on the follow-up call.
    private static void checkPage(ContractApiGrpcClient api, String lang) {
        List<String> ids = new ArrayList<>();
        Iterator<Contractclient.ContractEvent> it = api.listEventsPaged(
        Contractclient.ContractPageRequest.newBuilder()
        .setTenantNamespace("tenant.alpha")
        .setPageSize(10)
        .build());
        while (it.hasNext()) {
            ids.add(it.next().getEventId());
        }
        if (ids.size() != 2 || !"event-1".equals(ids.get(0)) || !"event-2".equals(ids.get(1))) {
            fail("%s ListEventsPaged event ids = %s, want [event-1, event-2]", lang, ids);
        }
        System.out.printf("  %s: ListEventsPaged PAGE keyset continuation round-trip ok%n", lang);
    }

    // checkFailClosed: auth_entitled_users unary with a token the remote server
    // cannot accept. The remote generated wrapper must reject before the handler
    // — the fixture handlers return result_id "HANDLER-REACHED" for this method,
    // so a success (or that marker) here means auth was bypassed.
    //
    // Both hosted fixtures run the REAL generated Authorizer over an explicitly
    // empty verifying-key set, so the opaque fixture bearer is rejected at token
    // verification in both languages: UNAUTHENTICATED, exactly, either side.
    private static void checkFailClosed(ContractApiGrpcClient api, String lang) {
        try {
            Contractclient.ContractResponse resp = api.getContract(validRequest());
            fail("%s GetContract was accepted (result_id=%s) — auth is not fail-closed", lang, resp.getResultId());
        } catch (ProtoException e) {
            int code = e.getCode();
            if (code != ProtoException.UNAUTHENTICATED) {
                fail("%s GetContract rejected with code %d, want UNAUTHENTICATED(16): %s",
                lang, code, e.getMessage());
            }
            System.out.printf("  %s: GetContract fail-closed rejection ok (code %d)%n", lang, code);
        }
    }

    private static void fail(String format, Object... args) {
        System.err.printf("server-runtime java client FAILED: " + format + "%n", args);
        System.exit(1);
    }
}
