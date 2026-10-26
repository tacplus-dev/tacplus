// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the tenant-slice fixture's grpc_config owner (issue #135).
// NOT generated: run.sh copies this file into the generated
// chassis/grpcserver package of the tenancy build and runs it as a main-method
// self-test, like TenantPinnedReadTest. It lives in that package for
// Authorizer.SERVICE_NAME, the audience a token must carry.
//
// tenancy's Config holds each tenant's generated TenantConfig in its `tenants`
// map, and ledger owns a slice of it, so ledger holds a TENANT-PINNED read of
// the Config. Before #135 ledger was not an allowed caller of the service at
// all (its Watch was refused outright) and nothing filtered the map. This
// drives the real generated TenancyConfigStateService wrapper, behind the real
// Authorizer with the method auth the wrapper registers, over real signed
// tokens and a handler holding tenants x and y — each carrying ledger's slice
// AND registrar's secret slice — and pins what the serving side must do on
// getConfig and watchConfig:
//   - ledger in /tenants/x gets x's entry only, projected to ledger's slice
//     (registrar's client_secret never leaves), and none of the Config's other
//     fields;
//   - ledger in /system gets every tenant, each projected;
//   - ledger in any other namespace is PERMISSION_DENIED;
//   - ledger is refused setConfig outright;
//   - the owner (tenancy) is not pinned and reads the Config unchanged.
// With the filter removed from the generator, the projection and the
// one-tenant checks fail.

package com.gitlab.example.dummy.tenancy.model.chassis.grpcserver;

import com.gitlab.example.dummy.tenancy.model.chassis.key.ImmutableKey;
import com.gitlab.example.dummy.tenancy.model.chassis.key.KeyHelpers;
import com.gitlab.example.dummy.tenancy.model.chassis.servicemanager.Auth;
import com.gitlab.example.dummy.tenancy.model.chassis.token.TokenWrapper;
import com.gitlab.example.dummy.tenancy.model.s2sm.internal.grpc.servers.services.tenancy.TenancyConfigStateService;
import com.gitlab.example.dummy.tenancy.model.types.generated.tenant_config.TenantConfigOuterClass;
import com.gitlab.example.dummy.tenancy.model.types.platform.registrarslice.Registrarslice;
import com.gitlab.example.dummy.tenancy.model.types.services.ledger.Ledger;
import com.gitlab.example.dummy.tenancy.model.types.services.tenancy.Tenancy;
import com.gitlab.example.dummy.tenancy.model.types.sys.jwk.JwkOuterClass;
import com.gitlab.example.dummy.tenancy.model.types.sys.key.KeyOuterClass;
import com.gitlab.example.dummy.tenancy.model.types.sys.trace.Trace;

import io.grpc.Context;
import io.grpc.Status;
import io.grpc.stub.StreamObserver;

import java.lang.reflect.Proxy;
import java.util.ArrayList;
import java.util.Base64;
import java.util.List;
import java.util.Map;
import java.util.TreeSet;

public class TenantPinnedConfigReadTest {
    private static int failures = 0;

    private static void fail(String message) {
        System.err.println("FAIL: " + message);
        failures++;
    }

    private static TenantConfigOuterClass.TenantConfig tenant(String tenant) {
        return TenantConfigOuterClass.TenantConfig.newBuilder()
            .setLedger(Ledger.LedgerTenantConfig.newBuilder().setCurrency("cur-" + tenant))
            .setRegistrar(Registrarslice.RegistrarTenantConfig.newBuilder().setClientSecret("secret-" + tenant))
            .build();
    }

    private static Tenancy.Config config() {
        return Tenancy.Config.newBuilder()
            .putTenants("x", tenant("x"))
            .putTenants("y", tenant("y"))
            .setRegion("owner-region")
            .setTraceId("owner-only")
            .build();
    }

    /** A handler serving tenants x and y; any other method throws. */
    @SuppressWarnings("unchecked")
    private static TenancyConfigStateService.TenancyConfigStateServiceHandler handler() {
        return (TenancyConfigStateService.TenancyConfigStateServiceHandler) Proxy.newProxyInstance(
            TenantPinnedConfigReadTest.class.getClassLoader(),
            new Class<?>[] {TenancyConfigStateService.TenancyConfigStateServiceHandler.class},
            (proxy, method, args) -> {
                switch (method.getName()) {
                    case "getConfig":
                    case "setConfig":
                        return config();
                    case "watchConfig": {
                        StreamObserver<Tenancy.Config> o = (StreamObserver<Tenancy.Config>) args[1];
                        o.onNext(config());
                        o.onCompleted();
                        return null;
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
        void run(TenancyConfigStateService svc, StreamObserver<T> observer);
    }

    private static ImmutableKey key;
    private static Authorizer authorizer;
    private static TenancyConfigStateService service;

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
        TenancyConfigStateService.registerMethodAuth(a);
        return a;
    }

    /** c must carry exactly the tenants in want, each with ledger's slice only, and none of the Config's other fields. */
    private static void wantProjected(String what, Tenancy.Config c, String... want) {
        if (c == null) {
            return;
        }
        if (!new TreeSet<>(c.getTenantsMap().keySet()).equals(new TreeSet<>(List.of(want)))) {
            fail(what + ": tenants " + c.getTenantsMap().keySet() + ", want " + List.of(want));
            return;
        }
        for (String t : want) {
            TenantConfigOuterClass.TenantConfig e = c.getTenantsMap().get(t);
            if (!("cur-" + t).equals(e.getLedger().getCurrency())) {
                fail(what + ": tenant " + t + " ledger slice = " + e.getLedger().getCurrency() + ", want the caller's own slice");
            }
            if (e.hasRegistrar()) {
                fail(what + ": another service's slice (registrar client_secret "
                    + e.getRegistrar().getClientSecret() + ") reached a pinned caller");
            }
        }
        if (!c.getTraceId().isEmpty() || !c.getRegion().isEmpty()) {
            fail(what + ": the Config's own fields (region " + c.getRegion() + ", trace_id " + c.getTraceId() + ") reached a pinned caller");
        }
    }

    public static void main(String[] args) throws Exception {
        key = ImmutableKey.from(KeyHelpers.newEd25519Key());
        authorizer = authorizer(key);
        service = new TenancyConfigStateService(handler(), authorizer);

        Trace.TraceRequest trace = Trace.TraceRequest.getDefaultInstance();

        // Tenant x: its own entry only, projected, on both reads.
        wantProjected("GetConfig /tenants/x", only("GetConfig /tenants/x",
            TenantPinnedConfigReadTest.<Tenancy.Config>as("ledger", "/tenants/x", (s, o) -> s.getConfig(trace, o))), "x");
        wantProjected("WatchConfig /tenants/x", only("WatchConfig /tenants/x",
            TenantPinnedConfigReadTest.<Tenancy.Config>as("ledger", "/tenants/x", (s, o) -> s.watchConfig(trace, o))), "x");

        // /system: every tenant, each projected.
        wantProjected("GetConfig /system", only("GetConfig /system",
            TenantPinnedConfigReadTest.<Tenancy.Config>as("ledger", "/system", (s, o) -> s.getConfig(trace, o))), "x", "y");
        wantProjected("WatchConfig /system", only("WatchConfig /system",
            TenantPinnedConfigReadTest.<Tenancy.Config>as("ledger", "/system", (s, o) -> s.watchConfig(trace, o))), "x", "y");

        // Any other namespace: nothing.
        for (String ns : List.of("/tenants/x/workspaces/w")) {
            wantDenied("GetConfig from " + ns, TenantPinnedConfigReadTest.<Tenancy.Config>as("ledger", ns, (s, o) -> s.getConfig(trace, o)));
            wantDenied("WatchConfig from " + ns, TenantPinnedConfigReadTest.<Tenancy.Config>as("ledger", ns, (s, o) -> s.watchConfig(trace, o)));
        }

        // The grant is a read: the subscriber may not write the Config.
        wantDenied("SetConfig from ledger", TenantPinnedConfigReadTest.<Tenancy.Config>as("ledger", "/tenants/x",
            (s, o) -> s.setConfig(config(), o)));

        // The owner is not pinned.
        Tenancy.Config owner = only("GetConfig owner",
            TenantPinnedConfigReadTest.<Tenancy.Config>as("tenancy", "/system", (s, o) -> s.getConfig(trace, o)));
        if (owner != null && (owner.getTenantsCount() != 2
                || !"secret-y".equals(owner.getTenantsMap().get("y").getRegistrar().getClientSecret())
                || !"owner-region".equals(owner.getRegion()))) {
            fail("the owner's read must be unchanged: every tenant, unprojected, with the Config's other fields");
        }

        if (failures > 0) {
            System.err.println(failures + " tenant-pinned config read check(s) failed");
            System.exit(1);
        }
        System.out.println("TenantPinnedConfigReadTest: all checks passed");
    }
}
