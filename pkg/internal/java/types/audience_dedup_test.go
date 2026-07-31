// Copyright (c) 2026 Second Star Cavalier LLC

package java

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The IdentityManager template hard-codes infra packages (grpcconfig, jwkview,
// entitlementview, healthreport) in the audienceFromMethod switch. Generated
// AudienceMappings must never re-emit them: java rejects a duplicate case label
// and the whole model fails to compile. This bit the fleet after the state-audience
// fix — curgen's fixture had no such package, only the real fleet did.
func TestIdentityManagerTemplateHardcodedAudiencePackages(t *testing.T) {
	data, err := os.ReadFile("../../../templates/java/chassis/service_im/IdentityManager.java.tmpl")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	tmpl := string(data)

	// Every literal case in the template's switch, before the range block.
	idx := strings.Index(tmpl, "{{- range .AudienceMappings}}")
	if idx < 0 {
		t.Fatal("AudienceMappings range block not found — template shape changed")
	}
	caseRe := regexp.MustCompile(`case "([a-z_]+)":`)
	var hardcoded []string
	for _, m := range caseRe.FindAllStringSubmatch(tmpl[:idx], -1) {
		hardcoded = append(hardcoded, m[1])
	}
	if len(hardcoded) == 0 {
		t.Fatal("no hard-coded audience cases parsed — test would be vacuous")
	}
	// Each must be pre-seeded in the generator's mappingSeen so it is skipped.
	src, err := os.ReadFile("libs.go")
	if err != nil {
		t.Fatalf("read libs.go: %v", err)
	}
	seenBlock := string(src)
	for _, pkg := range hardcoded {
		if !strings.Contains(seenBlock, `"`+pkg+`":`) {
			t.Errorf("template hard-codes case %q but libs.go mappingSeen does not pre-seed it — duplicate case label at javac time", pkg)
		}
	}
}
