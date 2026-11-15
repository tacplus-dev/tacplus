// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package com.example.grid.model.chassis.memberport;

import static org.junit.jupiter.api.Assertions.assertTrue;

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
import java.util.Base64;
import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.condition.EnabledIfEnvironmentVariable;

/**
 * The Java half of the Go &lt;-&gt; Java member port interop leg (tacplus#322):
 * cross-test/memberport/run.sh --language go --interop runs it beside the Go
 * member port's memberport_interop_test.go, both pointed at
 * MEMBERPORT_INTEROP_DIR. The Go side writes the one fake fleet there; this
 * side reads it, and a Java member port and a Go member port tunnel to each
 * other both ways at once on 3-second tokens, so each session rotates several
 * times, and every byte must arrive intact both ways.
 */
class MemberPortInteropTest {
    static final String SVC = MemberPortBehaviorTest.SVC;
    static final String X = MemberPortBehaviorTest.X;

    static String waitFile(Path dir, String name, long timeoutMillis) throws Exception {
        long deadline = System.currentTimeMillis() + timeoutMillis;
        Path p = dir.resolve(name);
        while (!Files.exists(p)) {
            if (System.currentTimeMillis() > deadline) {
                throw new AssertionError("timed out waiting for " + name + " from the Go side");
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

        // 3-second tokens renewed with 2 seconds left, signed with the fleet's kernel key.
        AtomicInteger mints = new AtomicInteger();
        String[] cached = new String[1];
        MemberPort.TokenSource tokens = () -> {
            synchronized (cached) {
                if (cached[0] == null || TokenWrapper.parse(cached[0]).getExpiration() - System.currentTimeMillis() / 1000 <= 2) {
                    TokenWrapper tok = TokenWrapper.create()
                            .setIssuer("kernel")
                            .setAudience(MemberPort.audienceFor(SVC))
                            .setSubType("service")
                            .setSubject(SVC)
                            .setNamespace(X)
                            .setExpiration(System.currentTimeMillis() / 1000 + 3);
                    tok.sign(kernel);
                    cached[0] = tok.getBase64();
                    mints.incrementAndGet();
                }
                return cached[0];
            }
        };
        try (MemberPort j = new MemberPort(SVC, MemberPortBehaviorTest.TD, List.of(ca), tokens,
                id -> kid.equals(id) ? jwk : null, () -> leaf);
                // Behind the Java port: pushes seed 31, expects the Go client's seed 32.
                MemberportPeer.Server server = new MemberportPeer.Server(MemberportPeer.Mode.PUSH, 31, 800_000, 32, 4096, 20)) {
            InetSocketAddress jAddr = j.listen(new InetSocketAddress(InetAddress.getLoopbackAddress(), 0), server.address());
            writeFile(dir, "java.port", jAddr.getAddress().getHostAddress() + ":" + jAddr.getPort() + "\n");
            String goAddr = waitFile(dir, "go.port", 600_000);
            int colon = goAddr.lastIndexOf(':');
            InetSocketAddress fwd = j.forward(new InetSocketAddress(goAddr.substring(0, colon), Integer.parseInt(goAddr.substring(colon + 1))));
            // Java client -> Java port -> Go port -> Go peer server (pushes 41, expects 42).
            long start = System.currentTimeMillis();
            MemberportPeer.Result r = MemberportPeer.exchange(fwd, 42, 800_000, 41, 4096, 20, 0);
            long elapsed = System.currentTimeMillis() - start;
            assertTrue(r.ok(800_000), "Java forward -> Go listen: no loss, duplication or reordering: " + r);
            assertTrue(elapsed > 6_500, "the session must outlive two token lifetimes, took " + elapsed + "ms");
            assertTrue(mints.get() >= 3, "the Java client rotated onto new tokens: " + mints.get() + " mints");
            // Go client -> Go port -> Java port -> this peer server.
            MemberPortBehaviorTest.waitFor("the Go client's session", 120_000, () -> server.results.size() == 1);
            assertTrue(server.results.get(1).ok(800_000), "Go forward -> Java listen: " + server.results.get(1));
            assertTrue(server.accepted.get() == 1, "one byte session from Go: the target was dialled " + server.accepted.get() + " times");
            writeFile(dir, "java.done", "1\n");
            waitFile(dir, "go.done", 120_000);
        }
    }
}
