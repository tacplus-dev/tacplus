// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ES512 (curve P-521) is the one supported EC algorithm whose DER-encoded
// signature SEQUENCE exceeds 127 bytes, so it is the one that needs DER's
// long-form length encoding. keyBytes is 66, r and s each encode to 65 or 66
// content bytes, so seqLen = 2+rDerLen+2+sDerLen lands at 134..136 — always
// over the short form's 127 ceiling. ES256 (keyBytes 32, seqLen <= 70) and
// ES384 (keyBytes 48, seqLen <= 102) stay inside it.

// javaMethodSource lifts a whole method out of a chassis template by its
// signature line and brace-matches to its close, so the harness below compiles
// the SAME text tacplus emits rather than a restatement of it.
func javaMethodSource(t *testing.T, src, signature string) string {
	t.Helper()
	start := strings.Index(src, signature)
	if start < 0 {
		t.Fatalf("template no longer contains %q", signature)
	}
	depth := 0
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	t.Fatalf("unbalanced braces after %q", signature)
	return ""
}

// The executable control. A real P-521 keypair signs, the template's derToJws
// turns the JDK's DER into the r||s a token carries, the template's jwsToDer
// turns it back, and the JDK verifies it. Anything but "verified on every
// round trip" means Java cannot check an ES512 token it just signed.
func TestJavaTokenWrapperES512SignatureRoundTripsThroughDer(t *testing.T) {
	if _, err := exec.LookPath("javac"); err != nil {
		t.Skip("javac not installed; skipping the executable ES512 DER control")
	}
	tmpl := readJavaTemplate(t, "java/chassis/token/TokenWrapper.java.tmpl")
	derToJws := javaMethodSource(t, tmpl, "private static byte[] derToJws(byte[] der, int keyBytes) {")
	jwsToDer := javaMethodSource(t, tmpl, "private static byte[] jwsToDer(byte[] jws) {")

	dir := t.TempDir()
	program := `import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.MessageDigest;
import java.security.SecureRandom;
import java.security.Signature;
import java.security.SignatureException;
import java.security.interfaces.ECPrivateKey;
import java.security.spec.ECGenParameterSpec;

public class Es512Der {
    ` + derToJws + `

    ` + jwsToDer + `

    public static void main(String[] args) throws Exception {
        KeyPairGenerator gen = KeyPairGenerator.getInstance("EC");
        gen.initialize(new ECGenParameterSpec(args[1]));
        SecureRandom rand = new SecureRandom();
        int rounds = Integer.parseInt(args[0]);
        int verified = 0, rejected = 0, threw = 0, longForm = 0, minSeq = Integer.MAX_VALUE, maxSeq = 0;
        String firstFailure = "";
        for (int i = 0; i < rounds; i++) {
            KeyPair kp = gen.generateKeyPair();
            ECPrivateKey priv = (ECPrivateKey) kp.getPrivate();
            int keyBytes = (priv.getParams().getOrder().bitLength() + 7) / 8;
            byte[] data = new byte[64];
            rand.nextBytes(data);
            byte[] hash = MessageDigest.getInstance("SHA-512").digest(data);

            Signature signer = Signature.getInstance("NONEwithECDSA");
            signer.initSign(priv);
            signer.update(hash);
            byte[] jws = derToJws(signer.sign(), keyBytes);
            byte[] der = jwsToDer(jws);

            boolean isLong = (der[1] & 0xff) > 127;
            if (isLong) longForm++;
            int seqLen = der.length - (isLong ? 3 : 2);
            minSeq = Math.min(minSeq, seqLen);
            maxSeq = Math.max(maxSeq, seqLen);

            Signature verifier = Signature.getInstance("NONEwithECDSA");
            verifier.initVerify(kp.getPublic());
            verifier.update(hash);
            try {
                if (verifier.verify(der)) {
                    verified++;
                } else {
                    rejected++;
                    if (firstFailure.isEmpty()) firstFailure = "returned false, der[0..2]=" + hex(der, 3);
                }
            } catch (SignatureException e) {
                threw++;
                if (firstFailure.isEmpty()) firstFailure = e.getClass().getName() + ": " + e.getMessage() + ", der[0..2]=" + hex(der, 3);
            }
        }
        System.out.println("rounds=" + rounds + " verified=" + verified + " rejected=" + rejected
            + " threw=" + threw + " longForm=" + longForm + " seqLen=" + minSeq + ".." + maxSeq);
        if (!firstFailure.isEmpty()) System.out.println("firstFailure: " + firstFailure);
    }

    private static String hex(byte[] b, int n) {
        StringBuilder sb = new StringBuilder();
        for (int i = 0; i < n && i < b.length; i++) sb.append(String.format("%02x ", b[i]));
        return sb.toString().trim();
    }
}
`
	src := filepath.Join(dir, "Es512Der.java")
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatalf("write Es512Der.java: %v", err)
	}
	if out, err := exec.Command("javac", "-d", dir, src).CombinedOutput(); err != nil {
		t.Fatalf("javac on the template's DER helpers failed: %v\n%s\n--- program ---\n%s", err, out, program)
	}

	const rounds = 50
	for _, tc := range []struct {
		alg, curve   string
		wantLongForm int
	}{
		// ES512 is the case that was broken: every one of its round trips must
		// exercise the long form, or the control proves nothing.
		{"ES512", "secp521r1", rounds},
		// Paired controls. The other two curves stay under 128 bytes, so they
		// must still take the short form — the fix did not trade one algorithm
		// for the other two.
		{"ES384", "secp384r1", 0},
		{"ES256", "secp256r1", 0},
	} {
		out, err := exec.Command("java", "-cp", dir, "Es512Der", fmt.Sprint(rounds), tc.curve).CombinedOutput()
		if err != nil {
			t.Fatalf("running the %s round trip: %v\n%s", tc.alg, err, out)
		}
		got := string(out)
		t.Logf("%s round trip: %s", tc.alg, strings.TrimSpace(got))
		if !strings.Contains(got, fmt.Sprintf("verified=%d rejected=0 threw=0", rounds)) {
			t.Fatalf("every %s signature must survive derToJws -> jwsToDer -> verify; got:\n%s", tc.alg, got)
		}
		if !strings.Contains(got, fmt.Sprintf("longForm=%d ", tc.wantLongForm)) {
			t.Fatalf("%s must take the DER long form on %d of %d round trips; got:\n%s",
				tc.alg, tc.wantLongForm, rounds, got)
		}
	}
}
