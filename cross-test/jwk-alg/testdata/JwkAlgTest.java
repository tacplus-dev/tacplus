// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// The java leg of the jwk-alg gate (issue #179): the generated JWK and token
// chassis against cross-test/fixtures/jwk_alg_vectors.json. Mirrors the go leg
// (jwk_alg_test.go): build each case's JWK, parse its token, verify; any
// refusal on the way is a reject.

package jwkalg;

import com.gitlab.example.tacplus.jwkalg.jwkalgclient.model.chassis.jwk.ImmutableJwk;
import com.gitlab.example.tacplus.jwkalg.jwkalgclient.model.chassis.token.TokenWrapper;
import com.gitlab.example.tacplus.jwkalg.jwkalgclient.model.types.sys.jwk.JwkOuterClass;
import com.gitlab.example.tacplus.jwkalg.jwkalgclient.model.types.sys.key.KeyOuterClass;
import com.google.gson.JsonElement;
import com.google.gson.JsonObject;
import com.google.gson.JsonParser;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

public final class JwkAlgTest {

    private JwkAlgTest() {}

    private static String str(JsonObject m, String k) {
        JsonElement v = m.get(k);
        return v != null && v.isJsonPrimitive() && v.getAsJsonPrimitive().isString() ? v.getAsString() : "";
    }

    private static JwkOuterClass.Jwk toProto(JsonObject m) {
        KeyOuterClass.KeyType kty = switch (str(m, "kty")) {
            case "RSA" -> KeyOuterClass.KeyType.KEY_TYPE_RSA;
            case "EC" -> KeyOuterClass.KeyType.KEY_TYPE_EC;
            case "OKP" -> KeyOuterClass.KeyType.KEY_TYPE_OKP;
            default -> KeyOuterClass.KeyType.KEY_TYPE_UNDEFINED;
        };
        JwkOuterClass.JwkUseType use = "sig".equals(str(m, "use"))
                ? JwkOuterClass.JwkUseType.JWK_USE_TYPE_SIG
                : JwkOuterClass.JwkUseType.JWK_USE_TYPE_UNDEFINED;
        return JwkOuterClass.Jwk.newBuilder()
                .setKty(kty)
                .setAlg(str(m, "alg"))
                .setKid(str(m, "kid"))
                .setUse(use)
                .setN(str(m, "n"))
                .setE(str(m, "e"))
                .setCrv(str(m, "crv"))
                .setX(str(m, "x"))
                .setY(str(m, "y"))
                .build();
    }

    // verify builds the JWK and verifies the token against it; any refusal on
    // the way (the JWK, the token's parse, its verification) is a reject.
    private static Exception verify(JwkOuterClass.Jwk key, String token) {
        try {
            ImmutableJwk jwk = ImmutableJwk.from(key);
            TokenWrapper tw = TokenWrapper.parse(token);
            tw.verify(jwk);
            return null;
        } catch (Exception e) {
            return e;
        }
    }

    public static void main(String[] args) throws IOException {
        if (args.length != 1) {
            System.err.println("usage: JwkAlgTest <fixture.json>");
            System.exit(2);
        }
        JsonObject root = JsonParser.parseString(
                Files.readString(Path.of(args[0]), StandardCharsets.UTF_8)).getAsJsonObject();

        Map<String, JwkOuterClass.Jwk> keys = new HashMap<>();
        for (Map.Entry<String, JsonElement> group : root.getAsJsonObject("jwks").entrySet()) {
            for (JsonElement e : group.getValue().getAsJsonArray()) {
                JsonObject m = e.getAsJsonObject();
                keys.put(str(m, "kid"), toProto(m));
            }
        }

        List<String> failures = new ArrayList<>();
        int total = 0;
        int accepted = 0;
        for (JsonElement e : root.getAsJsonArray("cases")) {
            JsonObject c = e.getAsJsonObject();
            total++;
            String name = str(c, "name");
            String expect = str(c, "expect");
            JwkOuterClass.Jwk key = keys.get(str(c, "key"));
            if (key == null) {
                failures.add(name + ": fixture has no key " + str(c, "key"));
                continue;
            }
            Exception err = verify(key, str(c, "token"));
            if ("accept".equals(expect) && err != null) {
                failures.add(name + ": expected accept, got " + err);
            } else if ("reject".equals(expect) && err == null) {
                failures.add(name + ": expected reject, but the token verified");
            } else if ("accept".equals(expect)) {
                accepted++;
            } else if (!"reject".equals(expect)) {
                failures.add(name + ": unknown expect " + expect);
            }
        }
        if (total < 100) {
            failures.add("fixture has " + total + " cases; expected the full set");
        }

        // The accepted set comes from the key, narrowed to a declared alg; the
        // alg a JWK reports is the one it declares, never a guess.
        Map<String, List<String>> wantAlgs = new LinkedHashMap<>();
        wantAlgs.put("rsa-noalg", List.of("RS256", "RS384", "RS512"));
        wantAlgs.put("rsa-rs256", List.of("RS256"));
        wantAlgs.put("ms-noalg", List.of("RS256", "RS384", "RS512"));
        wantAlgs.put("ec-p256-noalg", List.of("ES256"));
        wantAlgs.put("ec-p384-noalg", List.of("ES384"));
        wantAlgs.put("okp-noalg", List.of("EdDSA"));
        wantAlgs.put("okp-eddsa", List.of("EdDSA"));
        for (Map.Entry<String, List<String>> w : wantAlgs.entrySet()) {
            String kid = w.getKey();
            JwkOuterClass.Jwk key = keys.get(kid);
            if (key == null) {
                failures.add(kid + ": fixture has no such key");
                continue;
            }
            ImmutableJwk jwk;
            try {
                jwk = ImmutableJwk.from(key);
            } catch (Exception ex) {
                failures.add(kid + ": " + ex);
                continue;
            }
            if (!jwk.getAcceptedAlgs().equals(w.getValue())) {
                failures.add(kid + ": getAcceptedAlgs() = " + jwk.getAcceptedAlgs() + ", want " + w.getValue());
            }
            if (!jwk.getAlg().equals(key.getAlg())) {
                failures.add(kid + ": getAlg() = \"" + jwk.getAlg() + "\", want the declared \"" + key.getAlg() + "\"");
            }
        }

        for (String f : failures) {
            System.out.println("FAIL: " + f);
        }
        System.out.println("jwk-alg java: " + total + " cases, " + accepted + " accepted");
        if (!failures.isEmpty()) {
            System.exit(1);
        }
    }
}
