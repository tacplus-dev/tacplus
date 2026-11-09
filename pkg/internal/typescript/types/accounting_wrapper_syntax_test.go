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
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// TestTypeScriptAccountedServiceWrapperParses generates a whole S2S wrapper
// whose every method kind is accounted (tacplus#273) and checks node parses
// it (esbuild, tsSyntaxCheck's tool, is often absent): runWithMetrics closes each method body and the stage statements land
// between statements everywhere.
func TestTypeScriptAccountedServiceWrapperParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping accounted wrapper parse check")
	}
	outputDir := t.TempDir()
	svc := ServiceInfo{
		ProtoFile:    "contract/contract.proto",
		PackageName:  "contract",
		ServiceName:  "ContractApi",
		GrpcName:     "contract",
		IsGrpc:       true,
		OutputDir:    outputDir,
		ParentDir:    "contract",
		Config:       &tactype.Config{OutputDir: outputDir, TsPackageBase: "demo-tacplus"},
		AuthServices: []string{"identity"},
	}
	plain := func(name string, client, server, emptyOut bool) *parser.MethodInfo {
		out, fullOut := "ReadResponse", "contract.ReadResponse"
		if emptyOut {
			out, fullOut = "Empty", "google.protobuf.Empty"
		}
		return &parser.MethodInfo{
			Name: name, InputType: "ReadRequest", OutputType: out,
			FullInputType: "contract.ReadRequest", FullOutputType: fullOut,
			ClientStreaming: client, ServerStreaming: server,
		}
	}
	methods := []*parser.MethodInfo{
		plain("Read", false, false, false),
		plain("Touch", false, false, true),
		plain("Stream", false, true, false),
		plain("Upload", true, false, false),
		plain("Chat", true, true, false),
		tsTier2Method(),
		tsTier3Method(),
		tsWatchMethod(),
		tsPageMethod(),
	}
	rpcs := map[string]bool{}
	ordinals := map[string]int{}
	for i, m := range methods {
		rpcs[m.Name] = true
		ordinals[m.Name] = i + 1
	}
	info := &parser.ProtoInfo{
		Package:  "contract",
		Services: []*parser.ServiceInfo{{Name: "ContractApi", IsS2S: true, Methods: methods}},
	}
	accounting := map[string]service.AccountingInfo{"contract": {EnumName: "Contract", RPCs: rpcs, RPCOrdinals: ordinals}}
	if _, err := generateServiceWrapper(svc, info, nil, accounting); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}
	rendered := readOnlyFileInDir(t, outputDir)
	if got := strings.Count(rendered, "accounting.runWithMetrics(async (__tacplusAccountingMetrics) => {"); got != len(methods) {
		t.Fatalf("%d methods run inside runWithMetrics, want %d:\n%s", got, len(methods), rendered)
	}

	path := filepath.Join(t.TempDir(), "wrapper.ts")
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
	// node strips the types and parses the whole module before it resolves
	// any import, so a wrapper that parses fails on its first (absent) import.
	out, _ := exec.Command(node, path).CombinedOutput()
	if !strings.Contains(string(out), "ERR_MODULE_NOT_FOUND") {
		t.Fatalf("node cannot parse the accounted wrapper:\n%s\n%s", out, rendered)
	}
}
