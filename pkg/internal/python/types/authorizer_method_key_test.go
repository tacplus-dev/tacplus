// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// Two DIFFERENT services hosted behind ONE authorizer that each declare an RPC
// with the SAME bare name must keep their own auth_services. Keyed by the bare
// name ("Get"), the second registration silently overwrote the first, so
// whichever service registered last decided authorization for BOTH — a caller
// authorized for alpha.AlphaService/Get also passed for beta.BetaService/Get.
//
// Python has no generated-code test runner (no gate executes the generated
// Python authorizer), so — like the page-token behavioral check in
// runtime_templates_regression_test.go — this lifts the real key-derivation
// function verbatim out of the template and executes it under the system
// interpreter, driven by the keys the GENERATOR actually emits. Together those
// two halves are what decide the lookup at runtime.
func TestPythonAuthorizerKeysMethodPolicyPerService(t *testing.T) {
	src, err := templates.FS.ReadFile("python/chassis/grpcserver/authorizer.py.tmpl")
	if err != nil {
		t.Fatalf("read authorizer template: %v", err)
	}
	rendered := string(src)

	const fnStart = "def _qualified_method_key(full_method: str) -> str:"
	i := strings.Index(rendered, fnStart)
	if i < 0 {
		t.Fatalf("authorizer.py.tmpl no longer declares _qualified_method_key")
	}
	fnSrc := rendered[i:]
	if end := strings.Index(fnSrc, "\n\n\n"); end >= 0 {
		fnSrc = fnSrc[:end]
	}
	if strings.Contains(fnSrc, "{{") {
		t.Fatalf("_qualified_method_key gained template directives; render it before lifting")
	}

	var py string
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			py = p
			break
		}
	}
	if py == "" {
		t.Skip("no python interpreter found; skipping behavioral check")
	}

	// The keys the generator emits for two services that both declare "Get".
	alphaKey := pyQualifiedMethodAuthKey("alpha", "AlphaService", "Get")
	betaKey := pyQualifiedMethodAuthKey("beta", "BetaService", "Get")
	if alphaKey == betaKey {
		t.Fatalf("generator emits the same registration key %q for two different services", alphaKey)
	}

	script := fnSrc + `

# Exactly what two generated register_method_auth functions do against the one
# authorizer the ServiceManager builds.
policy = {}
policy[` + strconv.Quote(alphaKey) + `] = "alpha-caller"
policy[` + strconv.Quote(betaKey) + `] = "beta-caller"

assert len(policy) == 2, "a registration overwrote the other: %d" % len(policy)

lookup = lambda full: policy.get(_qualified_method_key(full))

assert lookup("/alpha.AlphaService/Get") == "alpha-caller", lookup("/alpha.AlphaService/Get")
assert lookup("/beta.BetaService/Get") == "beta-caller", lookup("/beta.BetaService/Get")

# The routing-parent shape resolves to the same key.
assert _qualified_method_key("/services/alpha.AlphaService/Get") == ` + strconv.Quote(alphaKey) + `

print("ok")
`
	dir := t.TempDir()
	mod := filepath.Join(dir, "method_key_check.py")
	if err := os.WriteFile(mod, []byte(script), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	out, err := exec.Command(py, mod).CombinedOutput()
	if err != nil {
		t.Fatalf("per-service method-policy keying is broken under Python: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ok") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

// The generator half of the pair above: register_method_auth must key policy
// under the SERVICE-QUALIFIED identifier the authorizer derives from the full
// gRPC method, never the bare rpc name.
func TestPythonServiceWrapperRegistersMethodAuthUnderQualifiedKey(t *testing.T) {
	outputDir := t.TempDir()
	svc := ServiceInfo{
		ProtoFile:    "contract/contract.proto",
		PackageName:  "contractclient",
		ServiceName:  "ContractApi",
		GrpcName:     "contract",
		IsGrpc:       true,
		IsC2S:        true,
		ParentDir:    "contract",
		AuthServices: []string{"identity"},
		Config:       &tactype.Config{OutputDir: outputDir},
	}
	info := &parser.ProtoInfo{
		Package: "contractclient",
		Services: []*parser.ServiceInfo{{
			Name: "ContractApi",
			Methods: []*parser.MethodInfo{{
				Name:                 "Read",
				InputType:            "ContractRequest",
				OutputType:           "ContractResponse",
				FullInputType:        "contractclient.ContractRequest",
				FullOutputType:       "contractclient.ContractResponse",
				TargetNamespaceField: "tenant_namespace",
			}},
		}},
	}

	if err := generateServiceWrapper(svc, info, map[string]service.AccountingInfo{}); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}

	var rendered string
	err := filepath.Walk(outputDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.IsDir() && filepath.Base(path) == "contract_api.py" {
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			rendered = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk generated output: %v", err)
	}
	if rendered == "" {
		t.Fatalf("generated Python wrapper contract_api.py not found under %s", outputDir)
	}

	for _, want := range []string{
		`authorizer.set_method_auth("contractclient.ContractApi/Read", ["identity"])`,
		`authorizer.set_method_target_namespace_field("contractclient.ContractApi/Read", "tenant_namespace")`,
		// The same identifier the wrapper hands to authorize().
		`'/contractclient.ContractApi/Read'`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("generated Python wrapper missing %q:\n%s", want, rendered)
		}
	}
	for _, notWant := range []string{
		`authorizer.set_method_auth("Read"`,
		`authorizer.set_method_target_namespace_field("Read"`,
	} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("generated Python wrapper registered policy under the bare rpc name %q:\n%s", notWant, rendered)
		}
	}
}
