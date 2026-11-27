// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the product-layer fixture's hub build (#475). NOT
// generated: run.sh copies this file beside the generated
// CrewPeerServiceGrpcClient of the hub build and runs it as a main-method
// self-test, like TenantRouteClientTest. It drives the REAL generated typed
// client of crew.CrewPeerService (tenant-owned, routed by its "tenant" field)
// over a fake Channel that records the tenant each call it is asked to dial is
// routed to. The client's token provider hands it the caller's own (unsigned)
// token, which is all the client reads.
//
// What it pins: the edge table of roadmap/PRODUCTS_LAYER.md at the client,
// before anything is sent (products-layer-2026-11-08): a product's service or
// workload never calls a tenant (PERMISSION_DENIED, nothing dialled), whatever
// tenant it names; the platform names the tenant it calls; a tenant's service
// reaches its own tenant and no other (#258).

package com.gitlab.example.dummy.hub.model.s2sm.internal.grpc.clients.services.crew;

import com.gitlab.example.dummy.hub.model.chassis.serviceconnection.RouteTenant;
import com.gitlab.example.dummy.hub.model.types.services.crew.Crew;
import com.gitlab.example.dummy.hub.model.types.sys.errors.ProtoException;

import io.grpc.CallOptions;
import io.grpc.Channel;
import io.grpc.ClientCall;
import io.grpc.Metadata;
import io.grpc.MethodDescriptor;
import io.grpc.Status;

import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Base64;
import java.util.List;

public class ProductLayerHubClientTest {
    private static int failures = 0;

    private static final String A = "aaaaaaaa";
    private static final String B = "bbbbbbbb";

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
     * A Channel that dials nothing: it records the route tenant call option of
     * each call and answers it with an empty response.
     */
    private static final class RoutedChannel extends Channel {
        final List<String> tenants = new ArrayList<>();

        @Override
        public <ReqT, RespT> ClientCall<ReqT, RespT> newCall(MethodDescriptor<ReqT, RespT> method, CallOptions options) {
            tenants.add(options.getOption(RouteTenant.CALL_OPTION));
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
                    listener.onMessage((RespT) Crew.GetCrewStatusResponse.getDefaultInstance());
                    listener.onClose(Status.OK, new Metadata());
                }
            };
        }

        @Override
        public String authority() {
            return "crew.fake";
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

    private static Crew.GetCrewStatusRequest req(String tenant) {
        return Crew.GetCrewStatusRequest.newBuilder().setTenant(tenant).build();
    }

    /** The call naming tenant is refused PERMISSION_DENIED before anything is dialled. */
    private static void refused(String what, String ownToken, String tenant) {
        what = what + " calling tenant-owned crew";
        RoutedChannel ch = new RoutedChannel();
        CrewPeerServiceGrpcClient client = new CrewPeerServiceGrpcClient(ch, () -> ownToken);
        try {
            client.getCrewStatus(req(tenant));
            fail(what + ": the call was sent, want PERMISSION_DENIED");
        } catch (ProtoException e) {
            expect(e.getCode() == ProtoException.PERMISSION_DENIED,
                what + ": code " + e.getCode() + ", want PERMISSION_DENIED (" + e.getMessage() + ")");
        } catch (RuntimeException e) {
            fail(what + ": untyped refusal " + e);
        }
        expect(ch.tenants.isEmpty(), what + ": " + ch.tenants.size() + " call(s) dialled, want none");
    }

    /** The call naming tenant goes out once, routed to want. */
    private static void sent(String what, String ownToken, String tenant, String want) {
        what = what + " calling tenant-owned crew";
        RoutedChannel ch = new RoutedChannel();
        CrewPeerServiceGrpcClient client = new CrewPeerServiceGrpcClient(ch, () -> ownToken);
        try {
            client.getCrewStatus(req(tenant));
        } catch (RuntimeException e) {
            fail(what + ": refused " + e);
            return;
        }
        if (ch.tenants.size() != 1) {
            fail(what + ": " + ch.tenants.size() + " call(s) dialled, want 1");
            return;
        }
        expect(want.equals(ch.tenants.get(0)), what + ": routed to \"" + ch.tenants.get(0) + "\", want " + want);
    }

    public static void main(String[] args) {
        refused("product ledger's service naming tenant bbbbbbbb",
            token("service", "spiffe://example.com/products/ledger/services/billing", "/products/ledger", null), B);
        refused("product ledger's workload naming tenant bbbbbbbb",
            token("workload", "spiffe://example.com/products/ledger/services/billing/workloads/w-1", "/products/ledger", null), B);

        sent("the platform's service naming tenant bbbbbbbb",
            token("service", "spiffe://example.com/system/services/hub", "/system", null), B, B);

        String tenantA = token("service", "spiffe://example.com/tenants/aaaaaaaa/services/crew", "/tenants/alpha", A);
        sent("tenant alpha's service with an empty tenant", tenantA, "", A);
        refused("tenant alpha's service naming tenant bbbbbbbb", tenantA, B);

        if (failures > 0) {
            System.err.println(failures + " ProductLayerHubClientTest check(s) FAILED");
            System.exit(1);
        }
        System.out.println("ProductLayerHubClientTest: all checks passed");
    }
}
