// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for "S2S never crosses tenants" in the generated Java S2S
// client (#258, roadmap decision decisions/s2s-never-crosses-tenants-2026-10-31.md).
// NOT generated: run.sh copies this file beside the generated
// CrewPeerServiceGrpcClient of the tenant-route fixture's harbor build and runs
// it as a main-method self-test, like TenantRecordRouteTest.
//
// It drives the REAL generated typed client of crew.CrewPeerService (whose
// GetCrewStatus is routed by route_field "tenant") over a fake Channel that
// records every call it is asked to dial: the tenant the call option carried
// and the request it sent. The client's token provider hands it the caller's
// own (unsigned) token, which is all the client reads.
//
// What it pins: a tenant's own service (tid aaaaaaaa) cannot name another
// tenant (refused PERMISSION_DENIED, nothing dialled); its empty route field
// is stamped with its own tid, on the request and the call's route; its own tid
// goes out as named; a tenant's service token without a tid claim fails closed;
// a /system service still names the target tenant, and an empty one is still
// refused INVALID_ARGUMENT.

package com.gitlab.example.dummy.harbor.model.s2sm.internal.grpc.clients.services.crew;

import com.gitlab.example.dummy.harbor.model.chassis.serviceconnection.RouteTenant;
import com.gitlab.example.dummy.harbor.model.types.services.crew.Crew;
import com.gitlab.example.dummy.harbor.model.types.sys.errors.ProtoException;

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

public class TenantRouteClientTest {
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

    /** One call the fake channel was asked to dial. */
    private static final class Dialled {
        String routeTenant;
        Crew.GetCrewStatusRequest request;
    }

    /**
     * A Channel that dials nothing: it records each call (its route tenant
     * call option and the request sent) and answers it with an empty response.
     */
    private static final class FakeChannel extends Channel {
        final List<Dialled> calls = new ArrayList<>();

        @Override
        public <ReqT, RespT> ClientCall<ReqT, RespT> newCall(MethodDescriptor<ReqT, RespT> method, CallOptions options) {
            Dialled d = new Dialled();
            d.routeTenant = options.getOption(RouteTenant.CALL_OPTION);
            calls.add(d);
            return new ClientCall<ReqT, RespT>() {
                private Listener<RespT> listener;

                @Override public void start(Listener<RespT> l, Metadata headers) { listener = l; }
                @Override public void request(int n) {}
                @Override public void cancel(String message, Throwable cause) {}
                @Override public void sendMessage(ReqT message) { d.request = (Crew.GetCrewStatusRequest) message; }

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

    /** An unsigned JWT with the given claims (null: omitted); the client reads, never verifies, its own token. */
    private static String token(String subType, String namespace, String tid) {
        StringBuilder payload = new StringBuilder("{\"sub\":\"spiffe://example.com/services/harbor\"");
        if (subType != null) {
            payload.append(",\"sub_type\":\"").append(subType).append('"');
        }
        if (namespace != null) {
            payload.append(",\"namespace\":\"").append(namespace).append('"');
        }
        if (tid != null) {
            payload.append(",\"tid\":\"").append(tid).append('"');
        }
        payload.append('}');
        Base64.Encoder enc = Base64.getUrlEncoder().withoutPadding();
        return enc.encodeToString("{\"alg\":\"ES256\",\"typ\":\"JWT\"}".getBytes(StandardCharsets.UTF_8)) + "."
            + enc.encodeToString(payload.toString().getBytes(StandardCharsets.UTF_8)) + ".c2ln";
    }

    private static Crew.GetCrewStatusRequest req(String tenant) {
        return Crew.GetCrewStatusRequest.newBuilder().setTenant(tenant).build();
    }

    /** The call is refused with code before anything is dialled. */
    private static void refused(String what, String ownToken, String tenant, int code) {
        FakeChannel ch = new FakeChannel();
        CrewPeerServiceGrpcClient client = new CrewPeerServiceGrpcClient(ch, () -> ownToken);
        try {
            client.getCrewStatus(req(tenant));
            fail(what + ": the call was sent");
        } catch (ProtoException e) {
            expect(e.getCode() == code, what + ": code " + e.getCode() + ", want " + code + " (" + e.getMessage() + ")");
        } catch (RuntimeException e) {
            fail(what + ": untyped refusal " + e);
        }
        expect(ch.calls.isEmpty(), what + ": " + ch.calls.size() + " call(s) dialled, want none");
    }

    /** The call goes out once, routed to want, its request naming want. */
    private static void sent(String what, String ownToken, String tenant, String want) {
        FakeChannel ch = new FakeChannel();
        CrewPeerServiceGrpcClient client = new CrewPeerServiceGrpcClient(ch, () -> ownToken);
        try {
            client.getCrewStatus(req(tenant));
        } catch (RuntimeException e) {
            fail(what + ": refused " + e);
            return;
        }
        if (ch.calls.size() != 1) {
            fail(what + ": " + ch.calls.size() + " call(s) dialled, want 1");
            return;
        }
        Dialled d = ch.calls.get(0);
        expect(want.equals(d.routeTenant), what + ": routed to \"" + d.routeTenant + "\", want " + want);
        expect(d.request != null && want.equals(d.request.getTenant()),
            what + ": the request sent names \"" + (d.request == null ? null : d.request.getTenant()) + "\", want " + want);
    }

    public static void main(String[] args) {
        String serviceA = token("service", "/tenants/alpha", A);
        refused("tenant A's service naming tenant B", serviceA, B, ProtoException.PERMISSION_DENIED);
        sent("tenant A's service with an empty tenant", serviceA, "", A);
        sent("tenant A's service naming its own tenant", serviceA, A, A);

        String workloadA = token("workload", "/tenants/alpha", A);
        refused("tenant A's workload naming tenant B", workloadA, B, ProtoException.PERMISSION_DENIED);
        sent("tenant A's workload with an empty tenant", workloadA, "", A);

        refused("a tenant's service token without a tid claim", token("service", "/tenants/alpha", null), "",
            ProtoException.PERMISSION_DENIED);

        String system = token("service", "/system", null);
        sent("a /system service naming tenant B", system, B, B);
        refused("a /system service with an empty tenant", system, "", ProtoException.INVALID_ARGUMENT);

        if (failures > 0) {
            System.err.println(failures + " TenantRouteClientTest check(s) FAILED");
            System.exit(1);
        }
        System.out.println("TenantRouteClientTest: all checks passed");
    }
}
