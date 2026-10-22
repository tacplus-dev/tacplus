// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// The java leg of the serialization-equivalence corpus (LED-2 phase 2).
// Decodes the checked-in golden bytes with the GENERATED CorpusRecord type and
// asserts every field equals the fixture's `expect` block. It never encodes:
// go owns the bytes.
//
// uint64 is the shape that bites here: protobuf-java has no unsigned long, so
// big_unsigned arrives as a NEGATIVE long and only Long.toUnsignedString
// renders it correctly. That is exactly what the `sparse` vector's 2^64-1
// value is for.

package decoder;

import com.google.gson.JsonObject;
import com.google.gson.JsonParser;
import com.gitlab.example.tacplus.corpus.corpusclient.model.types.corpus.corpusclient.Corpusclient.CorpusRecord;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Map;

public final class Decoder {

    private Decoder() {}

    private static void fail(String message) {
        System.err.println("FAIL: " + message);
        System.exit(1);
    }

    private static byte[] unhex(String text) {
        if (text.length() % 2 != 0) {
            fail("hex string has odd length: " + text);
        }
        byte[] out = new byte[text.length() / 2];
        for (int i = 0; i < out.length; i++) {
            out[i] = (byte) Integer.parseInt(text.substring(i * 2, i * 2 + 2), 16);
        }
        return out;
    }

    private static String hex(byte[] bytes) {
        StringBuilder builder = new StringBuilder(bytes.length * 2);
        for (byte value : bytes) {
            builder.append(String.format("%02x", value));
        }
        return builder.toString();
    }

    private static void check(String name, JsonObject expect, byte[] golden) throws Exception {
        CorpusRecord record = CorpusRecord.parseFrom(golden);

        String wantNegative = expect.get("big_negative").getAsString();
        if (!Long.toString(record.getBigNegative()).equals(wantNegative)) {
            fail(name + ": big_negative: got " + record.getBigNegative() + " want " + wantNegative);
        }
        String wantUnsigned = expect.get("big_unsigned").getAsString();
        String gotUnsigned = Long.toUnsignedString(record.getBigUnsigned());
        if (!gotUnsigned.equals(wantUnsigned)) {
            fail(name + ": big_unsigned: got " + gotUnsigned + " want " + wantUnsigned);
        }

        Map<String, String> labels = record.getLabelsMap();
        JsonObject wantLabels = expect.getAsJsonObject("labels");
        if (labels.size() != wantLabels.size()) {
            fail(name + ": labels: got " + labels.size() + " entries want " + wantLabels.size());
        }
        for (Map.Entry<String, com.google.gson.JsonElement> entry : wantLabels.entrySet()) {
            String key = entry.getKey();
            if (!labels.containsKey(key)) {
                fail(name + ": labels[" + key + "]: missing");
            }
            String want = entry.getValue().getAsString();
            if (!want.equals(labels.get(key))) {
                fail(name + ": labels[" + key + "]: got " + labels.get(key) + " want " + want);
            }
        }

        boolean wantPresent = expect.get("opt_count_present").getAsBoolean();
        if (record.hasOptCount() != wantPresent) {
            fail(name + ": opt_count presence: got " + record.hasOptCount() + " want " + wantPresent);
        }
        if (wantPresent && record.getOptCount() != expect.get("opt_count").getAsInt()) {
            fail(name + ": opt_count: got " + record.getOptCount()
                    + " want " + expect.get("opt_count").getAsInt());
        }

        String wantBlob = expect.get("blob_hex").getAsString();
        String gotBlob = hex(record.getBlob().toByteArray());
        if (!gotBlob.equals(wantBlob)) {
            fail(name + ": blob: got " + gotBlob + " want " + wantBlob);
        }

        System.out.println("java: " + name + " OK (" + golden.length + " bytes)");
    }

    public static void main(String[] args) throws Exception {
        if (args.length != 1) {
            fail("usage: Decoder <wire_corpus.json>");
        }
        String raw = readFixture(args[0]);
        JsonObject fixture = JsonParser.parseString(raw).getAsJsonObject();
        var vectors = fixture.getAsJsonArray("vectors");
        if (vectors.isEmpty()) {
            fail("fixture has no vectors");
        }
        for (var element : vectors) {
            JsonObject vector = element.getAsJsonObject();
            check(
                    vector.get("name").getAsString(),
                    vector.getAsJsonObject("expect"),
                    unhex(vector.get("hex").getAsString()));
        }
        System.out.println("java: " + vectors.size() + " vectors OK");
    }

    private static String readFixture(String path) throws IOException {
        return new String(Files.readAllBytes(Path.of(path)), StandardCharsets.UTF_8);
    }
}
