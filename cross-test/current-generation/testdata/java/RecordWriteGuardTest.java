// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the record-egress HOST fixture (#163). NOT generated:
// run.sh copies this file into the generated chassis/grpcserver package of the
// egressstratum build and runs it as a main-method self-test, like
// TenantPinnedReadTest. It lives in that package for Authorizer.SERVICE_NAME,
// the audience a token must carry.
//
// It drives the REAL generated EgressRecordMap wrapper in-process: a fake
// handler behind it, the real Authorizer (with the method auth the wrapper
// registers, over real signed tokens) in front of it, and the record write
// surface's guards between. Twin of the Go record_write_guard_test.go.
//
// What it pins, through the wrapper's own methods:
//   - durability: a write below VaultRow's REMOTE floor is refused, a write
//     naming none reaches the handler at REMOTE, and a write the store acks
//     below that is not answered;
//   - tenant: a /tenants caller is pinned to its own tenant (stamped when
//     unset, refused when foreign), a /system peer is not, and a token that
//     still carries the retired `linked` claim (#171) is not widened by it;
//   - Apply: ops out of canonical lock order or naming one entry twice are
//     refused, each op is authorized as its single-type rpc, the handler sees
//     the highest floor, and a short answer is not returned.

package com.gitlab.example.dummy.egressstratum.model.chassis.grpcserver;

import com.gitlab.example.dummy.egressstratum.model.chassis.key.ImmutableKey;
import com.gitlab.example.dummy.egressstratum.model.chassis.key.KeyHelpers;
import com.gitlab.example.dummy.egressstratum.model.chassis.servicemanager.Auth;
import com.gitlab.example.dummy.egressstratum.model.chassis.token.TokenWrapper;
import com.gitlab.example.dummy.egressstratum.model.s2sm.internal.grpc.servers.vservices.egressquery.EgressRecordMap;
import com.gitlab.example.dummy.egressstratum.model.types.services.egressvault.Egressvault;
import com.gitlab.example.dummy.egressstratum.model.types.sys.errors.ProtoException;
import com.gitlab.example.dummy.egressstratum.model.types.sys.jwk.JwkOuterClass;
import com.gitlab.example.dummy.egressstratum.model.types.sys.key.KeyOuterClass;
import com.gitlab.example.dummy.egressstratum.model.types.vservices.egressquery.Egressquery;

import io.grpc.Context;
import io.grpc.Status;
import io.grpc.stub.StreamObserver;

import java.lang.reflect.Proxy;
import java.util.ArrayList;
import java.util.Base64;
import java.util.List;
import java.util.Map;

public class RecordWriteGuardTest {
    private static int failures = 0;

    private static void fail(String message) {
        System.err.println("FAIL: " + message);
        failures++;
    }

    private static final Egressquery.RecordDurability MEMORY = Egressquery.RecordDurability.RECORD_DURABILITY_MEMORY;
    private static final Egressquery.RecordDurability LOCAL = Egressquery.RecordDurability.RECORD_DURABILITY_LOCAL;
    private static final Egressquery.RecordDurability REMOTE = Egressquery.RecordDurability.RECORD_DURABILITY_REMOTE;

    // What the fake handler saw, and how it answers.
    private static Egressquery.PutVaultRowRequest putSeen;
    private static Egressquery.DeleteVaultRowRequest delSeen;
    private static Egressquery.RecordApplyRequest applySeen;
    private static Egressquery.RecordDurability ack = REMOTE;
    private static int results = -1; // < 0: one per op

    /** A handler implementing only the write rpcs the test drives; any other method throws. */
    private static EgressRecordMap.EgressRecordMapHandler handler() {
        return (EgressRecordMap.EgressRecordMapHandler) Proxy.newProxyInstance(
            RecordWriteGuardTest.class.getClassLoader(),
            new Class<?>[] {EgressRecordMap.EgressRecordMapHandler.class},
            (proxy, method, args) -> {
                switch (method.getName()) {
                    case "putVaultRow":
                        putSeen = (Egressquery.PutVaultRowRequest) args[1];
                        return Egressquery.RecordWriteResult.newBuilder().setRevision(1).setDurability(ack).build();
                    case "deleteVaultRow":
                        delSeen = (Egressquery.DeleteVaultRowRequest) args[1];
                        return Egressquery.RecordWriteResult.newBuilder().setRevision(2).setDurability(ack).build();
                    case "apply": {
                        applySeen = (Egressquery.RecordApplyRequest) args[1];
                        int n = results < 0 ? applySeen.getOpsCount() : results;
                        Egressquery.RecordApplyResponse.Builder b = Egressquery.RecordApplyResponse.newBuilder();
                        for (int i = 0; i < n; i++) {
                            b.addResults(Egressquery.RecordWriteResult.newBuilder().setRevision(i + 1).setDurability(ack));
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
        void run(EgressRecordMap svc, StreamObserver<T> observer);
    }

    private static ImmutableKey key;
    private static JwkOuterClass.Jwk jwk;
    /** The retired `linked` claim (#171) the next bearer carries; empty for none. */
    private static List<String> linkedClaim = List.of();

    private static String bearer(String ns) throws Exception {
        TokenWrapper tok = TokenWrapper.create()
            .setIssuer("test-issuer")
            .setAudience(Authorizer.SERVICE_NAME)
            .setExpiration(System.currentTimeMillis() / 1000 + 3600)
            .setNamespace(ns)
            .setSubType("service")
            .setSubject("spiffe://example.com" + ns + "/services/egressvault");
        if (!linkedClaim.isEmpty()) {
            tok.setListField("linked", linkedClaim);
        }
        tok.sign(key);
        return "Bearer " + tok.getBase64();
    }

    /**
     * The wrapper behind the real Authorizer, which additionally refuses every
     * full method ending in "/" + one of deny — the way a grant the caller does
     * not hold would.
     */
    private static EgressRecordMap server(String... deny) {
        Authorizer a = new Authorizer(sink -> sink.update(Map.of(jwk.getKid(), jwk))) {
            @Override
            public TokenWrapper authorize(String fullMethod, Object request) throws ProtoException {
                for (String d : deny) {
                    if (fullMethod.endsWith("/" + d)) {
                        throw new ProtoException(ProtoException.PERMISSION_DENIED, "denied: " + d);
                    }
                }
                return super.authorize(fullMethod, request);
            }
        };
        EgressRecordMap.registerMethodAuth(a);
        return new EgressRecordMap(handler(), a);
    }

    /** Runs one call as egressvault in namespace ns. */
    private static <T> Recorder<T> as(EgressRecordMap svc, String ns, Call<T> call) throws Exception {
        Recorder<T> rec = new Recorder<>();
        Context ctx = Auth.withVerifiedTokenSlot(Context.ROOT)
            .withValue(Authorizer.AUTH_HEADER_CTX_KEY, bearer(ns));
        ctx.run(() -> call.run(svc, rec));
        return rec;
    }

    private static void wantOk(String what, Recorder<?> rec) {
        if (rec.error != null || rec.values.size() != 1) {
            fail(what + ": want one response, got " + (rec.error != null ? rec.error : rec.values));
        }
    }

    private static void wantCode(String what, Recorder<?> rec, Status.Code code, String substr) {
        if (rec.error == null) {
            fail(what + ": want " + code + " containing \"" + substr + "\", got " + rec.values);
            return;
        }
        Status s = Status.fromThrowable(rec.error);
        String msg = s.getDescription() == null ? "" : s.getDescription();
        if (s.getCode() != code || !msg.contains(substr)) {
            fail(what + ": want " + code + " containing \"" + substr + "\", got " + s.getCode() + ": " + msg);
        }
    }

    private static Egressvault.VaultRow vaultRow(String id, String ns) {
        return Egressvault.VaultRow.newBuilder().setRowId(id).setNamespace(ns).setOwner("o").build();
    }

    private static Egressquery.PutVaultRowRequest put(String id, String ns) {
        return Egressquery.PutVaultRowRequest.newBuilder().setItem(vaultRow(id, ns)).build();
    }

    private static Egressquery.RecordOp putOp(String id, String ns) {
        return Egressquery.RecordOp.newBuilder().setPutVaultRow(put(id, ns)).build();
    }

    private static Egressquery.RecordOp auditOp(long seq) {
        return Egressquery.RecordOp.newBuilder().setInsertIfAbsentVaultAuditRow(
            Egressquery.InsertIfAbsentVaultAuditRowRequest.newBuilder().setItem(
                Egressvault.VaultAuditRow.newBuilder().setSeq(seq).setNamespace("/tenants/acme").setOwner("o"))).build();
    }

    private static Egressquery.RecordApplyRequest apply(Egressquery.RecordOp... ops) {
        return Egressquery.RecordApplyRequest.newBuilder().addAllOps(List.of(ops)).build();
    }

    private static Recorder<Egressquery.RecordWriteResult> callPut(EgressRecordMap svc, String ns, Egressquery.PutVaultRowRequest req) throws Exception {
        return RecordWriteGuardTest.<Egressquery.RecordWriteResult>as(svc, ns, (s, o) -> s.putVaultRow(req, o));
    }

    private static Recorder<Egressquery.RecordApplyResponse> callApply(EgressRecordMap svc, String ns, Egressquery.RecordApplyRequest req) throws Exception {
        return RecordWriteGuardTest.<Egressquery.RecordApplyResponse>as(svc, ns, (s, o) -> s.apply(req, o));
    }

    private static void durabilityFloor() throws Exception {
        EgressRecordMap p = server();
        ack = REMOTE;
        putSeen = null;
        wantCode("Put at MEMORY", callPut(p, "/system", put("r1", "/tenants/acme").toBuilder().setDurability(MEMORY).build()),
            Status.Code.INVALID_ARGUMENT, "below the record type's declared floor RECORD_DURABILITY_REMOTE");
        if (putSeen != null) {
            fail("a write below the floor must not reach the handler");
        }

        wantOk("Put naming no durability", callPut(p, "/system", put("r1", "/tenants/acme")));
        if (putSeen == null || putSeen.getDurability() != REMOTE) {
            fail("the handler must receive the floor REMOTE, got " + (putSeen == null ? "no call" : putSeen.getDurability()));
        }

        ack = LOCAL;
        wantCode("Put acked LOCAL", callPut(p, "/system", put("r1", "/tenants/acme")),
            Status.Code.INTERNAL, "acked the write below the durability the request required");
        ack = REMOTE;
    }

    private static void tenantPin() throws Exception {
        EgressRecordMap p = server();
        ack = REMOTE;

        // A key-addressed write names its tenant beside the key; unset is
        // stamped. (A row's own tenant field is required by VaultRow, so
        // validation refuses an unset one before any pin runs.)
        delSeen = null;
        wantOk("Delete with unset tenant", RecordWriteGuardTest.<Egressquery.RecordWriteResult>as(p, "/tenants/acme",
            (s, o) -> s.deleteVaultRow(Egressquery.DeleteVaultRowRequest.newBuilder().setRowId("r1").build(), o)));
        if (delSeen == null || !"/tenants/acme".equals(delSeen.getNamespace())) {
            fail("stamped tenant = " + (delSeen == null ? "no call" : "\"" + delSeen.getNamespace() + "\"") + ", want /tenants/acme");
        }
        wantCode("Delete of a foreign tenant", RecordWriteGuardTest.<Egressquery.RecordWriteResult>as(p, "/tenants/acme",
            (s, o) -> s.deleteVaultRow(Egressquery.DeleteVaultRowRequest.newBuilder().setRowId("r1").setNamespace("/tenants/beta").build(), o)),
            Status.Code.INVALID_ARGUMENT, "DeleteVaultRow: namespace names a tenant other than the caller's");

        wantOk("Put of the caller's own tenant", callPut(p, "/tenants/acme", put("r1", "/tenants/acme")));
        wantCode("Put of a foreign tenant", callPut(p, "/tenants/acme", put("r1", "/tenants/beta")),
            Status.Code.INVALID_ARGUMENT, "names a tenant other than the caller's");

        putSeen = null;
        wantOk("Put from /system", callPut(p, "/system", put("r1", "/tenants/beta")));
        if (putSeen == null || !"/tenants/beta".equals(putSeen.getItem().getNamespace())) {
            fail("a /system peer's tenant must pass untouched, got " + (putSeen == null ? "no call" : putSeen.getItem().getNamespace()));
        }
    }

    // The `linked` claim is retired (#171): a tenant caller whose signed token
    // still names /tenants/beta in one is held to its own tenant exactly as one
    // without.
    private static void linkedClaimWidensNothing() throws Exception {
        EgressRecordMap p = server();
        ack = REMOTE;
        results = -1;
        linkedClaim = List.of("/tenants/beta");
        try {
            wantOk("Put of the caller's own tenant, linked claim in play", callPut(p, "/tenants/acme", put("r1", "/tenants/acme")));
            putSeen = null;
            wantCode("Put of a tenant only the linked claim names", callPut(p, "/tenants/acme", put("r1", "/tenants/beta")),
                Status.Code.INVALID_ARGUMENT, "names a tenant other than the caller's");
            if (putSeen != null) {
                fail("a write to a tenant only the linked claim names must not reach the handler");
            }
            wantCode("Delete of a tenant only the linked claim names", RecordWriteGuardTest.<Egressquery.RecordWriteResult>as(p, "/tenants/acme",
                (s, o) -> s.deleteVaultRow(Egressquery.DeleteVaultRowRequest.newBuilder().setRowId("r1").setNamespace("/tenants/beta").build(), o)),
                Status.Code.INVALID_ARGUMENT, "DeleteVaultRow: namespace names a tenant other than the caller's");
            delSeen = null;
            wantOk("Delete with unset tenant, linked claim in play", RecordWriteGuardTest.<Egressquery.RecordWriteResult>as(p, "/tenants/acme",
                (s, o) -> s.deleteVaultRow(Egressquery.DeleteVaultRowRequest.newBuilder().setRowId("r1").build(), o)));
            if (delSeen == null || !"/tenants/acme".equals(delSeen.getNamespace())) {
                fail("stamped tenant = " + (delSeen == null ? "no call" : "\"" + delSeen.getNamespace() + "\"") + ", want the caller's own /tenants/acme");
            }
            wantCode("Apply with an op only the linked claim names", callApply(p, "/tenants/acme", apply(putOp("a", "/tenants/beta"))),
                Status.Code.INVALID_ARGUMENT, "Apply (put_vault_row): item.namespace names a tenant other than the caller's");
        } finally {
            linkedClaim = List.of();
        }
    }

    private static void applyGuard() throws Exception {
        EgressRecordMap p = server();
        ack = REMOTE;
        results = -1;

        // egressvault.VaultAuditRow sorts before egressvault.VaultRow.
        wantCode("Apply out of order", callApply(p, "/system", apply(putOp("a", "/tenants/acme"), auditOp(1))),
            Status.Code.INVALID_ARGUMENT, "out of canonical lock order");
        wantCode("Apply naming one entry twice", callApply(p, "/system", apply(putOp("a", "/tenants/acme"), putOp("a", "/tenants/acme"))),
            Status.Code.INVALID_ARGUMENT, "ops[1] is out of canonical lock order");
        // Integer keys order numerically: 9 before 10.
        wantOk("Apply with integer keys 9, 10", callApply(p, "/system", apply(auditOp(9), auditOp(10))));
        // String keys order by their UTF-8 bytes, as in every language: U+FFFF
        // (EF BF BF) before U+1F600 (F0 9F 98 80), though its UTF-16 code unit
        // (FFFF) sorts after the surrogate D83D.
        wantOk("Apply with UTF-8-ordered keys", callApply(p, "/system", apply(putOp("￿", "/tenants/acme"), putOp("😀", "/tenants/acme"))));
        wantCode("Apply with UTF-16-ordered keys", callApply(p, "/system", apply(putOp("😀", "/tenants/acme"), putOp("￿", "/tenants/acme"))),
            Status.Code.INVALID_ARGUMENT, "ops[1] is out of canonical lock order");
        wantCode("Apply with an empty op", callApply(p, "/system", apply(Egressquery.RecordOp.getDefaultInstance())),
            Status.Code.INVALID_ARGUMENT, "ops[0] names no operation");

        applySeen = null;
        wantOk("Apply in canonical order", callApply(p, "/system", apply(auditOp(1), putOp("a", "/tenants/acme"), putOp("b", "/tenants/acme"))));
        if (applySeen == null || applySeen.getDurability() != REMOTE) {
            fail("an Apply is acked at the highest floor its ops involve (REMOTE), handler saw "
                + (applySeen == null ? "no call" : applySeen.getDurability()));
        }
        wantCode("Apply asking MEMORY", callApply(p, "/system", apply(auditOp(1)).toBuilder().setDurability(MEMORY).build()),
            Status.Code.INVALID_ARGUMENT, "the request asks for a durability below egressvault.VaultAuditRow's declared floor");

        results = 1;
        wantCode("Apply answered short", callApply(p, "/system", apply(putOp("a", "/tenants/acme"), putOp("b", "/tenants/acme"))),
            Status.Code.INTERNAL, "answered 1 results for 2 ops");
        results = -1;
        ack = LOCAL;
        wantCode("Apply acked LOCAL", callApply(p, "/system", apply(putOp("a", "/tenants/acme"))),
            Status.Code.INTERNAL, "acked ops[0] below the durability the request required");
        ack = REMOTE;

        // Each op is authorized as its single-type rpc: Apply itself is
        // allowed, PutVaultRow is not, so an Apply carrying a Put is refused
        // with the authorizer's own answer and never reaches the handler.
        EgressRecordMap denied = server("PutVaultRow");
        applySeen = null;
        wantCode("Apply carrying a denied Put", callApply(denied, "/system", apply(putOp("a", "/tenants/acme"))),
            Status.Code.PERMISSION_DENIED, "denied: PutVaultRow");
        if (applySeen != null) {
            fail("an Apply carrying an op its caller may not make must not reach the handler");
        }
        wantOk("Apply whose ops are all allowed", callApply(denied, "/system", apply(auditOp(1))));

        // The tenant pin runs per op.
        wantCode("Apply with a foreign tenant op", callApply(p, "/tenants/acme", apply(putOp("a", "/tenants/beta"))),
            Status.Code.INVALID_ARGUMENT, "Apply (put_vault_row): item.namespace names a tenant other than the caller's");
    }

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

        durabilityFloor();
        tenantPin();
        linkedClaimWidensNothing();
        applyGuard();

        if (failures > 0) {
            System.err.println(failures + " record write guard check(s) failed");
            System.exit(1);
        }
        System.out.println("RecordWriteGuardTest: all checks passed");
    }
}
