// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// registerMethodAuth must key policy under the SERVICE-QUALIFIED identifier the
// wrapper itself hands to Authorizer.authorize() for that method — never the
// bare rpc name. Lookup in the Authorizer is by that exact string, so the two
// halves must agree; and a bare name is what let one co-hosted service's policy
// answer for another service's identically named rpc.
func TestJavaServiceWrapperRegistersMethodAuthUnderTheKeyAuthorizeUses(t *testing.T) {
	for _, tc := range []struct {
		name       string
		parentDir  string
		wantKey    string
		wantNsLine string
	}{
		{
			name:      "service",
			parentDir: "contract",
			wantKey:   "ContractApi/Read",
		},
		{
			// A vservice is package-qualified, because the Authorizer derives
			// the C2S grant symbol from this same string and must use the
			// trait's package rather than falling back to the host's.
			name:      "vservice",
			parentDir: "vservices",
			wantKey:   "contractclient.ContractApi/Read",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputDir := t.TempDir()
			wrapperDir := filepath.Join(outputDir, "wrapper")
			typesDir := filepath.Join(outputDir, "types")
			svc := ServiceInfo{
				ProtoFile:      "contract/contract.proto",
				PackageName:    "contractclient",
				ServiceName:    "ContractApi",
				GrpcName:       "contract",
				IsGrpc:         true,
				IsC2S:          true,
				OutputDir:      wrapperDir,
				TypesOutputDir: typesDir,
				ParentDir:      tc.parentDir,
				AuthServices:   []string{"identity"},
				Config: &tactype.Config{
					OutputDir:       outputDir,
					JavaPackageBase: "com.example",
				},
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

			if _, err := generateServiceWrapper(svc, info, nil, proto.SkippedTraceTypes{}, nil); err != nil {
				t.Fatalf("generateServiceWrapper: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(wrapperDir, "ContractApi.java"))
			if err != nil {
				t.Fatalf("read generated wrapper: %v", err)
			}
			rendered := string(raw)

			for _, want := range []string{
				`checkAuth("` + tc.wantKey + `", request);`,
				`authorizer.setMethodAuth("` + tc.wantKey + `", java.util.List.of("identity"));`,
				`authorizer.setMethodTargetNamespaceField("` + tc.wantKey + `", "tenant_namespace");`,
			} {
				if !strings.Contains(rendered, want) {
					t.Fatalf("generated Java wrapper missing %q:\n%s", want, rendered)
				}
			}
			// The bare rpc name is not a key in any of the three places.
			for _, notWant := range []string{
				`authorizer.setMethodAuth("Read"`,
				`authorizer.setMethodTargetNamespaceField("Read"`,
			} {
				if strings.Contains(rendered, notWant) {
					t.Fatalf("generated Java wrapper registered policy under the bare rpc name %q:\n%s", notWant, rendered)
				}
			}
		})
	}
}
