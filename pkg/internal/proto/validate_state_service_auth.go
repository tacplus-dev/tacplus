// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// generatedStateServiceRe matches the sentinel/service header pair that
// AppendStateServiceToProtos emits for every spec.service_state root, capturing
// the service name. Config-state services carry the same sentinel and are
// checked identically.
var generatedStateServiceRe = regexp.MustCompile(
	`(?m)^// tacplus:generated-service ` + parser.SynthesizedKindState + `\n\s*service\s+(\w+)\s*\{`)

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

		for _, m := range generatedStateServiceRe.FindAllStringSubmatchIndex(text, -1) {
			svcName := text[m[2]:m[3]]
			if authExempt[filepath.ToSlash(rel)+"/"+svcName] {
				continue
			}
			// Body runs from the opening brace to the service's closing brace.
			body := text[m[1]:]
			if end := strings.Index(body, "\n}"); end >= 0 {
				body = body[:end]
			}
			if c2sServiceRe.MatchString(body) {
				continue
			}
			if !s2sAuthServicesRe.MatchString(body) {
				violations = append(violations, fmt.Sprintf(
					"%s: generated state service %s has no auth_services on (spec.service_s2s) — "+
						"every service caller would be denied at runtime with "+
						"\"no auth_services policy configured\" and no build-time signal; "+
						"the caller list is resolved from the state root's owning package and its "+
						"declared (spec.state_import)/(spec.state_imports) importers, or the root "+
						"must declare (spec.disable_auth_message) = true",
					rel, svcName))
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("validate generated state service auth: %w", err)
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		return fmt.Errorf("generated state services missing auth_services:\n  %s",
			strings.Join(violations, "\n  "))
	}
	return nil
}
