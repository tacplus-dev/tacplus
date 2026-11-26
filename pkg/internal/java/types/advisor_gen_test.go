// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// javaAdvisorFixture lays out the protos of zerorpc (an S2S client to
// upstreamllm.LlmProxy, the service whose (spec.service).name is llmproxy, as
// in cross-test/proto-zerorpc), and returns the config of a zerorpc that
// declares its advisor at src/main/java/example/zerorpc/advisor, with the
// generated client the generator hands the advisor.
func javaAdvisorFixture(t *testing.T) (*tactype.Config, string, []generatedGrpcClientJava) {
	t.Helper()
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	project := t.TempDir()
	protoDir := filepath.Join(t.TempDir(), "proto")
	specProto, err := os.ReadFile("../../../proto/sys/spec.proto")
	if err != nil {
		t.Fatal(err)
	}
	for rel, text := range map[string]string{
		"sys/spec.proto": string(specProto),
		"services/llmproxy.proto": `syntax = "proto3";
package upstreamllm;
import "sys/spec.proto";
option (spec.service) = {name: "llmproxy"};
message CompleteRequest { string prompt = 1; string trace_id = 200; }
message CompleteResponse { string text = 1; }
message Chunk { string text = 1; }
service LlmProxy {
  option (spec.service_s2s) = {};
  rpc Generate(CompleteRequest) returns (CompleteResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["*"]};
  }
  rpc GenerateStream(CompleteRequest) returns (stream Chunk) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["*"]};
  }
}
`,
	} {
		path := filepath.Join(protoDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	config := &tactype.Config{
		OutputDir:       filepath.Join(project, "model"),
		ProjectDir:      project,
		JavaPackageBase: "example.zerorpc.model",
		SmPrefix:        "s2sm",
		AdvisorDir:      filepath.Join(project, "src", "main", "java", "example", "zerorpc", "advisor"),
		Language:        "java",
	}
	clients := []generatedGrpcClientJava{{
		PackageName:  "upstreamllm",
		ServiceName:  "LlmProxy",
		ClassName:    "LlmProxyGrpcClient",
		ParentDir:    "services",
		FullPackage:  "example.zerorpc.model.s2sm.internal.grpc.clients.services.upstreamllm",
		ProtoFile:    "services/llmproxy.proto",
		TypesPackage: "example.zerorpc.model.types.services.upstreamllm",
		Methods: []javaResolvedGrpcMethod{
			{method: &parser.MethodInfo{Name: "Generate", FullInputType: "upstreamllm.CompleteRequest", FullOutputType: "upstreamllm.CompleteResponse"}, inputType: "Upstreamllm.CompleteRequest", outputType: "Upstreamllm.CompleteResponse"},
			{method: &parser.MethodInfo{Name: "GenerateStream", ServerStreaming: true, FullInputType: "upstreamllm.CompleteRequest", FullOutputType: "upstreamllm.Chunk"}, inputType: "Upstreamllm.CompleteRequest", outputType: "Upstreamllm.Chunk"},
		},
		GroupAccessor:  "services",
		ClientAccessor: "llmProxy",
	}}
	return config, protoDir, clients
}

func readJavaGenerated(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func javaPortFile(config *tactype.Config, name string) string {
	return filepath.Join(config.OutputDir, "src", "main", "java", "example", "zerorpc", "model", "advisor", name)
}

func TestJavaAdvisorPackage(t *testing.T) {
	for _, tc := range []struct{ rel, want string }{
		{"src/main/java/example/zerorpc/advisor", "example.zerorpc.advisor"},
		{"advisor/src/main/java/com/example/skipper/advisor", "com.example.skipper.advisor"},
	} {
		got, err := javaAdvisorPackage(filepath.FromSlash(tc.rel), "example.zerorpc.model")
		if err != nil || got != tc.want {
			t.Errorf("javaAdvisorPackage(%q) = %q, %v; want %q", tc.rel, got, err, tc.want)
		}
	}
	for _, tc := range []struct{ rel, want string }{
		// The gate finds the advisor by its package: the path must name one.
		{"advisor", "a package directory under src/main/java"},
		{"src/main/java", "a package directory under src/main/java"},
		{"src/main/java/example/1advisor", "not a Java package"},
		// The generated tree is the service's: the advisor's package is not in it.
		{"src/main/java/example/zerorpc/model", "inside the generated package"},
		{"src/main/java/example/zerorpc/model/advisor", "inside the generated package"},
		{"src/main/java/example/zerorpc", "contains the generated package"},
	} {
		_, err := javaAdvisorPackage(filepath.FromSlash(tc.rel), "example.zerorpc.model")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("javaAdvisorPackage(%q) = %v, want %q", tc.rel, err, tc.want)
		}
	}
}

// TestJavaAdvisorPortIsTypedOnTheModelServicesMessages: ModelPort has one
// method per unary rpc of llmproxy, typed on llmproxy's message classes, and
// never names the raw transport stub beside them.
func TestJavaAdvisorPortIsTypedOnTheModelServicesMessages(t *testing.T) {
	config, protoDir, clients := javaAdvisorFixture(t)
	if err := generateJavaAdvisorPort(config, protoDir, clients); err != nil {
		t.Fatalf("generateJavaAdvisorPort: %v", err)
	}
	port := readJavaGenerated(t, javaPortFile(config, "ModelPort.java"))
	for _, want := range []string{
		"package example.zerorpc.model.advisor;",
		"import example.zerorpc.model.types.services.upstreamllm.Upstreamllm;",
		"public interface ModelPort {",
		"Upstreamllm.CompleteResponse generate(String traceId, Upstreamllm.CompleteRequest request);",
	} {
		if !strings.Contains(port, want) {
			t.Errorf("ModelPort.java lacks %q:\n%s", want, port)
		}
	}
	for _, stub := range []string{"LlmProxyGrpc", "io.grpc", "generateStream", "import example.zerorpc.model.s2sm"} {
		if strings.Contains(port, stub) {
			t.Errorf("ModelPort.java reaches %s:\n%s", stub, port)
		}
	}
	exc := readJavaGenerated(t, javaPortFile(config, "ModelPortException.java"))
	if !strings.Contains(exc, "public final class ModelPortException extends RuntimeException") || !strings.Contains(exc, "public int getCode()") {
		t.Errorf("ModelPortException.java:\n%s", exc)
	}
}

// TestJavaAdvisorAdapterCarriesTheTraceAndTheServiceToken: the core's adapter
// calls the generated S2S client (which attaches the service's token) with a
// copy of the request carrying the advisor's trace_id (field 200).
func TestJavaAdvisorAdapterCarriesTheTraceAndTheServiceToken(t *testing.T) {
	config, protoDir, clients := javaAdvisorFixture(t)
	if err := generateJavaAdvisorPort(config, protoDir, clients); err != nil {
		t.Fatalf("generateJavaAdvisorPort: %v", err)
	}
	adapter := readJavaGenerated(t, filepath.Join(config.OutputDir, "src", "main", "java", "example", "zerorpc", "model", "s2sm", "ModelPortAdapter.java"))
	for _, want := range []string{
		"package example.zerorpc.model.s2sm;",
		"import example.zerorpc.model.advisor.ModelPort;",
		"public final class ModelPortAdapter implements ModelPort {",
		"public ModelPortAdapter(ServiceManager sm)",
		".setTraceId(traceId == null ? \"\" : traceId)",
		"return sm.getGrpcClients().services().llmProxy().generate(out);",
		"throw new ModelPortException(e.getCode(), e.getMessage(), e);",
	} {
		if !strings.Contains(adapter, want) {
			t.Errorf("ModelPortAdapter.java lacks %q:\n%s", want, adapter)
		}
	}
}

// TestJavaAdvisorGateIsGenerated: the gate (LlmRule, in the port's package)
// knows the advisor's package, and a JUnit test runs it.
func TestJavaAdvisorGateIsGenerated(t *testing.T) {
	config, protoDir, clients := javaAdvisorFixture(t)
	if err := generateJavaAdvisorPort(config, protoDir, clients); err != nil {
		t.Fatalf("generateJavaAdvisorPort: %v", err)
	}
	rule := readJavaGenerated(t, javaPortFile(config, "LlmRule.java"))
	for _, want := range []string{
		"public final class LlmRule {",
		`static final String ADVISOR_PACKAGE = "example.zerorpc.advisor";`,
		`ToolProvider.findFirst("jdeps")`,
	} {
		if !strings.Contains(rule, want) {
			t.Errorf("LlmRule.java lacks %q", want)
		}
	}
	test := readJavaGenerated(t, filepath.Join(config.OutputDir, "src", "test", "java", "example", "zerorpc", "model", "advisor", "LlmRuleTest.java"))
	for _, want := range []string{"package example.zerorpc.model.advisor;", "@Test", "LlmRule.check();"} {
		if !strings.Contains(test, want) {
			t.Errorf("LlmRuleTest.java lacks %q:\n%s", want, test)
		}
	}
}

// TestJavaAdvisorRefusesAServiceWithoutTheModelClient: no S2S client to
// llmproxy, no model to reach; generation says how to give it one.
func TestJavaAdvisorRefusesAServiceWithoutTheModelClient(t *testing.T) {
	config, protoDir, _ := javaAdvisorFixture(t)
	err := generateJavaAdvisorPort(config, protoDir, nil)
	if err == nil || !strings.Contains(err.Error(), "add llmproxy to depends") {
		t.Fatalf("generateJavaAdvisorPort without the llmproxy client = %v", err)
	}
}

// TestJavaAdvisorGate compiles the generated port and LlmRule beside
// stand-ins for the rest of a service (the message classes and the raw stub
// beside them, the ServiceManager, the token chassis, the core) and an
// advisor, and runs LlmRule as its test would: green when the advisor
// references only the port and its messages, red, naming the reference, for
// each other part of the service it reaches, from its main or its test
// classes.
func TestJavaAdvisorGate(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs Java")
	}
	for _, tool := range []string{"javac", "java", "jdeps"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	config, protoDir, clients := javaAdvisorFixture(t)
	if err := generateJavaAdvisorPort(config, protoDir, clients); err != nil {
		t.Fatalf("generateJavaAdvisorPort: %v", err)
	}
	gen := filepath.Join(config.OutputDir, "src", "main", "java")
	// The adapter needs the real ServiceManager and client; the gate does not build them.
	if err := os.Remove(filepath.Join(gen, "example", "zerorpc", "model", "s2sm", "ModelPortAdapter.java")); err != nil {
		t.Fatal(err)
	}
	project := config.ProjectDir
	write := func(rel, text string) {
		t.Helper()
		path := filepath.Join(project, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("model/src/main/java/example/zerorpc/model/types/services/upstreamllm/Upstreamllm.java", `package example.zerorpc.model.types.services.upstreamllm;

import example.zerorpc.model.types.sys.tokenrequest.Tokenrequest;

public final class Upstreamllm {
    static final Object DESCRIPTOR = Tokenrequest.DESCRIPTOR;
    public static final class CompleteRequest {
        public static CompleteRequest getDefaultInstance() { return new CompleteRequest(); }
        public String getPrompt() { return ""; }
    }
    public static final class CompleteResponse {
        public String getText() { return ""; }
    }
}
`)
	// Imported by llmproxy's proto file: the message classes reach it, so the advisor may too.
	write("model/src/main/java/example/zerorpc/model/types/sys/tokenrequest/Tokenrequest.java", `package example.zerorpc.model.types.sys.tokenrequest;

public final class Tokenrequest {
    public static final Object DESCRIPTOR = new Object();
}
`)
	write("model/src/main/java/example/zerorpc/model/types/services/upstreamllm/LlmProxyGrpc.java", "package example.zerorpc.model.types.services.upstreamllm;\n\npublic final class LlmProxyGrpc {}\n")
	write("model/src/main/java/example/zerorpc/model/s2sm/ServiceManager.java", "package example.zerorpc.model.s2sm;\n\npublic final class ServiceManager {}\n")
	write("model/src/main/java/example/zerorpc/model/chassis/token/Token.java", "package example.zerorpc.model.chassis.token;\n\npublic final class Token {}\n")
	write("src/main/java/example/zerorpc/Core.java", "package example.zerorpc;\n\npublic final class Core {}\n")
	write("src/main/java/example/zerorpc/advisor/Advisor.java", `package example.zerorpc.advisor;

import example.zerorpc.model.advisor.ModelPort;
import example.zerorpc.model.advisor.ModelPortException;
import example.zerorpc.model.types.services.upstreamllm.Upstreamllm;

public final class Advisor {
    private final ModelPort model;

    public Advisor(ModelPort model) { this.model = model; }

    public String ask(String traceId) {
        try {
            return model.generate(traceId, Upstreamllm.CompleteRequest.getDefaultInstance()).getText();
        } catch (ModelPortException e) {
            return "";
        }
    }
}
`)
	classes := filepath.Join(project, "target", "classes")
	testClasses := filepath.Join(project, "target", "test-classes")
	run := func() (string, bool) {
		t.Helper()
		for _, dir := range []string{classes, testClasses} {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		}
		compile := func(out string, roots ...string) {
			var srcs []string
			for _, root := range roots {
				_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
					if err == nil && strings.HasSuffix(path, ".java") {
						srcs = append(srcs, path)
					}
					return nil
				})
			}
			if len(srcs) == 0 {
				return
			}
			args := append([]string{"-d", out, "-cp", classes}, srcs...)
			if out, err := exec.Command("javac", args...).CombinedOutput(); err != nil {
				t.Fatalf("javac: %v\n%s", err, out)
			}
		}
		compile(classes, gen, filepath.Join(project, "src", "main", "java"))
		compile(testClasses, filepath.Join(project, "src", "test", "java"))
		out, err := exec.Command("java", "-cp", classes, "example.zerorpc.model.advisor.LlmRule").CombinedOutput()
		return string(out), err == nil
	}

	if out, ok := run(); !ok || !strings.Contains(out, "LlmRule: all checks passed") {
		t.Fatalf("green: the gate failed an advisor that references only the port and its messages:\n%s", out)
	}

	for _, tc := range []struct{ name, file, text, want string }{
		{"the ServiceManager", "src/main/java/example/zerorpc/advisor/Sm.java",
			"package example.zerorpc.advisor;\n\nfinal class Sm { example.zerorpc.model.s2sm.ServiceManager sm; }\n",
			"example.zerorpc.advisor.Sm references example.zerorpc.model.s2sm.ServiceManager"},
		{"the raw transport stub", "src/main/java/example/zerorpc/advisor/Stub.java",
			"package example.zerorpc.advisor;\n\nfinal class Stub { Object s = new example.zerorpc.model.types.services.upstreamllm.LlmProxyGrpc(); }\n",
			"references example.zerorpc.model.types.services.upstreamllm.LlmProxyGrpc"},
		{"the core, from a subpackage", "src/main/java/example/zerorpc/advisor/tools/Tools.java",
			"package example.zerorpc.advisor.tools;\n\nfinal class Tools { example.zerorpc.Core core; }\n",
			"example.zerorpc.advisor.tools.Tools references example.zerorpc.Core"},
		{"the token chassis, from a test", "src/test/java/example/zerorpc/advisor/AdvisorTest.java",
			"package example.zerorpc.advisor;\n\nfinal class AdvisorTest { example.zerorpc.model.chassis.token.Token token; }\n",
			"example.zerorpc.advisor.AdvisorTest references example.zerorpc.model.chassis.token.Token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(tc.file, tc.text)
			defer os.Remove(filepath.Join(project, tc.file))
			out, ok := run()
			if ok {
				t.Fatalf("red: the gate passed an advisor that references %s:\n%s", tc.name, out)
			}
			if !strings.Contains(out, "tacplus LLM rule") || !strings.Contains(out, tc.want) {
				t.Fatalf("red: the failure is not the gate's %q:\n%s", tc.want, out)
			}
		})
	}

	if out, ok := run(); !ok {
		t.Fatalf("green again: the gate failed the advisor once it was back to the port alone:\n%s", out)
	}

	// An advisor package with no class is no advisor the gate can check.
	if err := os.RemoveAll(filepath.Join(project, "src", "main", "java", "example", "zerorpc", "advisor")); err != nil {
		t.Fatal(err)
	}
	if out, ok := run(); ok || !strings.Contains(out, "no class of the advisor package example.zerorpc.advisor") {
		t.Fatalf("no advisor classes: the gate = %v:\n%s", ok, out)
	}
}
