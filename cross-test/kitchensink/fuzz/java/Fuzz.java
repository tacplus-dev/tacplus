// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The kitchensink's Java fuzzer (tacplus#196): a seeded mutation loop over the
// generated Java of the kitchensink member, the same properties as the Go and
// Rust targets. A message that decodes must validate without anything but a
// ProtoException, survive a round trip, and, for Bounds and Sizes, be accepted
// exactly when (Bounds) or only when (Sizes) the declared bounds hold.
// cross-test/kitchensink/run.sh compiles it against the generated model and
// runs it: java Fuzz <seconds> <seed>.
package com.example.kitchensink.model.types.services.kitchensink;

import com.example.kitchensink.model.types.sys.errors.ProtoException;
import com.google.protobuf.InvalidProtocolBufferException;
import com.google.protobuf.Message;
import java.util.ArrayList;
import java.util.List;
import java.util.Random;

public class Fuzz {
    interface Target {
        void run(byte[] data) throws Exception;
    }

    static boolean accepts(Runnable validate) {
        try {
            validate.run();
            return true;
        } catch (ProtoException e) {
            return false;
        }
    }

    static void roundTrip(Message m, Message back) {
        if (!m.equals(back)) {
            throw new AssertionError("round trip changed the message:\n" + m + "\n" + back);
        }
    }

    static boolean boundsHold(Kitchensink.Bounds m) {
        int oneOf = m.getOneOf();
        return m.getPort() >= 0 && m.getPort() <= 65535
            && m.getGtZero() > 0
            && m.getGeMinusTen() >= -10
            && Integer.toUnsignedLong(m.getLtHundred()) < 100
            && Long.compareUnsigned(m.getLeThousand(), 1000) <= 0
            && m.getWindow() >= 1 && m.getWindow() <= 64
            && m.getNotSeven() != 7
            && (oneOf == 1 || oneOf == 2 || oneOf == 4 || oneOf == 8)
            && m.getEven() % 2 == 0
            && (m.getModeValue() == 1 || m.getModeValue() == 2)
            && m.getTraceIdBytes().size() <= 32768;
    }

    static final Target BOUNDS = data -> {
        Kitchensink.Bounds m = Kitchensink.Bounds.parseFrom(data);
        boolean ok = accepts(() -> Helpers.validateBounds(m));
        if (ok != boundsHold(m)) {
            throw new AssertionError("validateBounds accepted=" + ok + " for " + m);
        }
        roundTrip(m, Kitchensink.Bounds.parseFrom(m.toByteArray()));
    };

    static final Target SIZES = data -> {
        Kitchensink.Sizes m = Kitchensink.Sizes.parseFrom(data);
        if (!accepts(() -> Helpers.validateSizes(m))) {
            return;
        }
        int name = m.getNameBytes().size();
        int code = m.getCodeBytes().size();
        boolean tagsOk = m.getTagsList().stream().allMatch(t -> t.getBytes(java.nio.charset.StandardCharsets.UTF_8).length <= 5);
        if (name < 1 || name > 8 || m.getBlob().size() > 16 || m.getCountsCount() > 4 || m.getTagsCount() > 3
            || !tagsOk || m.getLabelsCount() > 2 || code < 2 || code > 4
            || (m.hasNickname() && m.getNicknameBytes().size() > 6)) {
            throw new AssertionError("validateSizes accepted out-of-bounds sizes: " + m);
        }
        roundTrip(m, Kitchensink.Sizes.parseFrom(m.toByteArray()));
    };

    static final Target REQUESTS = data -> {
        try {
            Kitchensink.GetBoundsRequest m = Kitchensink.GetBoundsRequest.parseFrom(data);
            accepts(() -> Helpers.validateGetBoundsRequest(m));
            roundTrip(m, Kitchensink.GetBoundsRequest.parseFrom(m.toByteArray()));
        } catch (InvalidProtocolBufferException e) {
            // not a GetBoundsRequest
        }
        try {
            Kitchensink.Formats m = Kitchensink.Formats.parseFrom(data);
            accepts(() -> Helpers.validateFormats(m));
        } catch (InvalidProtocolBufferException e) {
            // not a Formats
        }
        try {
            Kitchensink.Conditional m = Kitchensink.Conditional.parseFrom(data);
            accepts(() -> Helpers.validateConditional(m));
        } catch (InvalidProtocolBufferException e) {
            // not a Conditional
        }
        try {
            Kitchensink.RepeatedScalars m = Kitchensink.RepeatedScalars.parseFrom(data);
            accepts(() -> Helpers.validateRepeatedScalars(m));
        } catch (InvalidProtocolBufferException e) {
            // not a RepeatedScalars
        }
        try {
            Kitchensink.Maps m = Kitchensink.Maps.parseFrom(data);
            accepts(() -> Helpers.validateMaps(m));
        } catch (InvalidProtocolBufferException e) {
            // not a Maps
        }
    };

    // Seeds: valid messages and the boundary values of every bound, unsigned
    // ones included.
    static List<byte[]> seeds() {
        List<byte[]> out = new ArrayList<>();
        out.add(new byte[0]);
        out.add(Kitchensink.Bounds.newBuilder().setPort(443).setGtZero(1).setLtHundred(99).setLeThousand(1000)
            .setWindow(64).setOneOf(8).setModeValue(1).build().toByteArray());
        out.add(Kitchensink.Bounds.newBuilder().setGtZero(1).setLtHundred(-268435456).setLeThousand(-1L)
            .setWindow(1).setOneOf(1).setModeValue(2).build().toByteArray());
        out.add(Kitchensink.Sizes.newBuilder().setName("n").setCode("ab").addCounts(1).build().toByteArray());
        out.add(Kitchensink.GetBoundsRequest.newBuilder().setDeep(Kitchensink.Deep.newBuilder()
            .setRoot(Kitchensink.Level1.newBuilder().setInner(Kitchensink.Level2.newBuilder()
                .setLeaf(Kitchensink.Level3.newBuilder().setValue(1))))).build().toByteArray());
        out.add(Kitchensink.Formats.newBuilder().setEmail("a@b.io").setFqdn("example.com").setHost("10.0.0.1")
            .setVersion("1.2.3").setListen(":8080").setEncoded("YQ==").build().toByteArray());
        return out;
    }

    static byte[] mutate(byte[] in, Random r) {
        byte[] b = in.clone();
        int edits = 1 + r.nextInt(4);
        for (int i = 0; i < edits; i++) {
            switch (r.nextInt(4)) {
                case 0 -> {
                    if (b.length > 0) b[r.nextInt(b.length)] = (byte) r.nextInt(256);
                }
                case 1 -> {
                    int at = r.nextInt(b.length + 1);
                    byte[] n = new byte[b.length + 1];
                    System.arraycopy(b, 0, n, 0, at);
                    n[at] = (byte) r.nextInt(256);
                    System.arraycopy(b, at, n, at + 1, b.length - at);
                    b = n;
                }
                case 2 -> {
                    if (b.length > 0) {
                        int at = r.nextInt(b.length);
                        byte[] n = new byte[b.length - 1];
                        System.arraycopy(b, 0, n, 0, at);
                        System.arraycopy(b, at + 1, n, at, b.length - at - 1);
                        b = n;
                    }
                }
                default -> {
                    byte[] n = new byte[r.nextInt(32)];
                    r.nextBytes(n);
                    b = n;
                }
            }
        }
        return b;
    }

    public static void main(String[] args) throws Exception {
        long seconds = args.length > 0 ? Long.parseLong(args[0]) : 10;
        long seed = args.length > 1 ? Long.parseLong(args[1]) : System.nanoTime();
        Random r = new Random(seed);
        List<byte[]> corpus = seeds();
        Target[] targets = {BOUNDS, SIZES, REQUESTS};
        String[] names = {"bounds", "sizes", "requests"};
        long deadline = System.nanoTime() + seconds * 1_000_000_000L;
        long runs = 0;
        while (System.nanoTime() < deadline) {
            byte[] data = mutate(corpus.get(r.nextInt(corpus.size())), r);
            for (int t = 0; t < targets.length; t++) {
                try {
                    targets[t].run(data);
                } catch (InvalidProtocolBufferException e) {
                    // does not decode: nothing to check
                } catch (Throwable e) {
                    StringBuilder hex = new StringBuilder();
                    for (byte x : data) hex.append(String.format("%02x", x));
                    System.err.println("fuzz: " + names[t] + " failed (seed " + seed + ") on input " + hex + ": " + e);
                    e.printStackTrace();
                    System.exit(1);
                }
            }
            if (runs++ % 64 == 0 && corpus.size() < 4096) {
                corpus.add(data);
            }
        }
        System.out.println("fuzz: java: " + runs + " inputs, 3 targets, " + seconds + "s, seed " + seed + ": OK");
    }
}
