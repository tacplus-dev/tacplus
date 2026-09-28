// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
)

// mustGenerateRustGRPCWrapper renders one method wrapper for a test and fails
// the test if the generator refuses to emit one. generateRustGRPCWrapper can
// refuse — a method touching a (spec.owner_default) type whose verb did not
// classify is a generation error — and no render test here exercises that path.
func mustGenerateRustGRPCWrapper(t *testing.T, svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage, parentDir string, crossPkgTypes map[string]string, grpcName string, accountingMap map[string]service.AccountingInfo, skippedTraceTypes proto.SkippedTraceTypes, san *rustEgressSanitizer) string {
	t.Helper()
	out, err := generateRustGRPCWrapper(svc, method, servicePackage, parentDir, crossPkgTypes, grpcName, accountingMap, skippedTraceTypes, san)
	if err != nil {
		t.Fatalf("generateRustGRPCWrapper(%s): %v", method.Name, err)
	}
	return out
}
