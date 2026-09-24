// Copyright (c) 2026 Second Star Cavalier LLC

package common

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/tmplutil"
)

// The generated state-singleton Fetch must apply the fetched type's proto
// defaults BEFORE it validates that value — in every language that generates
// one, server and client alike.
//
// This is a live production crash, not a hypothetical. scottyagent crash-looped
// on 2026-09-23 with
//
//	get_config: state value still failed validation after 30s:
//	field lock_acquire_attempts must be >= 1, got: 0
//
// against
//
//	int32 lock_acquire_attempts = 1 [(spec.default_int) = 5, (spec.assert_ge) = 1];
//
// The seeded state-authority config omits lock_acquire_attempts on purpose,
// because the proto declares a default of 5. But the wire carries no defaults:
// the raw Get response arrives with the field at 0, and (spec.assert_ge) = 1
// rejects 0. de12c933's validate-retry loop then re-fetched the same
// already-correct value until config_fetch_timeout_seconds (30s) was spent and
// failed loudly — on a value that should have passed on the first attempt.
//
// Each language carries its own copy of this fetch body, and each was wrong the
// same way for the same reason, so a per-language pin would not have caught it:
// nothing compared them. This does. The order is the whole invariant — applying
// defaults AFTER validate, or not at all, reproduces the crash.
func TestStateSingletonFetchAppliesDefaultsBeforeValidateInEveryLanguage(t *testing.T) {
	for _, tc := range []struct {
		lang string
		dir  string
		file string
		// fetchStart anchors the singleton fetch accessor. defaults and validate
		// are the two calls whose ORDER inside that body is the invariant.
		fetchStart string
		defaults   string
		validate   string
	}{
		{
			lang:       "go",
			dir:        "go/types",
			file:       "state_wrapper.go.tmpl",
			fetchStart: "func (w *{{$s.FieldName}}Wrapper) Fetch(ctx context.Context)",
			defaults:   "{{DefaultsRef $s.GoType $s.IsCrossPackage}}(value)",
			validate:   "{{ValidateRef $s.GoType $s.IsCrossPackage}}(value)",
		},
		{
			lang:       "rust",
			dir:        "rust/types",
			file:       "statewrapper.rs.tmpl",
			fetchStart: "pub async fn fetch(&self) -> Result<{{.TypeName}}, tonic::Status>",
			defaults:   "{{.DefaultsFn}}(&mut value);",
			validate:   "match value.validate()",
		},
		{
			lang: "java",
			dir:  "java/types",
			file: "statewrapper.java.tmpl",
			// Java's defaults helper returns a Builder, so the fetched value is
			// replaced by the defaulted one rather than mutated in place.
			fetchStart: "public {{.JavaType}} fetch{{.FieldPascal}}()",
			defaults:   "value = {{.DefaultsRef}}(value).build();",
			validate:   "{{.ValidateRef}}(value);",
		},
		{
			lang:       "python",
			dir:        "python/types",
			file:       "statewrapper.py.tmpl",
			fetchStart: "async def fetch_{{.FieldName}}(self) -> pb.{{.TypeName}}:",
			defaults:   "{{.DefaultsRef}}(value)",
			validate:   "{{.ValidateRef}}(value)",
		},
		{
			lang:       "typescript",
			dir:        "typescript/types",
			file:       "statewrapper.ts.tmpl",
			fetchStart: "async fetch{{.FieldPascal}}(): Promise<{{.TypePrefix}}.{{.TypeName}}>",
			defaults:   "{{.TypePrefix}}.{{.DefaultsFn}}(value);",
			validate:   "{{.TypePrefix}}.validate{{.TypeName}}(value);",
		},
	} {
		s, err := tmplutil.LoadTemplate(tc.dir, tc.file)
		if err != nil {
			t.Fatalf("%s: load %s: %v", tc.lang, tc.file, err)
		}
		start := strings.Index(s, tc.fetchStart)
		if start < 0 {
			t.Errorf("%s: %s no longer contains the singleton fetch accessor %q", tc.lang, tc.file, tc.fetchStart)
			continue
		}
		body := s[start:]
		validateAt := strings.Index(body, tc.validate)
		if validateAt < 0 {
			t.Errorf("%s: singleton fetch in %s no longer validates the fetched value (%q)", tc.lang, tc.file, tc.validate)
			continue
		}
		defaultsAt := strings.Index(body, tc.defaults)
		if defaultsAt < 0 {
			t.Errorf("%s: singleton fetch in %s never applies the type's generated proto defaults (%q); "+
				"the wire carries no defaults, so a field omitted because it declares (spec.default_int) = 5 "+
				"arrives at 0 and fails its own (spec.assert_ge) = 1 for the whole retry budget",
				tc.lang, tc.file, tc.defaults)
			continue
		}
		if defaultsAt > validateAt {
			t.Errorf("%s: singleton fetch in %s applies proto defaults (%q) AFTER validating (%q); "+
				"defaults must be applied first or validation sees the raw wire value",
				tc.lang, tc.file, tc.defaults, tc.validate)
		}
	}
}

// The defaults call must be inside the retry loop, on each fetched value — not
// hoisted above it. Every iteration re-fetches a fresh raw value from the wire,
// so a single pre-loop application would leave every retry after the first
// validating an undefaulted value: the exact 30s burn the crash showed.
func TestStateSingletonFetchAppliesDefaultsInsideTheRetryLoop(t *testing.T) {
	for _, tc := range []struct {
		lang     string
		dir      string
		file     string
		loop     string
		defaults string
	}{
		{"go", "go/types", "state_wrapper.go.tmpl", "for attempt := 0; ; attempt++ {", "{{DefaultsRef $s.GoType $s.IsCrossPackage}}(value)"},
		{"rust", "rust/types", "statewrapper.rs.tmpl", "let mut attempt = 0u32;", "{{.DefaultsFn}}(&mut value);"},
		{"java", "java/types", "statewrapper.java.tmpl", "for (int attempt = 0; ; attempt++) {", "value = {{.DefaultsRef}}(value).build();"},
		{"python", "python/types", "statewrapper.py.tmpl", "while True:", "{{.DefaultsRef}}(value)"},
		{"typescript", "typescript/types", "statewrapper.ts.tmpl", "for (let attempt = 0; ; attempt++) {", "{{.TypePrefix}}.{{.DefaultsFn}}(value);"},
	} {
		s, err := tmplutil.LoadTemplate(tc.dir, tc.file)
		if err != nil {
			t.Fatalf("%s: load %s: %v", tc.lang, tc.file, err)
		}
		loopAt := strings.Index(s, tc.loop)
		if loopAt < 0 {
			t.Errorf("%s: %s no longer contains the singleton fetch retry loop %q", tc.lang, tc.file, tc.loop)
			continue
		}
		defaultsAt := strings.Index(s, tc.defaults)
		if defaultsAt < 0 {
			t.Errorf("%s: %s never applies the type's generated proto defaults (%q)", tc.lang, tc.file, tc.defaults)
			continue
		}
		if defaultsAt < loopAt {
			t.Errorf("%s: %s applies proto defaults (%q) before entering the fetch retry loop (%q); "+
				"each retry re-fetches a fresh raw value, so only the first attempt would be defaulted",
				tc.lang, tc.file, tc.defaults, tc.loop)
		}
	}
}
