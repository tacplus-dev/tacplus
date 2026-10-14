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
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// service_manager.py calls <wrapper>.register_method_auth(authorizer) whenever
// ServiceInfo.HasMethodAuth is set, but the wrapper DEFINES register_method_auth
// only when it produced at least one methodAuthLines entry. The two conditions
// are computed independently, so they can disagree.
//
// The case that broke it: an rpc carrying target_namespace_field with no
// auth_services anywhere — the ordinary C2S shape (a service_c2s service has no
// service-level auth_services at all). The wrapper emits
// set_method_target_namespace_field for it, but the flag derivation only looked
// at auth_services, so HasMethodAuth stayed false and the ServiceManager never
// called register_method_auth. The authorizer's
// _method_target_namespace_field map then stayed empty and
// _check_target_namespace returned without checking anything: the level-or-below
// rule was silently not enforced.
func TestPythonMethodAuthFlagMatchesGeneratedWrapper(t *testing.T) {
	for _, tc := range []struct {
		name    string
		desc    parser.ServiceDescriptor
		wantDef bool // wrapper must define register_method_auth
	}{
		{
			name: "target_namespace_field only registers",
			desc: parser.ServiceDescriptor{
				Name: "ContractApi",
				Methods: []parser.MethodDescriptor{{
					Name:                 "Read",
					InputType:            "ReadRequest",
					OutputType:           "ReadResponse",
					TargetNamespaceField: "tenant_namespace",
				}},
			},
			wantDef: true,
		},
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
			name: "service auth_services registers",
			desc: parser.ServiceDescriptor{
				Name:         "ContractApi",
				AuthServices: []string{"spiffe://*/console"},
				Methods: []parser.MethodDescriptor{{
					Name:       "Read",
					InputType:  "ReadRequest",
					OutputType: "ReadResponse",
				}},
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
			_, hasMethodAuth := derivePyServiceAuthFlags(tc.desc)

			rendered := renderPyWrapperForDescriptor(t, tc.desc)
			gotDef := strings.Contains(rendered, "    def register_method_auth(self, authorizer):")

			if gotDef != tc.wantDef {
				t.Fatalf("wrapper definition of register_method_auth: got %v, want %v:\n%s", gotDef, tc.wantDef, rendered)
			}
			// The real invariant: ServiceManager emits the call iff HasMethodAuth,
			// so a false flag over a wrapper that registered something means the
			// registration never runs.
			if hasMethodAuth != gotDef {
				t.Fatalf("HasMethodAuth=%v but wrapper defines register_method_auth = %v; the registration is %s:\n%s",
					hasMethodAuth, gotDef,
					map[bool]string{true: "called but never defined", false: "defined but never called"}[hasMethodAuth],
					rendered)
			}
		})
	}
}

// renderPyWrapperForDescriptor generates the service wrapper for the same
// service the flag was derived from, and returns the emitted Python source.
func renderPyWrapperForDescriptor(t *testing.T, desc parser.ServiceDescriptor) string {
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
		})
	}
	info := &parser.ProtoInfo{
		Package:  "contract",
		Services: []*parser.ServiceInfo{{Name: desc.Name, Methods: methods}},
	}
	outDir := t.TempDir()
	svc := ServiceInfo{
		ProtoFile:    "contract/contract.proto",
		PackageName:  "contract",
		ServiceName:  desc.Name,
		GrpcName:     "contract",
		IsGrpc:       true,
		IsC2S:        true,
		ParentDir:    "contract",
		AuthServices: desc.AuthServices,
		Config:       &tactype.Config{OutputDir: outDir},
	}

	if err := generateServiceWrapper(svc, info, map[string]service.AccountingInfo{}); err != nil {
		t.Fatalf("generate Python service wrapper: %v", err)
	}

	var rendered string
	if err := filepath.Walk(outDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.IsDir() && filepath.Ext(path) == ".py" && filepath.Base(path) != "__init__.py" {
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			rendered = string(b)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk generated output: %v", err)
	}
	if rendered == "" {
		t.Fatalf("generated Python wrapper not found under %s", outDir)
	}
	return rendered
}
