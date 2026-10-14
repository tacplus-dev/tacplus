// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// The go chassis has TWO entry points into the same logger: Install() in
// logger.go (env-driven) and InstallFromConfig() in helpers.go (typed proto
// config). InstallFromConfig was fixed to build over a redacting SINK so that
// structured fields are redacted; Install() was left on zapConfig.Build() plus a
// message-only redactingCore.
//
// That gave the two paths different security properties. A message-only
// redactor rewrites ent.Message and nothing else, so the encoder writes field
// keys and values straight to the sink: zap.String("authorization", ...),
// zap.Error(err) on an error quoting a header, or anything bound earlier via
// logger.With() reached stderr intact. Which installer a service happened to
// call decided whether its field-borne credentials leaked.
//
// The existing security test did not catch it -- it hand-builds a
// redactingWriter and asserts against that construction while its comment claims
// to mirror InstallFromConfig. The capability was real and correct; the call
// site was missing. Guard the CALL SITE.
func TestGoInstallBuildsOverRedactingSink(t *testing.T) {
	src, err := templates.FS.ReadFile("go/chassis/logger/logger.go.tmpl")
	if err != nil {
		t.Fatalf("read logger template: %v", err)
	}
	logger := string(src)

	installAt := strings.Index(logger, "func (x *Logger) Install()")
	if installAt < 0 {
		t.Fatalf("Install() not found in logger.go.tmpl")
	}
	body := logger[installAt:]

	// zapConfig.Build() opens its own sinks internally, leaving no seam to
	// redact at. Using it here is the defect, not a style choice.
	//
	// Match the ASSIGNMENT, not any mention of the call: an earlier version
	// searched for the bare name and matched the comment directly above the fix
	// explaining why the call was removed.
	if strings.Contains(body, "x.ZapLogger, err = zapConfig.Build()") {
		t.Fatalf("Install() must not build via zapConfig.Build(): it opens its own sinks, " +
			"leaving no seam to redact structured fields at")
	}
	if !strings.Contains(body, "&redactingWriter{w: sink}") {
		t.Fatalf("Install() must build its core over a redactingWriter so that " +
			"structured fields are redacted, not just ent.Message")
	}

	// Sampling was applied by zapConfig.Build(); dropping it while replacing the
	// builder would silently change log volume under load.
	if !strings.Contains(body, "zapcore.NewSamplerWithOptions(") {
		t.Fatalf("Install() must preserve the sampling zapConfig.Build() applied")
	}
}

// Both installers must redact at the sink. If one regresses to message-only
// redaction the fleet gets an inconsistent guarantee again, which is worse than
// a uniform weak one because it is invisible at the call site.
func TestGoBothLoggerInstallersRedactAtTheSink(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"Install", "go/chassis/logger/logger.go.tmpl"},
		{"InstallFromConfig", "go/chassis/logger/helpers.go.tmpl"},
	} {
		src, err := templates.FS.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("read %s: %v", tc.path, err)
		}
		if !strings.Contains(string(src), "redactingWriter{w: sink}") {
			t.Fatalf("%s must wrap its sink in redactingWriter; message-only "+
				"redaction lets field-borne credentials through", tc.name)
		}
	}
}
