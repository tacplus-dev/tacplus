// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the product-layer fixture's billing build (#475). NOT
// generated: run.sh copies this file beside the generated
// MeterServiceGrpcClient of the billing build and runs it as a main-method
// self-test, like TenantRouteClientTest. It drives the REAL generated typed
// clients of meter.MeterService (product ledger's) and hub.HubService (the
// platform's) over a fake Channel that counts the calls it is asked to dial.
// The client's token provider hands it the caller's own (unsigned) token,
// which is all the client reads.
//
// What it pins: the edge table of roadmap/PRODUCTS_LAYER.md at the client,
// before anything is sent (products-layer-2026-11-08). Into product ledger:
// its own services and a tenant's pass; another product's and the platform's
// are refused PERMISSION_DENIED and never dialled. Into the platform: every
// layer passes.

package com.gitlab.example.dummy.billing.model.s2sm.internal.grpc.clients.services.meter;

import com.gitlab.example.dummy.billing.model.s2sm.internal.grpc.clients.services.hub.HubServiceGrpcClient;
import com.gitlab.example.dummy.billing.model.types.services.hub.Hub;
import com.gitlab.example.dummy.billing.model.types.services.meter.Meter;
import com.gitlab.example.dummy.billing.model.types.sys.errors.ProtoException;

import io.grpc.CallOptions;
import io.grpc.Channel;
import io.grpc.ClientCall;
import io.grpc.Metadata;
import io.grpc.MethodDescriptor;
import io.grpc.Status;

import java.nio.charset.StandardCharsets;
import java.util.Base64;

public class ProductLayerBillingClientTest {
    private static int failures = 0;

    private static void fail(String message) {
        System.err.println("FAIL: " + message);
        failures++;
    }

    private static void expect(boolean ok, String message) {
        if (!ok) {
            fail(message);
        }
    }

    /**
     * A Channel that dials nothing: it counts each call it is asked to make
     * and answers it with response, the called rpc's empty response.
     */
    private static final class CountingChannel extends Channel {
        final Object response;
        int calls = 0;

        CountingChannel(Object response) {
            this.response = response;
        }

        @Override
        public <ReqT, RespT> ClientCall<ReqT, RespT> newCall(MethodDescriptor<ReqT, RespT> method, CallOptions options) {
            calls++;
            return new ClientCall<ReqT, RespT>() {
                private Listener<RespT> listener;

                @Override public void start(Listener<RespT> l, Metadata headers) { listener = l; }
                @Override public void request(int n) {}
                @Override public void cancel(String message, Throwable cause) {}
                @Override public void sendMessage(ReqT message) {}

                @Override
                @SuppressWarnings("unchecked")
                public void halfClose() {
                    listener.onHeaders(new Metadata());
                    listener.onMessage((RespT) response);
                    listener.onClose(Status.OK, new Metadata());
                }
            };
        }

        @Override
        public String authority() {
            return "productlayer.fake";
        }
    }

    /** A caller in a layer: the claims of its own token. */
    private static final class Caller {
        final String name;
        final String token;

        Caller(String name, String subType, String sub, String namespace, String tid) {
            this.name = name;
            this.token = token(subType, sub, namespace, tid);
        }
    }

    /** An unsigned JWT with the given claims (tid null: omitted); the client reads, never verifies, its own token. */
    private static String token(String subType, String sub, String namespace, String tid) {
        StringBuilder payload = new StringBuilder("{\"iss\":\"https://kernel.example\"");
        payload.append(",\"sub\":\"").append(sub).append('"');
        payload.append(",\"sub_type\":\"").append(subType).append('"');
        payload.append(",\"namespace\":\"").append(namespace).append('"');
        if (tid != null) {
            payload.append(",\"tid\":\"").append(tid).append('"');
        }
        payload.append('}');
        Base64.Encoder enc = Base64.getUrlEncoder().withoutPadding();
        return enc.encodeToString("{\"alg\":\"EdDSA\",\"typ\":\"JWT\"}".getBytes(StandardCharsets.UTF_8)) + "."
            + enc.encodeToString(payload.toString().getBytes(StandardCharsets.UTF_8)) + ".c2ln";
    }

    private static final Caller LEDGER_SERVICE = new Caller("product ledger's service", "service",
        "spiffe://example.com/products/ledger/services/billing", "/products/ledger", null);
    private static final Caller LEDGER_WORKLOAD = new Caller("product ledger's workload", "workload",
        "spiffe://example.com/products/ledger/services/billing/workloads/w-1", "/products/ledger", null);
    private static final Caller BOOKS_SERVICE = new Caller("product books' service", "service",
        "spiffe://example.com/products/books/services/audit", "/products/books", null);
    private static final Caller SYSTEM_SERVICE = new Caller("the platform's service", "service",
        "spiffe://example.com/system/services/hub", "/system", null);
    private static final Caller TENANT_SERVICE = new Caller("tenant alpha's service", "service",
        "spiffe://example.com/tenants/aaaaaaaa/services/crew", "/tenants/alpha", "aaaaaaaa");

    /** Calls product ledger's meter as caller: sent once, or refused PERMISSION_DENIED with nothing dialled. */
    private static void intoMeter(Caller caller, boolean refused) {
        String what = caller.name + " calling product ledger's meter";
        CountingChannel ch = new CountingChannel(Meter.GetMeterReadingResponse.getDefaultInstance());
        MeterServiceGrpcClient client = new MeterServiceGrpcClient(ch, () -> caller.token);
        try {
            client.getMeterReading(Meter.GetMeterReadingRequest.getDefaultInstance());
            if (refused) {
                fail(what + ": the call was sent, want PERMISSION_DENIED");
            }
        } catch (ProtoException e) {
            if (refused) {
                expect(e.getCode() == ProtoException.PERMISSION_DENIED,
                    what + ": code " + e.getCode() + ", want PERMISSION_DENIED (" + e.getMessage() + ")");
            } else {
                fail(what + ": refused " + e);
            }
        } catch (RuntimeException e) {
            fail(what + ": untyped failure " + e);
        }
        int want = refused ? 0 : 1;
        expect(ch.calls == want, what + ": " + ch.calls + " call(s) dialled, want " + want);
    }

    /** Calls the platform's hub as caller: always sent once. */
    private static void intoHub(Caller caller) {
        String what = caller.name + " calling the platform's hub";
        CountingChannel ch = new CountingChannel(Hub.GetHubStatusResponse.getDefaultInstance());
        HubServiceGrpcClient client = new HubServiceGrpcClient(ch, () -> caller.token);
        try {
            client.getHubStatus(Hub.GetHubStatusRequest.getDefaultInstance());
        } catch (RuntimeException e) {
            fail(what + ": refused " + e);
        }
        expect(ch.calls == 1, what + ": " + ch.calls + " call(s) dialled, want 1");
    }

    public static void main(String[] args) {
        intoMeter(LEDGER_SERVICE, false);
        intoMeter(LEDGER_WORKLOAD, false);
        intoMeter(TENANT_SERVICE, false);
        intoMeter(BOOKS_SERVICE, true);
        intoMeter(SYSTEM_SERVICE, true);

        for (Caller caller : new Caller[] {LEDGER_SERVICE, BOOKS_SERVICE, SYSTEM_SERVICE, TENANT_SERVICE}) {
            intoHub(caller);
        }

        if (failures > 0) {
            System.err.println(failures + " ProductLayerBillingClientTest check(s) FAILED");
            System.exit(1);
        }
        System.out.println("ProductLayerBillingClientTest: all checks passed");
    }
}
