// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// ServiceManager calls Register<Svc>MethodAuth whenever ServiceInfo.HasMethodAuth
// is set (service_manager.go.tmpl), but the wrapper DEFINES that function only
// when it produced at least one methodAuthLines entry (servicewrapper.go). The
// two conditions are computed independently, so they can disagree — and when the
// flag is set for a service whose wrapper wrote no line, the generated
// ServiceManager calls a function that does not exist and the generated Go does
// not compile ("undefined: Register<Svc>MethodAuth").
//
// The case that broke it: one rpc carrying target_namespace_field AND
// auth_any_user, with no auth_services anywhere. The wrapper skips the
// SetMethodTargetNamespaceField line for an auth_any_user method (it uses the
// field as a fixed namespace literal in the gate instead), but
// deriveGoServiceAuthFlags used to set HasMethodAuth for any non-empty
// target_namespace_field.
func TestGoMethodAuthFlagMatchesGeneratedWrapper(t *testing.T) {
	for _, tc := range []struct {
		name    string
		desc    parser.ServiceDescriptor
		wantDef bool // wrapper must define Register<Svc>MethodAuth
	}{
		{
			name: "any_user target_namespace_field only",
			desc: parser.ServiceDescriptor{
				Name: "ContractApi",
				Methods: []parser.MethodDescriptor{{
					Name:                 "TraceRead",
					InputType:            "TraceReadRequest",
					OutputType:           "TraceReadResponse",
					AuthAnyUser:          true,
					TargetNamespaceField: "/tenants/acme",
				}},
			},
			wantDef: false,
		},
		{
			name: "plain target_namespace_field registers",
			desc: parser.ServiceDescriptor{
				Name: "ContractApi",
				Methods: []parser.MethodDescriptor{{
					Name:                 "Read",
					InputType:            "ReadRequest",
					OutputType:           "ReadResponse",
					TargetNamespaceField: "namespace",
				}},
			},
			wantDef: true,
		},
		{
			name: "method auth_services registers",
			desc: parser.ServiceDescriptor{
				Name: "ContractApi",
				Methods: []parser.MethodDescriptor{{
					Name:         "Read",
					InputType:    "ReadRequest",
					OutputType:   "ReadResponse",
					AuthServices: []string{"spiffe://*/console"},
				}},
			},
			wantDef: true,
		},
		{
			name: "any_user target field beside a registering sibling",
			desc: parser.ServiceDescriptor{
				Name: "ContractApi",
				Methods: []parser.MethodDescriptor{
					{
						Name:                 "TraceRead",
						InputType:            "TraceReadRequest",
						OutputType:           "TraceReadResponse",
						AuthAnyUser:          true,
						TargetNamespaceField: "/tenants/acme",
					},
					{
						Name:         "Read",
						InputType:    "ReadRequest",
						OutputType:   "ReadResponse",
						AuthServices: []string{"spiffe://*/console"},
					},
				},
			},
			wantDef: true,
		},
		{
			// Service-level auth_services with every rpc an MCP tool: the
			// wrapper skips every method, so it writes no SetMethodAuth line
			// and defines nothing, while the seed used to claim the flag from
			// len(service.Methods) alone.
			name: "service auth_services with only mcp methods",
			desc: parser.ServiceDescriptor{
				Name:         "ContractApi",
				AuthServices: []string{"spiffe://*/console"},
				Methods: []parser.MethodDescriptor{{
					Name:       "Lookup",
					InputType:  "LookupRequest",
					OutputType: "LookupResponse",
					IsMcp:      true,
				}},
			},
			wantDef: false,
		},
		{
			name: "service auth_services with an mcp method beside a grpc one",
			desc: parser.ServiceDescriptor{
				Name:         "ContractApi",
				AuthServices: []string{"spiffe://*/console"},
				Methods: []parser.MethodDescriptor{
					{
						Name:       "Lookup",
						InputType:  "LookupRequest",
						OutputType: "LookupResponse",
						IsMcp:      true,
					},
					{
						Name:       "Read",
						InputType:  "ReadRequest",
						OutputType: "ReadResponse",
					},
				},
			},
			wantDef: true,
		},
		{
			name: "no auth metadata at all",
			desc: parser.ServiceDescriptor{
				Name: "ContractApi",
				Methods: []parser.MethodDescriptor{{
					Name:       "Read",
					InputType:  "ReadRequest",
					OutputType: "ReadResponse",
				}},
			},
			wantDef: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, hasMethodAuth := deriveGoServiceAuthFlags(tc.desc)

			rendered := renderGoWrapperForDescriptor(t, tc.desc)
			funcDef := "func Register" + tc.desc.Name + "MethodAuth(auth grpcserver.Authorizer)"
			gotDef := strings.Contains(rendered, funcDef)

			if gotDef != tc.wantDef {
				t.Fatalf("wrapper definition of %s: got %v, want %v:\n%s", funcDef, gotDef, tc.wantDef, rendered)
			}
			// The real invariant: ServiceManager emits the call iff HasMethodAuth,
			// so a set flag with no definition is generated Go that does not build.
			if hasMethodAuth != gotDef {
				t.Fatalf("HasMethodAuth=%v but wrapper defines %s = %v; ServiceManager would call a function that is %s:\n%s",
					hasMethodAuth, funcDef, gotDef,
					map[bool]string{true: "never defined", false: "defined but uncalled"}[hasMethodAuth],
					rendered)
			}
		})
	}
}

// renderGoWrapperForDescriptor generates the service wrapper for the same
// service the flag was derived from, and returns the emitted Go source.
func renderGoWrapperForDescriptor(t *testing.T, desc parser.ServiceDescriptor) string {
	t.Helper()

	methods := make([]*parser.MethodInfo, 0, len(desc.Methods))
	for _, m := range desc.Methods {
		methods = append(methods, &parser.MethodInfo{
			Name:                 m.Name,
			InputType:            m.InputType,
			OutputType:           m.OutputType,
			FullInputType:        "contract." + m.InputType,
			FullOutputType:       "contract." + m.OutputType,
			AuthServices:         m.AuthServices,
			AuthAnyUser:          m.AuthAnyUser,
			TargetNamespaceField: m.TargetNamespaceField,
			IsMcp:                m.IsMcp,
		})
	}
	info := &parser.ProtoInfo{
		Package:  "contract",
		Services: []*parser.ServiceInfo{{Name: desc.Name, Methods: methods}},
	}
	outDir := t.TempDir()
	svc := ServiceInfo{
		ProtoFile:    "svc/contract.proto",
		PackageName:  "contract",
		DirName:      "contract",
		ServiceName:  desc.Name,
		GrpcName:     "contract",
		ParentDir:    "svc",
		AuthServices: desc.AuthServices,
		Config:       &tactype.Config{OutputDir: outDir},
	}

	if err := generateServiceWrapper(svc, info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, nil); err != nil {
		t.Fatalf("generate Go service wrapper: %v", err)
	}
	wrapperDir := filepath.Join(outDir, svc.Config.GetSmPrefix(), "grpc", "servers", "svc", "contract")
	entries, err := os.ReadDir(wrapperDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one generated wrapper in %s, got %v (err %v)", wrapperDir, entries, err)
	}
	raw, err := os.ReadFile(filepath.Join(wrapperDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read generated wrapper: %v", err)
	}
	return string(raw)
}
