// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the owner-default fixture. NOT generated: run.sh copies this
// file into the generated chassis/grpcserver package before the Java compile and
// runs it as a main-method self-test, like the generated AuthorizerBehaviorTest.
//
// What it pins: the (spec.owner_default) guard the Java wrapper generates, run
// through the REAL generated DeskService wrapper and the real Authorizer over
// real signed tokens. ownerdesk.Desk declares {read: true, write: false}, so:
//   - UpdateDesk from the row's owner (a self-scoped caller whose subject the
//     wrapper stamps into the request's (spec.principal) field) is refused
//     PERMISSION_DENIED and never reaches the handler,
//   - the same call from a caller whose grant carries a wider
//     (spec.principal_scope) reaches the handler — without this control a guard
//     that refused every caller would pass,
//   - CreateDesk (no prior owner) and GetDesk (a read the owner may make) reach
//     the handler for the same self-scoped owner.

package com.gitlab.example.dummy.ownerdesk.model.chassis.grpcserver;

import com.gitlab.example.dummy.ownerdesk.model.chassis.key.ImmutableKey;
import com.gitlab.example.dummy.ownerdesk.model.chassis.key.KeyHelpers;
import com.gitlab.example.dummy.ownerdesk.model.chassis.servicemanager.Auth;
import com.gitlab.example.dummy.ownerdesk.model.chassis.servicemanager.Caller;
import com.gitlab.example.dummy.ownerdesk.model.chassis.token.TokenWrapper;
import com.gitlab.example.dummy.ownerdesk.model.s2sm.internal.grpc.servers.services.ownerdesk.DeskService;
import com.gitlab.example.dummy.ownerdesk.model.types.generated.entitlements.Entitlements;
import com.gitlab.example.dummy.ownerdesk.model.types.services.ownerdesk.Ownerdesk;
import com.gitlab.example.dummy.ownerdesk.model.types.sys.jwk.JwkOuterClass;
import com.gitlab.example.dummy.ownerdesk.model.types.sys.key.KeyOuterClass;

import io.grpc.Context;
import io.grpc.Status;
import io.grpc.stub.StreamObserver;

import java.util.ArrayList;
import java.util.Base64;
import java.util.List;
import java.util.Map;

public class OwnerDefaultTest {
    private static final String OWNER = "spiffe://example.com/tenants/acme/services/clerk";

    private static int failures = 0;

    /** Records which handler methods the wrapper reached. */
    private static final class StubHandler implements DeskService.DeskServiceHandler {
        final List<String> reached = new ArrayList<>();

        @Override
        public Ownerdesk.CreateDeskResponse createDesk(Caller caller, Ownerdesk.CreateDeskRequest request) {
            reached.add("CreateDesk");
            return Ownerdesk.CreateDeskResponse.getDefaultInstance();
        }

        @Override
        public Ownerdesk.UpdateDeskResponse updateDesk(Caller caller, Ownerdesk.UpdateDeskRequest request) {
            reached.add("UpdateDesk");
            return Ownerdesk.UpdateDeskResponse.getDefaultInstance();
        }

        @Override
        public Ownerdesk.GetDeskResponse getDesk(Caller caller, Ownerdesk.GetDeskRequest request) {
            reached.add("GetDesk");
            return Ownerdesk.GetDeskResponse.getDefaultInstance();
        }
    }

    /** Captures the one outcome a unary call produces. */
    private static final class Outcome<T> implements StreamObserver<T> {
        T value;
        Throwable error;
        boolean completed;

        @Override public void onNext(T v) { value = v; }
        @Override public void onError(Throwable t) { error = t; }
        @Override public void onCompleted() { completed = true; }

        Status.Code code() {
            return error == null ? Status.Code.OK : Status.fromThrowable(error).getCode();
        }
    }

    private static String bearer(ImmutableKey key, String principalScope) throws Exception {
        TokenWrapper tok = TokenWrapper.create()
            .setIssuer("test-issuer")
            .setAudience(Authorizer.SERVICE_NAME)
            .setExpiration(System.currentTimeMillis() / 1000 + 3600)
            .setNamespace("/tenants/acme")
            .setSubType("service")
            .setSubject(OWNER);
        if (!principalScope.isEmpty()) {
            tok.setServiceEntitlements(Entitlements.TokenEntitlements.newBuilder().setPrincipalScope(principalScope).build());
        }
        tok.sign(key);
        return "Bearer " + tok.getBase64();
    }

    private static Authorizer authorizer(ImmutableKey key) {
        // The verifying JWK (OKP/Ed25519): the raw 32-byte public key is the
        // tail of the X.509 SPKI encoding.
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
        // The generated registration, not a restatement of it.
        DeskService.registerMethodAuth(a);
        return a;
    }

    /** Runs call inside the per-call context the Authorizer interceptor would install. */
    private static void asCaller(String bearer, Runnable call) {
        Auth.withVerifiedTokenSlot(Context.current())
            .withValue(Authorizer.AUTH_HEADER_CTX_KEY, bearer)
            .run(call);
    }

    private static void check(boolean ok, String what) {
        if (!ok) {
            System.err.println("FAIL: " + what);
            failures++;
        }
    }

    public static void main(String[] args) throws Exception {
        ImmutableKey key = ImmutableKey.from(KeyHelpers.newEd25519Key());
        Authorizer a = authorizer(key);
        String self = bearer(key, "");
        String wide = bearer(key, "PRINCIPAL_SCOPE_ANY");

        // The owner's own write: stamped to the owner, then refused.
        StubHandler stub = new StubHandler();
        DeskService svc = new DeskService(stub, a);
        Outcome<Ownerdesk.UpdateDeskResponse> update = new Outcome<>();
        asCaller(self, () -> svc.updateDesk(Ownerdesk.UpdateDeskRequest.newBuilder().setDeskId("d1").setLabel("new").build(), update));
        check(update.code() == Status.Code.PERMISSION_DENIED,
            "UpdateDesk by the self-scoped owner must be PERMISSION_DENIED, got " + update.code() + " " + update.error);
        check(update.error != null && String.valueOf(update.error.getMessage()).contains("(spec.owner_default) on Desk (write)"),
            "the refusal names the owner default, got: " + update.error);
        check(!stub.reached.contains("UpdateDesk"), "the refused UpdateDesk must not reach the handler");

        // The paired control: a wider principal scope is not the owner acting on itself.
        Outcome<Ownerdesk.UpdateDeskResponse> widened = new Outcome<>();
        asCaller(wide, () -> svc.updateDesk(Ownerdesk.UpdateDeskRequest.newBuilder().setDeskId("d1").setLabel("new").build(), widened));
        check(widened.code() == Status.Code.OK && widened.completed,
            "UpdateDesk by a caller with PRINCIPAL_SCOPE_ANY must reach the handler, got " + widened.code() + " " + widened.error);
        check(stub.reached.contains("UpdateDesk"), "the widened UpdateDesk reached the handler");

        // CREATE has no prior owner; a read the owner holds is not contradicted.
        Outcome<Ownerdesk.CreateDeskResponse> create = new Outcome<>();
        asCaller(self, () -> svc.createDesk(Ownerdesk.CreateDeskRequest.newBuilder().setLabel("mine").build(), create));
        check(create.code() == Status.Code.OK && stub.reached.contains("CreateDesk"),
            "CreateDesk by the owner must reach the handler, got " + create.code() + " " + create.error);
        Outcome<Ownerdesk.GetDeskResponse> get = new Outcome<>();
        asCaller(self, () -> svc.getDesk(Ownerdesk.GetDeskRequest.newBuilder().setDeskId("d1").build(), get));
        check(get.code() == Status.Code.OK && stub.reached.contains("GetDesk"),
            "GetDesk by the owner must reach the handler, got " + get.code() + " " + get.error);

        if (failures > 0) {
            System.err.println(failures + " owner-default check(s) failed");
            System.exit(1);
        }
        System.out.println("OwnerDefaultTest: all checks passed");
    }
}
