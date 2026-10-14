// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// generatedServiceRe matches the sentinel/service header pair that a synthesis
// path emits for one SynthesizedKind, capturing the service name.
func generatedServiceRe(kind string) *regexp.Regexp {
	return regexp.MustCompile(
		`(?m)^// tacplus:generated-service ` + kind + `\n\s*service\s+(\w+)\s*\{`)
}

// Config-state services carry the state sentinel and are checked identically.
var (
	generatedStateServiceRe   = generatedServiceRe(parser.SynthesizedKindState)
	generatedStoreServiceRe   = generatedServiceRe(parser.SynthesizedKindStore)
	generatedOpaEvalServiceRe = generatedServiceRe(parser.SynthesizedKindOpaEval)
)

// s2sAuthServicesRe matches a spec.service_s2s option carrying at least one
// auth_services entry.
var s2sAuthServicesRe = regexp.MustCompile(`\(spec\.service_s2s\)\s*=\s*\{[^}]*auth_services\s*:\s*\[\s*"`)

var c2sServiceRe = regexp.MustCompile(`\(spec\.service_c2s\)\s*=`)

// ValidateGeneratedStateServiceAuth is the post-condition on state-service
// emission: every generated S2S state service must declare a non-empty
// auth_services on its spec.service_s2s option. Record/event topic services
// with a declared C2S producer surface are checked by the ordinary C2S auth
// path instead.
//
// Why this is a hard error and not a lint: the Go, Java and Rust chassis
// authorizers all fail closed on an unregistered method — a service caller hits
// PermissionDenied("no auth_services policy configured...") at runtime, and an
// empty auth_services produces exactly that with no build-time signal. Making
// emission without a resolvable caller list fail generation is what turns the
// gap from a silent runtime denial into an impossible state.
//
// authExempt holds the state services whose root declares
// (spec.disable_auth_message): those skip token validation entirely, so they
// carry no caller list by design. Keys are "<protoDir-relative path>/<ServiceName>".
func ValidateGeneratedStateServiceAuth(protoDir string, authExempt map[string]bool) error {
	return validateGeneratedServiceAuth(protoDir, generatedStateServiceRe, "state", authExempt,
		"the caller list is resolved from the state root's owning package and its "+
			"declared (spec.state_import)/(spec.state_imports) importers, or the root "+
			"must declare (spec.disable_auth_message) = true")
}

// ValidateGeneratedStoreServiceAuth is the same post-condition for the store
// synthesis path (AppendStoreServiceToProtos). Store roots cannot declare
// (spec.disable_auth_message) — it is rejected on any message without
// (spec.service_state) — so there is no exemption set here.
func ValidateGeneratedStoreServiceAuth(protoDir string) error {
	return validateGeneratedServiceAuth(protoDir, generatedStoreServiceRe, "store", nil,
		"the caller list is the package that declares the (spec.service_store) message")
}

// ValidateGeneratedOpaEvalServiceAuth is the same post-condition for the OPA
// entitlement-evaluation synthesis path (GenerateOpaEvalProto).
func ValidateGeneratedOpaEvalServiceAuth(protoDir string) error {
	return validateGeneratedServiceAuth(protoDir, generatedOpaEvalServiceRe, "opa eval", nil,
		"the caller list is the package the OpaEval service is generated for")
}

// validateGeneratedServiceAuth walks protoDir and flags every service matched by
// re whose body carries no non-empty auth_services. remedy is appended to each
// violation to name the caller list that synthesis path is supposed to resolve.
func validateGeneratedServiceAuth(protoDir string, re *regexp.Regexp, kind string, authExempt map[string]bool, remedy string) error {
	var violations []string

	err := filepath.Walk(protoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return err
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", path, rerr)
		}
		text := string(data)
		rel, relErr := filepath.Rel(protoDir, path)
		if relErr != nil {
			rel = path
		}

		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			svcName := text[m[2]:m[3]]
			if authExempt[filepath.ToSlash(rel)+"/"+svcName] {
				continue
			}
			// Body runs from the opening brace to the service's closing brace,
			// found by brace-depth tracking rather than the first unindented
			// "}" — an option literal in the body can itself contain nested
			// braces, and a plain string search would stop at the first one.
			body := text[m[1]:]
			openBrace := m[1] - 1
			if end := findMatchingCloseBrace(text, openBrace); end >= 0 {
				body = text[m[1]:end]
			}
			if c2sServiceRe.MatchString(body) {
				continue
			}
			if !s2sAuthServicesRe.MatchString(body) {
				violations = append(violations, fmt.Sprintf(
					"%s: generated %s service %s has no auth_services on (spec.service_s2s) — "+
						"every service caller would be denied at runtime with "+
						"\"no auth_services policy configured\" and no build-time signal; %s",
					rel, kind, svcName, remedy))
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("validate generated %s service auth: %w", kind, err)
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		return fmt.Errorf("generated %s services missing auth_services:\n  %s",
			kind, strings.Join(violations, "\n  "))
	}
	return nil
}
