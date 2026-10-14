// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the record-egress fixture. NOT generated: run.sh copies this
// file into the generated chassis/grpcserver package before the Java compile and
// runs it as a main-method self-test, like the generated AuthorizerBehaviorTest.
// It lives in the fixture because the user leg needs the fixture's REAL
// generated grant catalog — the Authorizer drops any grant the catalog does not
// know.
//
// What it pins: one c2s rpc callable by entitled USERS and by the SERVICE
// identities its (spec.rpc).auth_services lists, on the same surface.
// egressvault.EgressVaultApi/CreateVaultNote declares
//   {principal: RPC_PRINCIPAL_USER, flags: [RPC_FLAG_AUTH_ENTITLED_USERS],
//    auth_services: ["notary"]}
// and run.sh asserts the generated wrapper authorizes it with METHOD and
// registers exactly the setMethodAuth line this test repeats. Then, through the
// real Authorizer over real signed tokens:
//   - a user token holding the rpc's grant passes,
//   - a tenant-owned "notary" service token passes, and the verified token it
//     returns is what the wrapper builds the caller identity from — the
//     caller's SPIFFE subject and its tenant namespace, never a client-chosen
//     value,
//   - a service not listed is refused,
//   - a user token without the grant is still refused.

package com.gitlab.example.dummy.egressvault.model.chassis.grpcserver;

import com.gitlab.example.dummy.egressvault.model.chassis.key.ImmutableKey;
import com.gitlab.example.dummy.egressvault.model.chassis.key.KeyHelpers;
import com.gitlab.example.dummy.egressvault.model.chassis.token.TokenWrapper;
import com.gitlab.example.dummy.egressvault.model.types.generated.entitlements.Entitlements;
import com.gitlab.example.dummy.egressvault.model.types.sys.errors.ProtoException;
import com.gitlab.example.dummy.egressvault.model.types.sys.jwk.JwkOuterClass;
import com.gitlab.example.dummy.egressvault.model.types.sys.key.KeyOuterClass;

import java.util.Base64;
import java.util.List;
import java.util.Map;

public class UserAndListedServiceTest {
    /** The method the generated EgressVaultApi wrapper passes to authorize. */
    private static final String METHOD = "EgressVaultApi/CreateVaultNote";
    /** The generated exact grant for the rpc (grants catalog token). */
    private static final String GRANT = "EGRESSVAULT_EGRESS_VAULT_API_CREATE_VAULT_NOTE";
    private static final String NOTARY = "spiffe://example.com/tenants/acme/services/notary";
    private static final String STRANGER = "spiffe://example.com/tenants/acme/services/stranger";

    private static int failures = 0;

    private interface Customize {
        void apply(TokenWrapper tok) throws Exception;
    }

    private static String signed(ImmutableKey key, Customize customize) throws Exception {
        TokenWrapper tok = TokenWrapper.create()
            .setIssuer("test-issuer")
            .setAudience(Authorizer.SERVICE_NAME)
            .setExpiration(System.currentTimeMillis() / 1000 + 3600)
            .setNamespace("/tenants/acme");
        customize.apply(tok);
        tok.sign(key);
        return "Bearer " + tok.getBase64();
    }

    private static String userToken(ImmutableKey key, String... grants) throws Exception {
        return signed(key, tok -> tok.setSubType("user")
            .setSubject("someone@example.com")
            .setServiceEntitlements(Entitlements.TokenEntitlements.newBuilder().addAllGrants(List.of(grants)).build()));
    }

    private static String serviceToken(ImmutableKey key, String spiffeId) throws Exception {
        return signed(key, tok -> tok.setSubType("service").setSubject(spiffeId));
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
        // Exactly the line the generated registerMethodAuth emits for this rpc.
        a.setMethodAuth("EgressVaultApi/CreateVaultNote", List.of("notary"));
        return a;
    }

    private static void fail(String message) {
        System.err.println("FAIL: " + message);
        failures++;
    }

    public static void main(String[] args) throws Exception {
        ImmutableKey key = ImmutableKey.from(KeyHelpers.newEd25519Key());
        Authorizer a = authorizer(key);

        try {
            a.authorizeWithToken(userToken(key, GRANT), METHOD, null);
        } catch (ProtoException e) {
            fail("a user holding the rpc's grant must pass, got: " + e);
        }

        try {
            TokenWrapper verified = a.authorizeWithToken(serviceToken(key, NOTARY), METHOD, null);
            if (!NOTARY.equals(verified.getSubject())) {
                fail("listed service subject = " + verified.getSubject() + ", want " + NOTARY);
            }
            if (!NOTARY.equals(Authorizer.identityClaims(verified).get("sub"))) {
                fail("listed service caller identity sub = " + Authorizer.identityClaims(verified).get("sub") + ", want " + NOTARY);
            }
            if (!"/tenants/acme".equals(verified.getNamespace())) {
                fail("listed service namespace = " + verified.getNamespace() + ", want /tenants/acme");
            }
            if (!"notary".equals(verified.servicePath())) {
                fail("listed service path = " + verified.servicePath() + ", want notary");
            }
        } catch (ProtoException e) {
            fail("a service listed in auth_services must pass, got: " + e);
        }

        try {
            a.authorizeWithToken(serviceToken(key, STRANGER), METHOD, null);
            fail("a service not in auth_services must be refused");
        } catch (ProtoException expected) {
            // Denied via the typed error path — expected.
        }

        try {
            a.authorizeWithToken(userToken(key), METHOD, null);
            fail("auth_services must not open the rpc to users without its grant");
        } catch (ProtoException expected) {
            // Denied via the typed error path — expected.
        }

        if (failures > 0) {
            System.err.println(failures + " user-and-listed-service check(s) failed");
            System.exit(1);
        }
        System.out.println("UserAndListedServiceTest: all checks passed");
    }
}
