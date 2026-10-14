// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// (spec.assert_ipaddr_type) MASK_V4/MASK_V6/MASK_ANY constrain a decimal prefix
// length. Every other target parses the whole string and rejects anything else:
// java `Integer.parseInt` (NumberFormatException), python `int(v)` (ValueError),
// rust `v.parse::<u8>()` (Err), typescript `/^\d+$/`.
//
// Go emitted `fmt.Sscanf(v, "%d", &mask)`, which stops as soon as the verb is
// satisfied and reports no error for the unconsumed remainder. That made the go
// bound fail-open on every mixed input: "24abc" and "24.9" scanned as 24, and
// "0x10" scanned as 0 — all accepted by go, all rejected by the other four. A
// mask field carrying "0x10" therefore passed go validation as prefix length 0,
// which is a materially different network than the one the caller wrote.
func TestGoIPAddrMaskParseIsStrict(t *testing.T) {
	msg := model.Message{
		Name: "NetConfig",
		Fields: []model.Field{
			maskField(model.Field{Name: "v4_mask", Type: "string"}, "IP_ADDR_TYPE_MASK_V4"),
			maskField(model.Field{Name: "v6_mask", Type: "string"}, "IP_ADDR_TYPE_MASK_V6"),
			maskField(model.Field{Name: "any_mask", Type: "string"}, "IP_ADDR_TYPE_MASK_ANY"),
			maskField(model.Field{Name: "v4_masks", Type: "string", IsRepeated: true}, "IP_ADDR_TYPE_MASK_V4"),
			maskField(model.Field{Name: "v6_masks", Type: "string", IsRepeated: true}, "IP_ADDR_TYPE_MASK_V6"),
			maskField(model.Field{Name: "any_masks", Type: "string", IsRepeated: true}, "IP_ADDR_TYPE_MASK_ANY"),
		},
	}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	// All six shapes parse strictly, and each keeps its own bound.
	want := []string{
		"mask, err := strconv.Atoi(m.V4Mask)\n\t\tif err != nil || mask < 0 || mask > 32 {",
		"mask, err := strconv.Atoi(m.V6Mask)\n\t\tif err != nil || mask < 0 || mask > 128 {",
		"mask, err := strconv.Atoi(m.AnyMask)\n\t\tif err != nil || mask < 0 || mask > 128 {",
		"mask, err := strconv.Atoi(v)\n\t\t\tif err != nil || mask < 0 || mask > 32 {",
		"mask, err := strconv.Atoi(v)\n\t\t\tif err != nil || mask < 0 || mask > 128 {",
	}
	for _, snippet := range want {
		if !strings.Contains(rendered, snippet) {
			t.Errorf("missing strict mask parse in generated validation:\n%s", snippet)
		}
	}

	// The exact pre-fix fragment: a revert reintroduces it.
	if strings.Contains(rendered, "fmt.Sscanf") {
		t.Errorf("generated mask validation uses fmt.Sscanf, which ignores trailing input")
	}
	if strings.Contains(rendered, "var mask int") {
		t.Errorf("generated mask validation still declares the Sscanf out-parameter")
	}
}

// The semantic difference the emitter change turns on, executed rather than
// asserted: Sscanf accepts every one of these, Atoi rejects them.
func TestGoMaskParseRejectsTrailingInput(t *testing.T) {
	for _, in := range []string{"24abc", "24.9", "0x10", "  24", "24 "} {
		var scanned int
		if _, err := fmt.Sscanf(in, "%d", &scanned); err != nil {
			t.Fatalf("premise broken: fmt.Sscanf(%q) already errors (%v); the emitter change is moot", in, err)
		}
		if _, err := strconv.Atoi(in); err == nil {
			t.Errorf("strconv.Atoi(%q) accepted a non-decimal mask; go still disagrees with java/python/rust/typescript", in)
		}
	}
	// Well-formed bounds still parse, including the signed form java's
	// Integer.parseInt, python's int() and rust's parse() all accept.
	for _, in := range []string{"0", "24", "128", "+24"} {
		if _, err := strconv.Atoi(in); err != nil {
			t.Errorf("strconv.Atoi(%q) rejected a mask the other four targets accept: %v", in, err)
		}
	}
}

// The same lenient scan sat on the (spec.assert_listen_addr) port in the
// generated utils package, where "8080abc" validated as port 8080 while java,
// python and rust all rejected it. Both helpers in that template now parse
// strictly, so the template no longer needs fmt.
func TestGoListenAddrPortParseIsStrict(t *testing.T) {
	tmpl := ValidationGoTmpl()

	for _, snippet := range []string{
		"port, err := strconv.Atoi(portStr)\n\tif err != nil || port < 0 || port > 65535 {",
		"p, scanErr := strconv.Atoi(portStr)\n\tif scanErr != nil || p < 0 || p > 65535 {",
		"\t\"strconv\"\n",
	} {
		if !strings.Contains(tmpl, snippet) {
			t.Errorf("generated utils validation.go missing:\n%s", snippet)
		}
	}
	if strings.Contains(tmpl, "fmt.Sscanf") {
		t.Errorf("generated utils validation.go parses the port with fmt.Sscanf")
	}
	// fmt is unused once the scans are gone; leaving it imported does not compile.
	if strings.Contains(tmpl, "\t\"fmt\"\n") {
		t.Errorf("generated utils validation.go imports fmt but no longer uses it")
	}
}

func maskField(f model.Field, ipType string) model.Field {
	f.Annotations = map[string]model.AnnotationValue{
		"assert_ipaddr_type": {Type: model.AnnotationTypeEnum, EnumValue: ipType},
	}
	return f
}
