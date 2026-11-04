// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The java leg of the redact gate (issue #274). cross-test/redact/run.sh
// compiles it against the GENERATED redactcheck model and runs the generated
// Helpers.redact<T> and validators against
// cross-test/fixtures/redact_vectors.json.
package redactvectors;

import com.gitlab.example.tacplus.redact.redactcheck.model.types.redact.redactcheck.Helpers;
import com.gitlab.example.tacplus.redact.redactcheck.model.types.redact.redactcheck.Redactcheck;
import com.gitlab.example.tacplus.redact.redactcheck.model.types.sys.errors.ProtoException;
import com.google.gson.JsonElement;
import com.google.gson.JsonObject;
import com.google.gson.JsonParser;
import com.google.protobuf.ByteString;
import com.google.protobuf.CodedOutputStream;
import com.google.protobuf.InvalidProtocolBufferException;
import com.google.protobuf.Message;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.SecureRandom;
import java.util.ArrayList;
import java.util.HexFormat;
import java.util.List;
import java.util.Map;

public final class RedactVectors {
    private RedactVectors() {}

    private static final Map<String, String> RULE_TEXT = Map.of(
            "required", "required and empty",
            "placeholder", "a known placeholder value",
            "entropy", "estimated entropy below");

    private static Message redactByType(String type, byte[] in) throws InvalidProtocolBufferException {
        switch (type) {
            case "UniConfig":
                return Helpers.redactUniConfig(Redactcheck.UniConfig.parseFrom(in));
            case "Secrets":
                return Helpers.redactSecrets(Redactcheck.Secrets.parseFrom(in));
            case "Endpoint":
                return Helpers.redactEndpoint(Redactcheck.Endpoint.parseFrom(in));
            case "SecretCheck":
                return Helpers.redactSecretCheck(Redactcheck.SecretCheck.parseFrom(in));
            default:
                throw new IllegalArgumentException("unknown type " + type);
        }
    }

    // Deterministic serialization (sorted map keys), as the go leg marshals.
    private static byte[] marshal(Message m) throws IOException {
        byte[] out = new byte[m.getSerializedSize()];
        CodedOutputStream cos = CodedOutputStream.newInstance(out);
        cos.useDeterministicSerialization();
        m.writeTo(cos);
        cos.checkNoSpaceLeft();
        return out;
    }

    private static Redactcheck.SecretCheck.Builder baseSecretCheck() {
        byte[] key = new byte[32];
        for (int i = 0; i < key.length; i++) {
            key[i] = (byte) i;
        }
        return Redactcheck.SecretCheck.newBuilder()
                .setToken("Xk9#mP2$vL7@qR4!wT8&")
                .setKey(ByteString.copyFrom(key))
                .setOptionalToken("");
    }

    private static String str(JsonObject o, String k) {
        JsonElement e = o.get(k);
        return e == null || e.isJsonNull() ? "" : e.getAsString();
    }

    public static void main(String[] args) throws IOException {
        if (args.length != 1) {
            System.err.println("usage: RedactVectors <redact_vectors.json>");
            System.exit(2);
        }
        JsonObject vec = JsonParser.parseString(Files.readString(Path.of(args[0]), StandardCharsets.UTF_8)).getAsJsonObject();
        HexFormat hex = HexFormat.of();
        List<String> failures = new ArrayList<>();

        int redactCases = 0;
        for (JsonElement el : vec.getAsJsonArray("redact")) {
            JsonObject c = el.getAsJsonObject();
            redactCases++;
            String name = str(c, "name");
            String type = str(c, "type");
            byte[] in = hex.parseHex(str(c, "input_hex"));
            try {
                String got = hex.formatHex(marshal(redactByType(type, in)));
                if (!got.equals(str(c, "expect_hex"))) {
                    failures.add(name + ": redact" + type + " bytes\n got  " + got + "\n want " + str(c, "expect_hex"));
                    continue;
                }
                String again = hex.formatHex(marshal(redactByType(type, in)));
                if (!again.equals(got)) {
                    failures.add(name + ": redact" + type + " is not deterministic");
                }
            } catch (IOException | RuntimeException e) {
                failures.add(name + ": " + e);
            }
        }

        int verdicts = 0;
        for (JsonElement el : vec.getAsJsonArray("verdicts")) {
            JsonObject c = el.getAsJsonObject();
            verdicts++;
            String name = str(c, "name");
            String field = str(c, "field");
            String expect = str(c, "expect");
            byte[] value = hex.parseHex(str(c, "value_hex"));
            Redactcheck.SecretCheck.Builder b = baseSecretCheck();
            switch (field) {
                case "token":
                    b.setToken(new String(value, StandardCharsets.UTF_8));
                    break;
                case "key":
                    b.setKey(ByteString.copyFrom(value));
                    break;
                case "optional_token":
                    b.setOptionalToken(new String(value, StandardCharsets.UTF_8));
                    break;
                default:
                    failures.add(name + ": unknown field " + field);
                    continue;
            }
            String msg = null;
            try {
                Helpers.validateSecretCheck(b.build());
            } catch (ProtoException e) {
                if (e.getCode() != ProtoException.INVALID_ARGUMENT) {
                    failures.add(name + ": want INVALID_ARGUMENT, got code " + e.getCode());
                }
                msg = e.getMessage();
            }
            if (expect.equals("ok")) {
                if (msg != null) {
                    failures.add(name + ": want ok, got " + msg);
                }
                continue;
            }
            if (msg == null) {
                failures.add(name + ": want " + expect + ", got ok");
                continue;
            }
            String rule = RULE_TEXT.get(expect);
            if (rule == null) {
                failures.add(name + ": unknown expect " + expect);
                continue;
            }
            if (!msg.contains("SecretCheck." + field) || !msg.contains(rule)) {
                failures.add(name + ": want field SecretCheck." + field + " and rule \"" + rule + "\", got \"" + msg + "\"");
            }
            if (value.length >= 6 && msg.contains(new String(value, StandardCharsets.UTF_8))) {
                failures.add(name + ": the error echoes the value: \"" + msg + "\"");
            }
        }

        // A CSPRNG-generated 32-byte key passes.
        byte[] key = new byte[32];
        new SecureRandom().nextBytes(key);
        try {
            Helpers.validateSecretCheck(baseSecretCheck().setKey(ByteString.copyFrom(key)).build());
        } catch (ProtoException e) {
            failures.add("csprng key: want ok, got " + e.getMessage());
        }

        if (!failures.isEmpty()) {
            for (String f : failures) {
                System.err.println("FAIL " + f);
            }
            System.err.println("redact java: " + failures.size() + " failing case(s)");
            System.exit(1);
        }
        System.out.println("redact java: " + redactCases + " redact cases, " + verdicts + " verdicts");
    }
}
