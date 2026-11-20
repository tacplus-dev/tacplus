// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package com.example.grid.model.chassis.memberport;

import static org.junit.jupiter.api.Assertions.fail;

import com.example.grid.model.chassis.jwk.ImmutableJwk;
import com.example.grid.model.chassis.key.ImmutableKey;
import com.example.grid.model.chassis.token.TokenWrapper;
import com.example.grid.model.types.sys.key.KeyOuterClass;
import java.io.ByteArrayInputStream;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.security.KeyFactory;
import java.security.PrivateKey;
import java.security.cert.CertificateFactory;
import java.security.cert.X509Certificate;
import java.security.spec.PKCS8EncodedKeySpec;
import java.util.ArrayList;
import java.util.Base64;
import java.util.Collections;
import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.logging.Handler;
import java.util.logging.LogRecord;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.condition.EnabledIfEnvironmentVariable;

/**
 * The Java half of the Go &lt;-&gt; Java member port interop leg (tacplus#322):
 * cross-test/memberport/run.sh --language go --interop runs it beside the Go
 * member port's memberport_interop_test.go, both pointed at
 * MEMBERPORT_INTEROP_DIR. The Go side writes the one fake fleet there; this
 * side reads it, and a Java member port and a Go member port tunnel to each
 * other both ways at once on 6-second tokens, so each session rotates several
 * times, and every byte must arrive intact both ways.
 *
 * <p>The leg is bounded by its bytes, not by seconds (#919): each stream is a
 * fixed count, cut only when it stops moving (the peer's stall bound). Neither
 * side takes its member port down before the other is done: a side whose own
 * direction fails says so in its done file ("ok", or its failures, with what
 * this member port logged) and keeps serving the other's session, so each side
 * reports its own result, not a reset. Every wait after the streams start has
 * an explicit deadline, named when it passes.
 */
class MemberPortInteropTest {
    static final String SVC = MemberPortBehaviorTest.SVC;
    static final String X = MemberPortBehaviorTest.X;
    /** Each wait's after the streams start. Generous: the streams take seconds. */
    static final long DEADLINE_MS = 300_000;

    static String waitFile(Path dir, String name, long timeoutMillis) throws Exception {
        String got = readFile(dir, name, timeoutMillis);
        if (got == null) {
            throw new AssertionError("timed out waiting for " + name + " from the Go side");
        }
        return got;
    }

    /** The other side's file {@code name}, trimmed, or null once the deadline has passed. */
    static String readFile(Path dir, String name, long timeoutMillis) throws Exception {
        long deadline = System.currentTimeMillis() + timeoutMillis;
        Path p = dir.resolve(name);
        while (!Files.exists(p)) {
            if (System.currentTimeMillis() > deadline) {
                return null;
            }
            Thread.sleep(50);
        }
        return Files.readString(p, StandardCharsets.UTF_8).trim();
    }

    static void writeFile(Path dir, String name, String content) throws Exception {
        Path tmp = dir.resolve(name + ".java.tmp");
        Files.writeString(tmp, content, StandardCharsets.UTF_8);
        Files.move(tmp, dir.resolve(name), StandardCopyOption.ATOMIC_MOVE);
    }

    static PrivateKey pkcs8(String pem) throws Exception {
        String b64 = pem.replaceAll("-----[A-Z ]+-----", "").replaceAll("\\s", "");
        return KeyFactory.getInstance("EC").generatePrivate(new PKCS8EncodedKeySpec(Base64.getDecoder().decode(b64)));
    }

    @Test
    @EnabledIfEnvironmentVariable(named = "MEMBERPORT_INTEROP_DIR", matches = ".+")
    void javaAndGoMemberPortsTunnelToEachOther() throws Exception {
        Path dir = Path.of(System.getenv("MEMBERPORT_INTEROP_DIR"));
        waitFile(dir, "fleet.ready", 600_000);
        List<String> k = Files.readAllLines(dir.resolve("kernel.key"), StandardCharsets.UTF_8);
        ImmutableKey kernel = ImmutableKey.from(KeyOuterClass.Key.newBuilder()
                .setId(k.get(0).trim())
                .setBase64(k.get(1).trim())
                .setCreated(System.currentTimeMillis() / 1000)
                .build());
        ImmutableJwk jwk = ImmutableJwk.from(MemberPortBehaviorTest.jwkFor(kernel));
        String kid = kernel.getProto().getId();
        CertificateFactory cf = CertificateFactory.getInstance("X.509");
        X509Certificate ca = (X509Certificate) cf.generateCertificate(new ByteArrayInputStream(Files.readAllBytes(dir.resolve("ca.pem"))));
        X509Certificate[] chain = cf.generateCertificates(new ByteArrayInputStream(Files.readAllBytes(dir.resolve("java-leaf.pem"))))
                .toArray(new X509Certificate[0]);
        MemberPort.Material leaf = new MemberPort.Material(chain, pkcs8(Files.readString(dir.resolve("java-leaf.key"))));

        // What this member port logs (its refusals and dropped sessions, with the
        // reason) goes into a failure's report, here and on the other side.
        List<String> memberPortLog = Collections.synchronizedList(new ArrayList<>());
        Handler logHandler = new Handler() {
            @Override
            public void publish(LogRecord r) {
                memberPortLog.add(r.getLevel() + " " + r.getMessage());
            }

            @Override
            public void flush() {}

            @Override
            public void close() {}
        };
        MemberPortBehaviorTest.MEMBER_PORT_LOG.addHandler(logHandler);

        // 6-second tokens renewed with 4 seconds left, signed with the fleet's
        // kernel key: a cutover has a second or more before the old stream's
        // exp, as in the behaviour tests (#737).
        AtomicInteger mints = new AtomicInteger();
        String[] cached = new String[1];
        MemberPort.TokenSource tokens = () -> {
            synchronized (cached) {
                if (cached[0] == null || TokenWrapper.parse(cached[0]).getExpiration() - System.currentTimeMillis() / 1000 <= 4) {
                    TokenWrapper tok = TokenWrapper.create()
                            .setIssuer("kernel")
                            .setAudience(MemberPort.audienceFor(SVC))
                            .setSubType("service")
                            .setSubject(SVC)
                            .setNamespace(X)
                            .setExpiration(System.currentTimeMillis() / 1000 + 6);
                    tok.sign(kernel);
                    cached[0] = tok.getBase64();
                    mints.incrementAndGet();
                }
                return cached[0];
            }
        };
        List<String> failures = new ArrayList<>();
        try (MemberPort j = new MemberPort(SVC, MemberPortBehaviorTest.TD, List.of(ca), tokens,
                id -> kid.equals(id) ? jwk : null, () -> leaf);
                // Behind the Java port: pushes seed 31, expects the Go client's seed 32.
                MemberportPeer.Server server = new MemberportPeer.Server(MemberportPeer.Mode.PUSH, 31, 1_600_000, 32, 4096, 20)) {
            InetSocketAddress jAddr = j.listen(new InetSocketAddress(InetAddress.getLoopbackAddress(), 0), server.address());
            writeFile(dir, "java.port", jAddr.getAddress().getHostAddress() + ":" + jAddr.getPort() + "\n");
            String goAddr = waitFile(dir, "go.port", 600_000);
            int colon = goAddr.lastIndexOf(':');
            // Java client -> Java port -> Go port -> Go peer server (pushes 41, expects 42).
            try {
                InetSocketAddress fwd = j.forward(new InetSocketAddress(goAddr.substring(0, colon), Integer.parseInt(goAddr.substring(colon + 1))));
                long start = System.currentTimeMillis();
                MemberportPeer.Result r = MemberportPeer.exchange(fwd, 42, 1_600_000, 41, 4096, 20, 0);
                long elapsed = System.currentTimeMillis() - start;
                if (!r.ok(1_600_000)) {
                    failures.add("Java forward -> Go listen: no loss, duplication or reordering: " + r + " after " + elapsed + "ms");
                } else if (elapsed <= 12_500) {
                    failures.add("the session must outlive two token lifetimes, took " + elapsed + "ms");
                }
                if (mints.get() < 3) {
                    failures.add("the Java client rotated onto new tokens: " + mints.get() + " mints");
                }
            } catch (Exception e) {
                failures.add("Java forward -> Go listen: " + e);
            }
            // Go client -> Go port -> Java port -> this peer server.
            long until = System.currentTimeMillis() + DEADLINE_MS;
            while (server.results.isEmpty() && System.currentTimeMillis() < until) {
                Thread.sleep(20);
            }
            MemberportPeer.Result sr = server.results.get(1);
            if (sr == null) {
                failures.add("Go forward -> Java listen: no session ended within the Java side's deadline of " + DEADLINE_MS + "ms");
            } else if (!sr.ok(1_600_000)) {
                failures.add("Go forward -> Java listen: " + sr);
            }
            if (server.accepted.get() != 1) {
                failures.add("one byte session from Go: the target was dialled " + server.accepted.get() + " times");
            }
            if (!failures.isEmpty()) {
                synchronized (memberPortLog) {
                    failures.add("the Java member port logged: " + (memberPortLog.isEmpty() ? "nothing" : String.join("; ", memberPortLog)));
                }
            }
            writeFile(dir, "java.done", failures.isEmpty() ? "ok\n" : String.join("\n", failures) + "\n");
            String other = readFile(dir, "go.done", DEADLINE_MS);
            if (!failures.isEmpty()) {
                fail(String.join("\n", failures) + "\nthe other side reported: "
                        + (other == null ? "nothing: it was not done within the Java side's deadline of " + DEADLINE_MS + "ms" : other));
            }
        } finally {
            MemberPortBehaviorTest.MEMBER_PORT_LOG.removeHandler(logHandler);
        }
    }
}
