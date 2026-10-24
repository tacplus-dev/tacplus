// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the tenant-slice fixture's service_state owner (issue
// #130). NOT generated: run.sh copies this file into the generated
// chassis/grpcserver package of the registrar build and runs it as a
// main-method self-test, like UserAndListedServiceTest. It lives in that
// package for Authorizer.SERVICE_NAME, the audience a token must carry.
//
// registrar.RegistrarState.tenant_configs holds each tenant's generated
// TenantConfig, and ledger owns a slice of it, so ledger holds a TENANT-PINNED
// read of that one collection. This drives the real generated
// RegistrarStateService wrapper, behind the real Authorizer with the method
// auth the wrapper registers, over real signed tokens and a handler holding
// tenants x and y — each carrying ledger's slice AND registrar's secret slice —
// and pins what the serving side must do:
//   - ledger in /tenants/x gets x's entry only, on every read RPC, projected to
//     the key and ledger's slice (registrar's client_secret never leaves);
//   - a Get/ContainsKey of y from /tenants/x is PERMISSION_DENIED;
//   - ledger in /system gets every tenant, each projected;
//   - ledger in any other namespace is PERMISSION_DENIED;
//   - ledger is refused the root's other boundary (tenants) outright;
//   - the owner (registrar) is not pinned and reads the entries unchanged.
// With the filter removed from the generator, the projection and the
// one-tenant checks fail.

package com.gitlab.example.dummy.registrar.model.chassis.grpcserver;

import com.gitlab.example.dummy.registrar.model.chassis.key.ImmutableKey;
import com.gitlab.example.dummy.registrar.model.chassis.key.KeyHelpers;
import com.gitlab.example.dummy.registrar.model.chassis.servicemanager.Auth;
import com.gitlab.example.dummy.registrar.model.chassis.token.TokenWrapper;
import com.gitlab.example.dummy.registrar.model.s2sm.internal.grpc.servers.services.registrar.RegistrarStateService;
import com.gitlab.example.dummy.registrar.model.types.generated.tenant_config.TenantConfigOuterClass;
import com.gitlab.example.dummy.registrar.model.types.platform.registrarslice.Registrarslice;
import com.gitlab.example.dummy.registrar.model.types.services.ledger.Ledger;
import com.gitlab.example.dummy.registrar.model.types.services.registrar.Registrar;
import com.gitlab.example.dummy.registrar.model.types.sys.jwk.JwkOuterClass;
import com.gitlab.example.dummy.registrar.model.types.sys.key.KeyOuterClass;
import com.gitlab.example.dummy.registrar.model.types.sys.trace.Trace;

import io.grpc.Context;
import io.grpc.Status;
import io.grpc.stub.StreamObserver;

import java.lang.reflect.Proxy;
import java.util.ArrayList;
import java.util.Base64;
import java.util.List;
import java.util.Map;
import java.util.TreeSet;

public class TenantPinnedReadTest {
    private static int failures = 0;

    private static void fail(String message) {
        System.err.println("FAIL: " + message);
        failures++;
    }

    private static Registrar.TenantConfigEntry entry(String tenant) {
        return Registrar.TenantConfigEntry.newBuilder()
            .setTenantName(tenant)
            .setConfig(TenantConfigOuterClass.TenantConfig.newBuilder()
                .setLedger(Ledger.LedgerTenantConfig.newBuilder().setCurrency("cur-" + tenant))
                .setRegistrar(Registrarslice.RegistrarTenantConfig.newBuilder().setClientSecret("secret-" + tenant)))
            .build();
    }

    private static Map<String, Registrar.TenantConfigEntry> all() {
        return Map.of("x", entry("x"), "y", entry("y"));
    }

    /** A handler serving tenants x and y; any other method throws. */
    @SuppressWarnings("unchecked")
    private static RegistrarStateService.RegistrarStateServiceHandler handler() {
        return (RegistrarStateService.RegistrarStateServiceHandler) Proxy.newProxyInstance(
            TenantPinnedReadTest.class.getClassLoader(),
            new Class<?>[] {RegistrarStateService.RegistrarStateServiceHandler.class},
            (proxy, method, args) -> {
                switch (method.getName()) {
                    case "getTenantConfigs":
                    case "containsKeyTenantConfigs": {
                        Registrar.TenantConfigsGetRequest req = (Registrar.TenantConfigsGetRequest) args[1];
                        Registrar.TenantConfigsValueResponse.Builder b = Registrar.TenantConfigsValueResponse.newBuilder();
                        Registrar.TenantConfigEntry e = all().get(req.getKey());
                        if (e != null) {
                            b.setValue(e).setFound(true);
                        }
                        return b.build();
                    }
                    case "getAllTenantConfigs":
                        return Registrar.TenantConfigsEntriesResponse.newBuilder().putAllEntries(all()).build();
                    case "keySetTenantConfigs":
                        return Registrar.TenantConfigsKeysResponse.newBuilder().addKeys("x").addKeys("y").build();
                    case "sizeTenantConfigs":
                        return Registrar.TenantConfigsSizeResponse.newBuilder().setSize(2).build();
                    case "queryTenantConfigs":
                        return Registrar.TenantConfigsQueryResponse.newBuilder()
                            .addResults(entry("x")).addResults(entry("y")).setTotalCount(2).build();
                    case "watchTenantConfigs": {
                        StreamObserver<Registrar.TenantConfigsEntriesResponse> o = (StreamObserver<Registrar.TenantConfigsEntriesResponse>) args[1];
                        o.onNext(Registrar.TenantConfigsEntriesResponse.newBuilder().putAllEntries(all()).build());
                        o.onCompleted();
                        return null;
                    }
                    case "watchQueryTenantConfigs": {
                        StreamObserver<Registrar.TenantConfigsQueryResponse> o = (StreamObserver<Registrar.TenantConfigsQueryResponse>) args[2];
                        o.onNext(Registrar.TenantConfigsQueryResponse.newBuilder()
                            .addResults(entry("x")).addResults(entry("y")).setTotalCount(2).build());
                        o.onCompleted();
                        return null;
                    }
                    case "getAllTenants":
                        return Registrar.TenantsEntriesResponse.getDefaultInstance();
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
        void run(RegistrarStateService svc, StreamObserver<T> observer);
    }

    private static ImmutableKey key;
    private static Authorizer authorizer;
    private static RegistrarStateService service;

    private static String bearer(String svc, String ns) throws Exception {
        TokenWrapper tok = TokenWrapper.create()
            .setIssuer("test-issuer")
            .setAudience(Authorizer.SERVICE_NAME)
            .setExpiration(System.currentTimeMillis() / 1000 + 3600)
            .setNamespace(ns)
            .setSubType("service")
            .setSubject("spiffe://example.com" + ns + "/services/" + svc);
        tok.sign(key);
        return "Bearer " + tok.getBase64();
    }

    /** Runs one call as svc in namespace ns, through the real Authorizer. */
    private static <T> Recorder<T> as(String svc, String ns, Call<T> call) throws Exception {
        Recorder<T> rec = new Recorder<>();
        Context ctx = Auth.withVerifiedTokenSlot(Context.ROOT)
            .withValue(Authorizer.AUTH_HEADER_CTX_KEY, bearer(svc, ns));
        ctx.run(() -> call.run(service, rec));
        return rec;
    }

    private static void wantDenied(String what, Recorder<?> rec) {
        if (rec.error == null || Status.fromThrowable(rec.error).getCode() != Status.Code.PERMISSION_DENIED) {
            fail(what + ": want PERMISSION_DENIED, got " + (rec.error == null ? rec.values : rec.error));
        }
    }

    private static <T> T only(String what, Recorder<T> rec) {
        if (rec.error != null || rec.values.size() != 1) {
            fail(what + ": want one response, got " + (rec.error != null ? rec.error : rec.values));
            return null;
        }
        return rec.values.get(0);
    }

    /** e must be tenant's entry carrying ledger's slice and nothing else. */
    private static void wantProjected(String what, String tenant, Registrar.TenantConfigEntry e) {
        if (e == null) {
            fail(what + ": no entry for " + tenant);
            return;
        }
        if (!tenant.equals(e.getTenantName())) {
            fail(what + ": tenant = " + e.getTenantName() + ", want " + tenant);
        }
        if (!("cur-" + tenant).equals(e.getConfig().getLedger().getCurrency())) {
            fail(what + ": ledger slice = " + e.getConfig().getLedger().getCurrency() + ", want the caller's own slice");
        }
        if (e.getConfig().hasRegistrar()) {
            fail(what + ": another service's slice (registrar client_secret "
                + e.getConfig().getRegistrar().getClientSecret() + ") reached a pinned caller");
        }
    }

    private static Authorizer authorizer(ImmutableKey key) {
        byte[] spki = key.getPublicKey().getEncoded();
        byte[] raw = new byte[32];
        System.arraycopy(spki, spki.length - 32, raw, 0, 32);
        JwkOuterClass.Jwk jwk = JwkOuterClass.Jwk.newBuilder()
            .setKid(key.getProto().getId())
            .setKty(KeyOuterClass.KeyType.KEY_TYPE_OKP)
            .setCrv("Ed25519")
            .setX(Base64.getUrlEncoder().withoutPadding().encodeToString(raw))
            .build();
        Authorizer a = new Authorizer(sink -> sink.update(Map.of(jwk.getKid(), jwk)));
        RegistrarStateService.registerMethodAuth(a);
        return a;
    }

    public static void main(String[] args) throws Exception {
        key = ImmutableKey.from(KeyHelpers.newEd25519Key());
        authorizer = authorizer(key);
        service = new RegistrarStateService(handler(), authorizer);

        Registrar.TenantConfigsGetAllRequest getAll = Registrar.TenantConfigsGetAllRequest.getDefaultInstance();

        // Tenant x: its own entry only, projected, on every read RPC.
        Registrar.TenantConfigsEntriesResponse all = only("GetAll /tenants/x",
            TenantPinnedReadTest.<Registrar.TenantConfigsEntriesResponse>as("ledger", "/tenants/x", (s, o) -> s.getAllTenantConfigs(getAll, o)));
        if (all != null) {
            if (!new TreeSet<>(all.getEntriesMap().keySet()).equals(new TreeSet<>(List.of("x")))) {
                fail("GetAll: tenant x was served keys " + all.getEntriesMap().keySet() + ", want only [x]");
            }
            wantProjected("GetAll", "x", all.getEntriesMap().get("x"));
        }
        Registrar.TenantConfigsValueResponse got = only("Get x",
            TenantPinnedReadTest.<Registrar.TenantConfigsValueResponse>as("ledger", "/tenants/x",
                (s, o) -> s.getTenantConfigs(Registrar.TenantConfigsGetRequest.newBuilder().setKey("x").build(), o)));
        if (got != null) {
            wantProjected("Get", "x", got.getValue());
        }
        Registrar.TenantConfigsKeysResponse keys = only("KeySet",
            TenantPinnedReadTest.<Registrar.TenantConfigsKeysResponse>as("ledger", "/tenants/x",
                (s, o) -> s.keySetTenantConfigs(Registrar.TenantConfigsKeysRequest.getDefaultInstance(), o)));
        if (keys != null && !keys.getKeysList().equals(List.of("x"))) {
            fail("KeySet: " + keys.getKeysList() + ", want [x]");
        }
        Registrar.TenantConfigsSizeResponse size = only("Size",
            TenantPinnedReadTest.<Registrar.TenantConfigsSizeResponse>as("ledger", "/tenants/x",
                (s, o) -> s.sizeTenantConfigs(Registrar.TenantConfigsSizeRequest.getDefaultInstance(), o)));
        if (size != null && size.getSize() != 1) {
            fail("Size: " + size.getSize() + ", want 1");
        }
        Registrar.TenantConfigsQueryResponse q = only("Query",
            TenantPinnedReadTest.<Registrar.TenantConfigsQueryResponse>as("ledger", "/tenants/x",
                (s, o) -> s.queryTenantConfigs(Registrar.TenantConfigsQueryRequest.getDefaultInstance(), o)));
        if (q != null) {
            if (q.getResultsCount() != 1 || q.getTotalCount() != 1) {
                fail("Query: " + q.getResultsCount() + " results, total " + q.getTotalCount() + ", want 1 and 1");
            } else {
                wantProjected("Query", "x", q.getResults(0));
            }
        }
        Registrar.TenantConfigsEntriesResponse frame = only("Watch",
            TenantPinnedReadTest.<Registrar.TenantConfigsEntriesResponse>as("ledger", "/tenants/x",
                (s, o) -> s.watchTenantConfigs(Trace.TraceRequest.getDefaultInstance(), o)));
        if (frame != null) {
            if (frame.getEntriesCount() != 1) {
                fail("Watch: frame carried " + frame.getEntriesMap().keySet() + ", want only x");
            }
            wantProjected("Watch", "x", frame.getEntriesMap().get("x"));
        }
        Registrar.TenantConfigsQueryResponse qframe = only("WatchQuery",
            TenantPinnedReadTest.<Registrar.TenantConfigsQueryResponse>as("ledger", "/tenants/x",
                (s, o) -> s.watchQueryTenantConfigs(Registrar.TenantConfigsQueryRequest.getDefaultInstance(), o)));
        if (qframe != null) {
            if (qframe.getResultsCount() != 1) {
                fail("WatchQuery: frame carried " + qframe.getResultsCount() + " results, want only x");
            } else {
                wantProjected("WatchQuery", "x", qframe.getResults(0));
            }
        }

        // A cross-tenant read is refused.
        wantDenied("Get y from /tenants/x", TenantPinnedReadTest.<Registrar.TenantConfigsValueResponse>as("ledger", "/tenants/x",
            (s, o) -> s.getTenantConfigs(Registrar.TenantConfigsGetRequest.newBuilder().setKey("y").build(), o)));
        wantDenied("ContainsKey y from /tenants/x", TenantPinnedReadTest.<Registrar.TenantConfigsValueResponse>as("ledger", "/tenants/x",
            (s, o) -> s.containsKeyTenantConfigs(Registrar.TenantConfigsGetRequest.newBuilder().setKey("y").build(), o)));

        // The grant is the one collection: the root's other boundary is refused.
        wantDenied("GetAllTenants from ledger", TenantPinnedReadTest.<Registrar.TenantsEntriesResponse>as("ledger", "/tenants/x",
            (s, o) -> s.getAllTenants(Registrar.TenantsGetAllRequest.getDefaultInstance(), o)));

        // /system: every tenant, each projected.
        Registrar.TenantConfigsEntriesResponse sys = only("GetAll /system",
            TenantPinnedReadTest.<Registrar.TenantConfigsEntriesResponse>as("ledger", "/system", (s, o) -> s.getAllTenantConfigs(getAll, o)));
        if (sys != null) {
            if (sys.getEntriesCount() != 2) {
                fail("GetAll from /system: keys " + sys.getEntriesMap().keySet() + ", want [x, y]");
            }
            for (String t : List.of("x", "y")) {
                wantProjected("GetAll /system", t, sys.getEntriesMap().get(t));
            }
        }

        // Any other namespace: nothing.
        for (String ns : List.of("/tenants/x/workspaces/w")) {
            wantDenied("GetAll from " + ns, TenantPinnedReadTest.<Registrar.TenantConfigsEntriesResponse>as("ledger", ns, (s, o) -> s.getAllTenantConfigs(getAll, o)));
        }

        // The owner is not pinned.
        Registrar.TenantConfigsEntriesResponse owner = only("GetAll owner",
            TenantPinnedReadTest.<Registrar.TenantConfigsEntriesResponse>as("registrar", "/system", (s, o) -> s.getAllTenantConfigs(getAll, o)));
        if (owner != null && (owner.getEntriesCount() != 2
                || !"secret-y".equals(owner.getEntriesMap().get("y").getConfig().getRegistrar().getClientSecret()))) {
            fail("the owner's read must be unchanged: every entry, unprojected");
        }

        if (failures > 0) {
            System.err.println(failures + " tenant-pinned read check(s) failed");
            System.exit(1);
        }
        System.out.println("TenantPinnedReadTest: all checks passed");
    }
}
