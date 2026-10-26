// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/tacplus-dev/tacplus/templates"
)

// javaClientIMStubs are minimal stand-ins for grpc-java and the generated
// proto classes the rendered ClientIdentityManager compiles against. The mint
// stub records every TokenMintRequest and can hold a call open on a latch.
var javaClientIMStubs = map[string]string{
	"io/grpc/Channel.java": "package io.grpc;\npublic abstract class Channel {}\n",
	"io/grpc/Status.java":  "package io.grpc;\npublic final class Status {\n  public enum Code { UNAUTHENTICATED }\n  public Code getCode() { return null; }\n}\n",
	"io/grpc/StatusRuntimeException.java": "package io.grpc;\npublic class StatusRuntimeException extends RuntimeException {\n" +
		"  public Status getStatus() { return null; }\n}\n",
	"demo/types/sys/token/TokenOuterClass.java": `package demo.types.sys.token;
import java.util.Map;
public final class TokenOuterClass {
  public enum TokenPurpose { TOKEN_PURPOSE_ACCESS }
  public static final class Token {
    private final String base64;
    private final Map<String, String> payload;
    public Token(String base64, Map<String, String> payload) { this.base64 = base64; this.payload = payload; }
    public String getBase64() { return base64; }
    public Map<String, String> getPayloadMap() { return payload; }
  }
}
`,
	"demo/types/sys/tokenrequest/Tokenrequest.java": `package demo.types.sys.tokenrequest;
import demo.types.sys.token.TokenOuterClass.Token;
import demo.types.sys.token.TokenOuterClass.TokenPurpose;
import java.util.ArrayList;
import java.util.List;
public final class Tokenrequest {
  public static final class TokenMintRequest {
    public String inputToken = "";
    public String tenantNamespace = "";
    public List<String> serviceAuds = new ArrayList<>();
    public static Builder newBuilder() { return new Builder(); }
    public static final class Builder {
      private final TokenMintRequest r = new TokenMintRequest();
      public Builder setInputToken(String v) { r.inputToken = v; return this; }
      public Builder setTokenPurpose(TokenPurpose v) { return this; }
      public Builder addAllServiceAuds(Iterable<String> v) { v.forEach(r.serviceAuds::add); return this; }
      public Builder setTenantNamespace(String v) { r.tenantNamespace = v; return this; }
      public TokenMintRequest build() { return r; }
    }
  }
  public static final class TokenMintResponse {
    public final Token access;
    public final List<Token> services;
    public TokenMintResponse(Token access, List<Token> services) { this.access = access; this.services = services; }
    public boolean hasAccessToken() { return access != null; }
    public Token getAccessToken() { return access; }
    public int getServiceTokensCount() { return services.size(); }
    public List<Token> getServiceTokensList() { return services; }
  }
}
`,
	"demo/types/services/gateway/MintGrpc.java": `package demo.types.services.gateway;
import demo.types.sys.token.TokenOuterClass.Token;
import demo.types.sys.tokenrequest.Tokenrequest.TokenMintRequest;
import demo.types.sys.tokenrequest.Tokenrequest.TokenMintResponse;
import io.grpc.Channel;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
public final class MintGrpc {
  public static final List<TokenMintRequest> REQUESTS = new CopyOnWriteArrayList<>();
  public static volatile CountDownLatch gate;
  private static int n = 0;
  public static Stub newBlockingStub(Channel c) { return new Stub(); }
  public static final class Stub {
    public Stub withDeadlineAfter(long d, TimeUnit u) { return this; }
    public TokenMintResponse mintToken(TokenMintRequest req) {
      REQUESTS.add(req);
      CountDownLatch g = gate;
      if (g != null) {
        try { g.await(); } catch (InterruptedException e) { throw new RuntimeException(e); }
      }
      int i;
      synchronized (MintGrpc.class) { i = ++n; }
      Map<String, String> p = Map.of("exp", Long.toString(System.currentTimeMillis() / 1000 + 3600));
      List<Token> svc = new ArrayList<>();
      for (String aud : req.serviceAuds) svc.add(new Token("ST" + i + ":" + aud, p));
      return new TokenMintResponse(new Token("AT" + i, p), svc);
    }
  }
}
`,
	"Driver.java": `import demo.chassis.client_im.ClientIdentityManager;
import demo.types.services.gateway.MintGrpc;
import demo.types.sys.tokenrequest.Tokenrequest.TokenMintRequest;
import java.util.List;
import java.util.concurrent.CountDownLatch;
public final class Driver {
  static void check(boolean ok, String msg) { if (!ok) throw new AssertionError(msg); }
  public static void main(String[] args) throws Exception {
    List<TokenMintRequest> reqs = MintGrpc.REQUESTS;
    ClientIdentityManager m = new ClientIdentityManager(null, null);
    m.setRefreshToken("RT");

    // Before setTenantNamespace: a plain personal exchange.
    check(m.getAccessToken().isEmpty(), "no access token before the first mint");
    check(m.getToken().equals("AT1"), "first access token");
    check(reqs.get(0).inputToken.equals("RT") && reqs.get(0).tenantNamespace.isEmpty(), "personal exchange must not carry tenant_namespace");
    check(m.getAccessToken().equals("AT1"), "getAccessToken returns the cached access token");
    m.getTokenFor("svc");
    check(reqs.get(1).inputToken.equals("AT1"), "service token minted from the access token");

    // Switching drops the cached access and service tokens.
    m.setTenantNamespace("/tenants/acme");
    check(m.getAccessToken().isEmpty(), "switch must drop the cached access token");
    check(m.getTokenFor("svc").equals("ST3:svc"), "switch must drop cached service tokens");
    check(reqs.get(2).inputToken.equals("RT") && reqs.get(2).tenantNamespace.equals("/tenants/acme"), "refresh -> access mint carries tenant_namespace");
    check(m.getAccessToken().equals("AT3"), "access token for the namespace");
    m.getTokenFor("other");
    check(reqs.get(3).inputToken.equals("AT3") && reqs.get(3).tenantNamespace.isEmpty(), "access -> service mints carry no tenant_namespace");

    // null returns to the plain personal exchange.
    m.setTenantNamespace(null);
    check(m.getAccessToken().isEmpty(), "switch back must drop the cached access token");
    check(m.getToken().equals("AT5"), "personal access token");
    check(reqs.get(4).inputToken.equals("RT") && reqs.get(4).tenantNamespace.isEmpty(), "null sends no tenant_namespace");

    // A mint in flight across a switch must not repopulate the cache.
    MintGrpc.gate = new CountDownLatch(1);
    int before = reqs.size();
    Thread t = new Thread(() -> { try { m.getTokenFor("svc2"); } catch (Exception e) { throw new RuntimeException(e); } });
    t.start();
    while (reqs.size() == before) Thread.sleep(1);
    m.setTenantNamespace("/tenants/beta");
    MintGrpc.gate.countDown();
    t.join();
    MintGrpc.gate = null;
    m.getTokenFor("svc2");
    TokenMintRequest last = reqs.get(reqs.size() - 1);
    check(last.inputToken.equals("RT") && last.tenantNamespace.equals("/tenants/beta"), "a pre-switch service token must not be cached after the switch");
    System.out.println("ok");
  }
}
`,
}

// TestJavaClientIdentityManagerTenantNamespace renders the real Java
// ClientIdentityManager, compiles it against stub grpc/proto classes and drives
// it: setTenantNamespace drops the cached tokens and the next refresh -> access
// mint carries tenant_namespace, and getAccessToken reads the cached access
// token without minting (#139).
func TestJavaClientIdentityManagerTenantNamespace(t *testing.T) {
	javac, err := exec.LookPath("javac")
	if err != nil {
		t.Skip("javac not installed; skipping ClientIdentityManager behaviour check")
	}
	java, err := exec.LookPath("java")
	if err != nil {
		t.Skip("java not installed; skipping ClientIdentityManager behaviour check")
	}
	content, err := fs.ReadFile(templates.FS, "java/chassis/client_im/ClientIdentityManager.java.tmpl")
	if err != nil {
		t.Fatalf("read ClientIdentityManager.java.tmpl: %v", err)
	}
	tmpl, err := template.New("cim").Parse(string(content))
	if err != nil {
		t.Fatalf("parse ClientIdentityManager template: %v", err)
	}
	data := map[string]any{
		"ChassisBase":                "demo.chassis",
		"TypesBase":                  "demo.types",
		"TokenRequestPackage":        "sys.tokenrequest",
		"TokenRequestOuterClass":     "Tokenrequest",
		"HasClientTokenExchanger":    true,
		"ClientMintExchangerDir":     "services",
		"ClientMintExchangerPackage": "gateway",
		"ClientMintServiceName":      "Mint",
		"ClientTokenMintMethod":      "MintToken",
		"ClientTokenMintMethodLower": "mintToken",
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, data); err != nil {
		t.Fatalf("execute ClientIdentityManager template: %v", err)
	}

	dir := t.TempDir()
	files := map[string]string{
		"demo/chassis/client_im/ClientIdentityManager.java": rendered.String(),
		"demo/chassis/client_im/TokenSource.java": "package demo.chassis.client_im;\n" +
			"public interface TokenSource {\n" +
			"  final class RefreshResult { public final String refreshToken = \"\"; public final String instanceId = \"\"; }\n" +
			"  RefreshResult acquire(io.grpc.Channel conn) throws Exception;\n" +
			"  boolean supportsAutoAcquire();\n}\n",
	}
	for k, v := range javaClientIMStubs {
		files[k] = v
	}
	var srcs []string
	for name, body := range files {
		path := filepath.Join(dir, "src", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		srcs = append(srcs, path)
	}
	classes := filepath.Join(dir, "classes")
	if out, err := exec.Command(javac, append([]string{"-d", classes}, srcs...)...).CombinedOutput(); err != nil {
		t.Fatalf("javac failed: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, java, "-cp", classes, "Driver").CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("driver did not finish within 30s")
	}
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("driver failed: %v\n%s", err, out)
	}
}
