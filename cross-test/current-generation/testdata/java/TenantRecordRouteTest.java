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
// tenant beta (tid bbbbbbbb): the method auth the wrapper registers, the host
// identity read from the instance's own token (the door, the route check and
// the route pin's namespace), over real signed tokens.
//
// What it pins: a call routed to another tenant than the instance serves is
// refused (the route check); an entry outside the tenant the call is routed
// to is refused, whoever the caller is, a /system peer included (the route
// pin), on Put, Delete, Get, Watch and per op of Apply; a call with no route
// is refused; another tenant's caller does not get in; and a call that is
// routed right, for an entry in that tenant, reaches the store.
//
// Identities are kernel#23's: a tenant-owned service's subject is
// spiffe://<td>/tenants/<tid>/services/<svc> while its namespace claim stays
// /tenants/<name>. Tenant beta is tid bbbbbbbb, tenant alpha is tid aaaaaaaa.
// The instance serves the tid of its own subject; a caller is in the tenant of
// its namespace claim, so its entries are /tenants/beta's, never
// /tenants/bbbbbbbb's.

package com.gitlab.example.dummy.tstratum.model.chassis.grpcserver;

import com.gitlab.example.dummy.tstratum.model.chassis.key.ImmutableKey;
import com.gitlab.example.dummy.tstratum.model.chassis.key.KeyHelpers;
import com.gitlab.example.dummy.tstratum.model.chassis.servicemanager.Auth;
import com.gitlab.example.dummy.tstratum.model.chassis.token.TokenWrapper;
import com.gitlab.example.dummy.tstratum.model.s2sm.internal.grpc.servers.vservices.tquery.TenantRecordMap;
import com.gitlab.example.dummy.tstratum.model.types.services.tmessaging.Tmessaging;
import com.gitlab.example.dummy.tstratum.model.types.sys.errors.ProtoException;
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
    private static final String A_NS = "/tenants/alpha";
    private static final String B_NS = "/tenants/beta";
    /** The instance's own subject: beta's tid, never its name. */
    private static final String B_HOST_SUBJECT = "spiffe://example.com/tenants/" + B + "/services/tquery";
    private static final String SYSTEM = "/system";
    private static final String SYSTEM_SUBJECT = "spiffe://example.com/system/services/tmessaging";

    /** The rpcs of the store the fake handler was reached on; empty: nothing stored, nothing in redo. */
    private static final List<String> reached = new ArrayList<>();
    /** The entry namespace of the last Put that reached the store. */
    private static String lastNs = "";

    /** The store: records which rpc reached it and answers a durable write; any other method throws. */
    private static TenantRecordMap.TenantRecordMapHandler handler() {
        return (TenantRecordMap.TenantRecordMapHandler) Proxy.newProxyInstance(
            TenantRecordRouteTest.class.getClassLoader(),
            new Class<?>[] {TenantRecordMap.TenantRecordMapHandler.class},
            (proxy, method, args) -> {
                reached.add(method.getName());
                switch (method.getName()) {
                    case "putTMessage":
                        lastNs = ((Tquery.PutTMessageRequest) args[1]).getItem().getNamespace();
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
        return "Bearer " + token(ns, subject);
    }

    private static String token(String ns, String subject) throws Exception {
        TokenWrapper tok = TokenWrapper.create()
            .setIssuer("test-issuer")
            .setAudience(Authorizer.SERVICE_NAME)
            .setExpiration(System.currentTimeMillis() / 1000 + 3600)
            .setNamespace(ns)
            .setSubType("service")
            .setSubject(subject);
        tok.sign(key);
        return tok.getBase64();
    }

    /**
     * The wrapper of tenant beta's instance behind the real Authorizer, wired
     * as the generated ServiceManager wires it: the wrapper's method auth and
     * the instance's own identity, read from its own token (namespace
     * /tenants/beta, the door; subject /tenants/bbbbbbbb/services/tquery, the
     * tid the route check admits).
     */
    private static TenantRecordMap server() throws Exception {
        Authorizer a = new Authorizer(sink -> sink.update(Map.of(jwk.getKid(), jwk)));
        a.setHostNamespaceFromToken(token(B_NS, B_HOST_SUBJECT));
        TenantRecordMap.registerMethodAuth(a);
        return new TenantRecordMap(handler(), a);
    }

    /**
     * Beta's instance with its door held open: the caller is admitted by an
     * Authorizer with no host identity (same keys, same method auth), while the
     * route check and the route pin's namespace stay beta's. What refuses a call
     * on it is the routing, never the caller's grant: the door is pinned above.
     */
    private static TenantRecordMap serverDoorOpen() throws Exception {
        Authorizer open = new Authorizer(sink -> sink.update(Map.of(jwk.getKid(), jwk)));
        TenantRecordMap.registerMethodAuth(open);
        Authorizer a = new Authorizer(sink -> sink.update(Map.of(jwk.getKid(), jwk))) {
            @Override
            public TokenWrapper authorizeWithToken(String authHeader, String fullMethod, Object request)
                    throws ProtoException {
                return open.authorizeWithToken(authHeader, fullMethod, request);
            }
        };
        a.setHostNamespaceFromToken(token(B_NS, B_HOST_SUBJECT));
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
    /** The route pin's refusal names the routed tid and the namespace its entries live in. */
    private static final String NOT_IN_B = "is not in tenant " + B + " (" + B_NS + "), " + ROUTE_PIN;

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
        wantRefused("Put routed to A on B's instance", callPut(b, put(A, "m1", A_NS)),
            Status.Code.PERMISSION_DENIED, "routed to tenant " + A + ", this instance serves tenant " + B);

        // 2. A's row under B's route (the route pin), a /system peer included.
        wantRefused("Put of A's row routed to B", callPut(b, put(B, "m1", A_NS)),
            Status.Code.PERMISSION_DENIED, "PutTMessage: item.namespace " + NOT_IN_B);
        // A namespace that only shares B's prefix is not B's.
        wantRefused("Put of a row in " + B_NS + "x routed to B", callPut(b, put(B, "m1", B_NS + "x")),
            Status.Code.PERMISSION_DENIED, ROUTE_PIN);

        // 3. No route.
        wantRefused("Put with no target_tenant", callPut(b, put("", "m1", B_NS)),
            Status.Code.INVALID_ARGUMENT, null);

        // 4. Apply: the route pin per op against the Apply's own route; and the route check.
        wantRefused("Apply routed to B with an op of A's", callApply(b, apply(B, putOp("m1", A_NS))),
            Status.Code.PERMISSION_DENIED, "Apply (put_t_message): item.namespace " + NOT_IN_B);
        wantRefused("Apply routed to B, its second op of A's", callApply(b, apply(B, putOp("m1", B_NS), putOp("m2", A_NS))),
            Status.Code.PERMISSION_DENIED, ROUTE_PIN);
        wantRefused("Apply routed to A on B's instance", callApply(b, apply(A, putOp("m1", A_NS))),
            Status.Code.PERMISSION_DENIED, "routed to tenant " + A);

        // 5. Key-addressed: Delete and Get of A's entry routed to B.
        wantRefused("Delete of A's entry routed to B", TenantRecordRouteTest.<Tquery.RecordWriteResult>asSystem(b,
            (s, o) -> s.deleteTMessage(del(B, "m1", A_NS), o)),
            Status.Code.PERMISSION_DENIED, "DeleteTMessage: namespace " + NOT_IN_B);
        wantRefused("Get of A's entry routed to B", TenantRecordRouteTest.<Tquery.GetTMessageResponse>asSystem(b,
            (s, o) -> s.getTMessage(get(B, "m1", A_NS), o)),
            Status.Code.PERMISSION_DENIED, "GetTMessage: namespace " + NOT_IN_B);
        // The streaming Watch carries the route check too.
        wantRefused("WatchTMessageChanges routed to A on B's instance", TenantRecordRouteTest.<Tquery.TMessageWatchEvent>asSystem(b,
            (s, o) -> s.watchTMessageChanges(Tquery.WatchTMessageChangesRequest.newBuilder()
                .setNamespace(A_NS).setTargetTenant(A).build(), o)),
            Status.Code.PERMISSION_DENIED, "routed to tenant " + A);
        wantRefused("WatchTMessageChanges of A's entries routed to B", TenantRecordRouteTest.<Tquery.TMessageWatchEvent>asSystem(b,
            (s, o) -> s.watchTMessageChanges(Tquery.WatchTMessageChangesRequest.newBuilder()
                .setNamespace(A_NS).setTargetTenant(B).build(), o)),
            Status.Code.PERMISSION_DENIED, ROUTE_PIN);

        // 6. A caller of tenant A at B's instance: refused at the door or the route.
        String aSubject = "spiffe://example.com/tenants/" + A + "/services/tmessaging";
        wantRefused("tenant A's caller, routed to B", TenantRecordRouteTest.<Tquery.RecordWriteResult>as(b, A_NS, aSubject,
            (s, o) -> s.putTMessage(put(B, "m1", A_NS), o)), null, null);
        wantRefused("tenant A's caller, routed to A", TenantRecordRouteTest.<Tquery.RecordWriteResult>as(b, A_NS, aSubject,
            (s, o) -> s.putTMessage(put(A, "m1", A_NS), o)), null, null);
        wantRefused("tenant A's caller, B's row routed to B", TenantRecordRouteTest.<Tquery.RecordWriteResult>as(b, A_NS, aSubject,
            (s, o) -> s.putTMessage(put(B, "m1", B_NS), o)), null, null);

        // 7. Routed right, an entry in the tenant: the store is reached.
        wantReached("Put of B's row routed to B", callPut(b, put(B, "m1", B_NS)), "putTMessage");
        wantReached("Put of a row below B routed to B", callPut(b, put(B, "m1", B_NS + "/users/u1")), "putTMessage");
        wantReached("Apply routed to B with B's ops", callApply(b, apply(B, putOp("m1", B_NS), putOp("m2", B_NS + "/users/u1"))), "apply");
        wantReached("Delete of B's entry routed to B", TenantRecordRouteTest.<Tquery.RecordWriteResult>asSystem(b,
            (s, o) -> s.deleteTMessage(del(B, "m1", B_NS), o)), "deleteTMessage");
        wantReached("Get of B's entry routed to B", TenantRecordRouteTest.<Tquery.GetTMessageResponse>asSystem(b,
            (s, o) -> s.getTMessage(get(B, "m1", B_NS), o)), "getTMessage");

        // 8. The tid is not the namespace: an entry spelled /tenants/bbbbbbbb is
        // not tenant beta's, though the call is routed to bbbbbbbb.
        wantRefused("Put of a row in /tenants/" + B + " routed to B", callPut(b, put(B, "m3", "/tenants/" + B)),
            Status.Code.PERMISSION_DENIED, "item.namespace " + NOT_IN_B);

        // 9. A tenant-owned caller (subject /tenants/<tid>/services/<svc>,
        // namespace /tenants/<name>) is in the tenant of its namespace claim:
        // its entry in /tenants/beta is served by beta's instance, and one it
        // spells /tenants/bbbbbbbb, its subject's tid, is not its tenant's. A
        // caller whose subject names beta's tid under another tenant's
        // namespace is held to that namespace, and refused: at beta's door,
        // and, past it, by the route pin (its own entry) or its tenant pin
        // (beta's entry).
        String bCallerSubject = "spiffe://example.com/tenants/" + B + "/services/tmessaging";
        wantReached("beta's caller with a tid subject, its entry in " + B_NS, TenantRecordRouteTest.<Tquery.RecordWriteResult>as(b, B_NS, bCallerSubject,
            (s, o) -> s.putTMessage(put(B, "m1", B_NS), o)), "putTMessage");
        if (!B_NS.equals(lastNs)) {
            fail("beta's caller's entry reached the store in \"" + lastNs + "\"; want " + B_NS);
        }
        wantRefused("beta's caller naming its subject's tid as the entry's tenant", TenantRecordRouteTest.<Tquery.RecordWriteResult>as(b, B_NS, bCallerSubject,
            (s, o) -> s.putTMessage(put(B, "m2", "/tenants/" + B), o)), Status.Code.INVALID_ARGUMENT, "names a tenant the caller neither owns nor is linked to");
        wantRefused("alpha's caller with beta's tid subject, at beta's door", TenantRecordRouteTest.<Tquery.RecordWriteResult>as(b, A_NS, bCallerSubject,
            (s, o) -> s.putTMessage(put(B, "m1", A_NS), o)), Status.Code.PERMISSION_DENIED, null);
        TenantRecordMap doorOpen = serverDoorOpen();
        wantRefused("alpha's caller with beta's tid subject, its own entry past the door", TenantRecordRouteTest.<Tquery.RecordWriteResult>as(doorOpen, A_NS, bCallerSubject,
            (s, o) -> s.putTMessage(put(B, "m1", A_NS), o)), Status.Code.PERMISSION_DENIED, "item.namespace " + NOT_IN_B);
        wantRefused("alpha's caller with beta's tid subject, beta's entry past the door", TenantRecordRouteTest.<Tquery.RecordWriteResult>as(doorOpen, A_NS, bCallerSubject,
            (s, o) -> s.putTMessage(put(B, "m1", B_NS), o)), Status.Code.INVALID_ARGUMENT, "names a tenant the caller neither owns nor is linked to");

        if (failures > 0) {
            System.err.println(failures + " tenant record route check(s) failed");
            System.exit(1);
        }
        System.out.println("TenantRecordRouteTest: all checks passed");
    }
}
