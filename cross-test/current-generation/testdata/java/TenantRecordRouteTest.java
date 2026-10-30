// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the tenant-routed record plane (#207). NOT generated:
// run.sh copies this file into the generated chassis/grpcserver package of the
// tstratum (tenant-owned record store) host build and runs it as a main-method
// self-test, like RecordWriteGuardTest. It lives in that package for
// Authorizer.SERVICE_NAME, the audience a token must carry.
//
// It drives the REAL generated TenantRecordMap wrapper in-process: a fake
// handler behind it, standing for the store and its redo (a call that never
// reaches it leaves nothing in either), and the REAL generated Authorizer in
// front of it, wired as the generated ServiceManager wires the instance of
// tenant bbbbbbbb: the method auth the wrapper registers, the host namespace
// /tenants/bbbbbbbb (the door and CheckRoute), over real signed tokens.
//
// What it pins: a call routed to another tenant than the instance serves is
// refused (the route check); an entry outside the tenant the call is routed
// to is refused, whoever the caller is, a /system peer included (the route
// pin), on Put, Delete, Get, Watch and per op of Apply; a call with no route
// is refused; another tenant's caller does not get in; and a call that is
// routed right, for an entry in that tenant, reaches the store.

package com.gitlab.example.dummy.tstratum.model.chassis.grpcserver;

import com.gitlab.example.dummy.tstratum.model.chassis.key.ImmutableKey;
import com.gitlab.example.dummy.tstratum.model.chassis.key.KeyHelpers;
import com.gitlab.example.dummy.tstratum.model.chassis.servicemanager.Auth;
import com.gitlab.example.dummy.tstratum.model.chassis.token.TokenWrapper;
import com.gitlab.example.dummy.tstratum.model.s2sm.internal.grpc.servers.vservices.tquery.TenantRecordMap;
import com.gitlab.example.dummy.tstratum.model.types.services.tmessaging.Tmessaging;
import com.gitlab.example.dummy.tstratum.model.types.sys.jwk.JwkOuterClass;
import com.gitlab.example.dummy.tstratum.model.types.sys.key.KeyOuterClass;
import com.gitlab.example.dummy.tstratum.model.types.vservices.tquery.Tquery;

import io.grpc.Context;
import io.grpc.Status;
import io.grpc.stub.StreamObserver;

import java.lang.reflect.Proxy;
import java.util.ArrayList;
import java.util.Base64;
import java.util.List;
import java.util.Map;

public class TenantRecordRouteTest {
    private static int failures = 0;

    private static void fail(String message) {
        System.err.println("FAIL: " + message);
        failures++;
    }

    private static final String A = "aaaaaaaa";
    private static final String B = "bbbbbbbb";
    private static final String SYSTEM = "/system";
    private static final String SYSTEM_SUBJECT = "spiffe://example.com/system/services/tmessaging";

    /** The rpcs of the store the fake handler was reached on; empty: nothing stored, nothing in redo. */
    private static final List<String> reached = new ArrayList<>();

    /** The store: records which rpc reached it and answers a durable write; any other method throws. */
    private static TenantRecordMap.TenantRecordMapHandler handler() {
        return (TenantRecordMap.TenantRecordMapHandler) Proxy.newProxyInstance(
            TenantRecordRouteTest.class.getClassLoader(),
            new Class<?>[] {TenantRecordMap.TenantRecordMapHandler.class},
            (proxy, method, args) -> {
                reached.add(method.getName());
                switch (method.getName()) {
                    case "putTMessage":
                        return Tquery.RecordWriteResult.newBuilder().setRevision(1)
                            .setDurability(((Tquery.PutTMessageRequest) args[1]).getDurability()).build();
                    case "deleteTMessage":
                        return Tquery.RecordWriteResult.newBuilder().setRevision(1)
                            .setDurability(((Tquery.DeleteTMessageRequest) args[1]).getDurability()).build();
                    case "getTMessage":
                        return Tquery.GetTMessageResponse.getDefaultInstance();
                    case "apply": {
                        Tquery.RecordApplyRequest req = (Tquery.RecordApplyRequest) args[1];
                        Tquery.RecordApplyResponse.Builder b = Tquery.RecordApplyResponse.newBuilder();
                        for (int i = 0; i < req.getOpsCount(); i++) {
                            b.addResults(Tquery.RecordWriteResult.newBuilder().setRevision(i + 1).setDurability(req.getDurability()));
                        }
                        return b.build();
                    }
                    default:
                        throw new UnsupportedOperationException(method.getName());
                }
            });
    }

    /** Records one call's frames and its error. */
    private static final class Recorder<T> implements StreamObserver<T> {
        final List<T> values = new ArrayList<>();
        Throwable error;

        @Override public void onNext(T value) { values.add(value); }
        @Override public void onError(Throwable t) { error = t; }
        @Override public void onCompleted() {}
    }

    private interface Call<T> {
        void run(TenantRecordMap svc, StreamObserver<T> observer);
    }

    private static ImmutableKey key;
    private static JwkOuterClass.Jwk jwk;

    private static String bearer(String ns, String subject) throws Exception {
        TokenWrapper tok = TokenWrapper.create()
            .setIssuer("test-issuer")
            .setAudience(Authorizer.SERVICE_NAME)
            .setExpiration(System.currentTimeMillis() / 1000 + 3600)
            .setNamespace(ns)
            .setSubType("service")
            .setSubject(subject);
        tok.sign(key);
        return "Bearer " + tok.getBase64();
    }

    /**
     * The wrapper of tenant B's instance behind the real Authorizer, wired as
     * the generated ServiceManager wires it: the wrapper's method auth and the
     * instance's own namespace, which the door and the route check hold to.
     */
    private static TenantRecordMap server() {
        Authorizer a = new Authorizer(sink -> sink.update(Map.of(jwk.getKid(), jwk)));
        a.setHostNamespace("/tenants/" + B);
        TenantRecordMap.registerMethodAuth(a);
        return new TenantRecordMap(handler(), a);
    }

    /** Runs one call as tmessaging in namespace ns. */
    private static <T> Recorder<T> as(TenantRecordMap svc, String ns, String subject, Call<T> call) throws Exception {
        reached.clear();
        Recorder<T> rec = new Recorder<>();
        Context ctx = Auth.withVerifiedTokenSlot(Context.ROOT)
            .withValue(Authorizer.AUTH_HEADER_CTX_KEY, bearer(ns, subject));
        ctx.run(() -> call.run(svc, rec));
        return rec;
    }

    private static <T> Recorder<T> asSystem(TenantRecordMap svc, Call<T> call) throws Exception {
        return as(svc, SYSTEM, SYSTEM_SUBJECT, call);
    }

    private static void wantReached(String what, Recorder<?> rec, String rpc) {
        if (rec.error != null || rec.values.size() != 1) {
            fail(what + ": want one response, got " + (rec.error != null ? rec.error : rec.values));
        }
        if (!reached.equals(List.of(rpc))) {
            fail(what + ": want the store reached on " + rpc + ", got " + reached);
        }
    }

    /** Refused with code (and, when substr is non-null, a message containing it), and the store never reached. */
    private static void wantRefused(String what, Recorder<?> rec, Status.Code code, String substr) {
        if (rec.error == null) {
            fail(what + ": want " + code + ", got " + rec.values);
        } else {
            Status s = Status.fromThrowable(rec.error);
            String msg = s.getDescription() == null ? "" : s.getDescription();
            if ((code != null && s.getCode() != code) || (substr != null && !msg.contains(substr))) {
                fail(what + ": want " + (code == null ? "a refusal" : code) + (substr == null ? "" : " containing \"" + substr + "\"")
                    + ", got " + s.getCode() + ": " + msg);
            }
        }
        if (!reached.isEmpty()) {
            fail(what + ": a refused call must not reach the store, it reached " + reached);
        }
    }

    private static Tmessaging.TMessage row(String id, String ns) {
        return Tmessaging.TMessage.newBuilder().setMessageId(id).setNamespace(ns).setOwner("o").setBody("b").build();
    }

    private static Tquery.PutTMessageRequest put(String route, String id, String ns) {
        return Tquery.PutTMessageRequest.newBuilder().setItem(row(id, ns)).setTargetTenant(route).build();
    }

    private static Tquery.DeleteTMessageRequest del(String route, String id, String ns) {
        return Tquery.DeleteTMessageRequest.newBuilder().setMessageId(id).setNamespace(ns).setTargetTenant(route).build();
    }

    private static Tquery.GetTMessageRequest get(String route, String id, String ns) {
        return Tquery.GetTMessageRequest.newBuilder().setMessageId(id).setNamespace(ns).setTargetTenant(route).build();
    }

    private static Tquery.RecordOp putOp(String id, String ns) {
        return Tquery.RecordOp.newBuilder().setPutTMessage(
            Tquery.PutTMessageRequest.newBuilder().setItem(row(id, ns))).build();
    }

    private static Tquery.RecordApplyRequest apply(String route, Tquery.RecordOp... ops) {
        return Tquery.RecordApplyRequest.newBuilder().addAllOps(List.of(ops)).setTargetTenant(route).build();
    }

    private static Recorder<Tquery.RecordWriteResult> callPut(TenantRecordMap svc, Tquery.PutTMessageRequest req) throws Exception {
        return TenantRecordRouteTest.<Tquery.RecordWriteResult>asSystem(svc, (s, o) -> s.putTMessage(req, o));
    }

    private static Recorder<Tquery.RecordApplyResponse> callApply(TenantRecordMap svc, Tquery.RecordApplyRequest req) throws Exception {
        return TenantRecordRouteTest.<Tquery.RecordApplyResponse>asSystem(svc, (s, o) -> s.apply(req, o));
    }

    private static final String ROUTE_PIN = "the tenant this call is routed to; a tenant's instance holds that tenant's entries only";

    public static void main(String[] args) throws Exception {
        key = ImmutableKey.from(KeyHelpers.newEd25519Key());
        byte[] spki = key.getPublicKey().getEncoded();
        byte[] raw = new byte[32];
        System.arraycopy(spki, spki.length - 32, raw, 0, 32);
        jwk = JwkOuterClass.Jwk.newBuilder()
            .setKid(key.getProto().getId())
            .setKty(KeyOuterClass.KeyType.KEY_TYPE_OKP)
            .setCrv("Ed25519")
            .setX(Base64.getUrlEncoder().withoutPadding().encodeToString(raw))
            .build();

        TenantRecordMap b = server();

        // 1. Misrouted: A's call, forced onto B's instance (the route check).
        wantRefused("Put routed to A on B's instance", callPut(b, put(A, "m1", "/tenants/" + A)),
            Status.Code.PERMISSION_DENIED, "routed to tenant " + A + ", this instance serves tenant " + B);

        // 2. A's row under B's route (the route pin), a /system peer included.
        wantRefused("Put of A's row routed to B", callPut(b, put(B, "m1", "/tenants/" + A)),
            Status.Code.PERMISSION_DENIED, "PutTMessage: item.namespace is not in tenant " + B + ", " + ROUTE_PIN);
        // A namespace that only shares B's prefix is not B's.
        wantRefused("Put of a row in /tenants/" + B + "x routed to B", callPut(b, put(B, "m1", "/tenants/" + B + "x")),
            Status.Code.PERMISSION_DENIED, ROUTE_PIN);

        // 3. No route.
        wantRefused("Put with no target_tenant", callPut(b, put("", "m1", "/tenants/" + B)),
            Status.Code.INVALID_ARGUMENT, null);

        // 4. Apply: the route pin per op against the Apply's own route; and the route check.
        wantRefused("Apply routed to B with an op of A's", callApply(b, apply(B, putOp("m1", "/tenants/" + A))),
            Status.Code.PERMISSION_DENIED, "Apply (put_t_message): item.namespace is not in tenant " + B + ", " + ROUTE_PIN);
        wantRefused("Apply routed to B, its second op of A's", callApply(b, apply(B, putOp("m1", "/tenants/" + B), putOp("m2", "/tenants/" + A))),
            Status.Code.PERMISSION_DENIED, ROUTE_PIN);
        wantRefused("Apply routed to A on B's instance", callApply(b, apply(A, putOp("m1", "/tenants/" + A))),
            Status.Code.PERMISSION_DENIED, "routed to tenant " + A);

        // 5. Key-addressed: Delete and Get of A's entry routed to B.
        wantRefused("Delete of A's entry routed to B", TenantRecordRouteTest.<Tquery.RecordWriteResult>asSystem(b,
            (s, o) -> s.deleteTMessage(del(B, "m1", "/tenants/" + A), o)),
            Status.Code.PERMISSION_DENIED, "DeleteTMessage: namespace is not in tenant " + B + ", " + ROUTE_PIN);
        wantRefused("Get of A's entry routed to B", TenantRecordRouteTest.<Tquery.GetTMessageResponse>asSystem(b,
            (s, o) -> s.getTMessage(get(B, "m1", "/tenants/" + A), o)),
            Status.Code.PERMISSION_DENIED, "GetTMessage: namespace is not in tenant " + B + ", " + ROUTE_PIN);
        // The streaming Watch carries the route check too.
        wantRefused("WatchTMessageChanges routed to A on B's instance", TenantRecordRouteTest.<Tquery.TMessageWatchEvent>asSystem(b,
            (s, o) -> s.watchTMessageChanges(Tquery.WatchTMessageChangesRequest.newBuilder()
                .setNamespace("/tenants/" + A).setTargetTenant(A).build(), o)),
            Status.Code.PERMISSION_DENIED, "routed to tenant " + A);
        wantRefused("WatchTMessageChanges of A's entries routed to B", TenantRecordRouteTest.<Tquery.TMessageWatchEvent>asSystem(b,
            (s, o) -> s.watchTMessageChanges(Tquery.WatchTMessageChangesRequest.newBuilder()
                .setNamespace("/tenants/" + A).setTargetTenant(B).build(), o)),
            Status.Code.PERMISSION_DENIED, ROUTE_PIN);

        // 6. A caller of tenant A at B's instance: refused at the door or the route.
        String aNs = "/tenants/" + A;
        String aSubject = "spiffe://example.com" + aNs + "/services/tmessaging";
        wantRefused("tenant A's caller, routed to B", TenantRecordRouteTest.<Tquery.RecordWriteResult>as(b, aNs, aSubject,
            (s, o) -> s.putTMessage(put(B, "m1", aNs), o)), null, null);
        wantRefused("tenant A's caller, routed to A", TenantRecordRouteTest.<Tquery.RecordWriteResult>as(b, aNs, aSubject,
            (s, o) -> s.putTMessage(put(A, "m1", aNs), o)), null, null);
        wantRefused("tenant A's caller, B's row routed to B", TenantRecordRouteTest.<Tquery.RecordWriteResult>as(b, aNs, aSubject,
            (s, o) -> s.putTMessage(put(B, "m1", "/tenants/" + B), o)), null, null);

        // 7. Routed right, an entry in the tenant: the store is reached.
        wantReached("Put of B's row routed to B", callPut(b, put(B, "m1", "/tenants/" + B)), "putTMessage");
        wantReached("Put of a row below B routed to B", callPut(b, put(B, "m1", "/tenants/" + B + "/users/u1")), "putTMessage");
        wantReached("Apply routed to B with B's ops", callApply(b, apply(B, putOp("m1", "/tenants/" + B), putOp("m2", "/tenants/" + B + "/users/u1"))), "apply");
        wantReached("Delete of B's entry routed to B", TenantRecordRouteTest.<Tquery.RecordWriteResult>asSystem(b,
            (s, o) -> s.deleteTMessage(del(B, "m1", "/tenants/" + B), o)), "deleteTMessage");
        wantReached("Get of B's entry routed to B", TenantRecordRouteTest.<Tquery.GetTMessageResponse>asSystem(b,
            (s, o) -> s.getTMessage(get(B, "m1", "/tenants/" + B), o)), "getTMessage");

        if (failures > 0) {
            System.err.println(failures + " tenant record route check(s) failed");
            System.exit(1);
        }
        System.out.println("TenantRecordRouteTest: all checks passed");
    }
}
