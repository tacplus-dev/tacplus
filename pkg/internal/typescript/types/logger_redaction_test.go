// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"strings"
	"testing"
)

// TypeScript was the only chassis logger with NO credential redaction: go,
// rust, java and python all strip credentials at the log boundary, while the
// Winston configurator passed `authorization: Bearer ...` through verbatim at
// any level, to any sink.
//
// Scope, stated honestly: these templates are NOT emitted today (see
// TestTypeScriptLoggerChassisIsNotEmitted), so this was a latent defect, not a
// live leak in any generated client. The fix matters when a Node-side TS target
// appears; it did not close a hole that was shipping.
//
// These guards are deliberately about the CALL SITE, not the capability.
// Shipping a correct redactor that nothing installs is the failure mode this
// codebase has hit before -- the capability exists, an existence test passes,
// and the defect is live in production. So assert that WinstonConfigurator
// actually installs it, and installs it FIRST.
func TestTypeScriptLoggerInstallsRedactionFirst(t *testing.T) {
	cfg := readTSTemplate(t, "typescript/chassis/logger/WinstonConfigurator.ts.tmpl")

	if !strings.Contains(cfg, "import { redactFormat } from './Redact.js';") {
		t.Fatalf("WinstonConfigurator must import redactFormat from the redaction module")
	}
	if !strings.Contains(cfg, "formats.push(redactFormat());") {
		t.Fatalf("WinstonConfigurator must install redactFormat into the format chain")
	}

	// Ordering is load-bearing, not cosmetic. Redaction placed after json() or
	// printf() never sees the entry before it is serialised, so the structured
	// path -- the idiomatic way to log a credential -- would go out in the clear
	// while the console path looked protected.
	redactAt := strings.Index(cfg, "formats.push(redactFormat());")
	for _, serialiser := range []string{
		"winston.format.json()",
		"winston.format.printf(",
		"winston.format.timestamp()",
	} {
		at := strings.Index(cfg, serialiser)
		if at >= 0 && at < redactAt {
			t.Fatalf("redactFormat must be installed BEFORE %s; found %s at %d, redaction at %d",
				serialiser, serialiser, at, redactAt)
		}
	}

	// Redaction must not be switchable. A logger that can be configured to leak
	// credentials eventually is. Scope this to createFormat's own body -- an
	// earlier version scanned from the top of the file and tripped on the
	// unrelated `if (!config)` default-config guard in install().
	bodyAt := strings.Index(cfg, "function createFormat(")
	if bodyAt < 0 || bodyAt > redactAt {
		t.Fatalf("expected redactFormat to be installed inside createFormat")
	}
	if strings.Contains(cfg[bodyAt:redactAt], "if (") {
		t.Fatalf("redactFormat must be installed unconditionally, not behind a branch")
	}
}

// The patterns must stay in step with the go chassis. Divergence is worse than
// absence: an operator who verified redaction in one language would reasonably
// assume the same behaviour in another.
func TestTypeScriptRedactionCoversBothHeaderAndJSONShapes(t *testing.T) {
	impl := readTSTemplate(t, "typescript/chassis/logger/Redact.ts.tmpl")

	// The JSON-field shape is the one that was historically missed everywhere:
	// a structured field puts a CLOSING QUOTE before the colon, so a
	// `key\s*[:=]` pattern cannot match it.
	if !strings.Contains(impl, `("(?:authorization|(?:set-)?cookie|[\w][\w-]*[-_](?:token|secret))"\s*:\s*")`) {
		t.Fatalf("redaction must cover the JSON structured-field shape, not just header/text")
	}
	// Values must terminate at a quote, comma or brace. `\S+` runs to end of
	// line in compact JSON, swallowing every following field and leaving an
	// unparseable log line.
	//
	// Check only the pattern array, not the whole file: the surrounding comment
	// explains the \S+ hazard in prose, and scanning the file matched that
	// explanation rather than any actual regex.
	start := strings.Index(impl, "const credentialPatterns")
	end := strings.Index(impl, "REDACTED_PLACEHOLDER =")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("could not locate the credentialPatterns array")
	}
	if strings.Contains(impl[start:end], `\S+`) {
		t.Fatalf("credential values must not be captured with \\S+; it truncates compact JSON lines")
	}
	// Metadata must be walked. Redacting only `message` leaves credentials
	// passed as structured fields untouched.
	if !strings.Contains(impl, "function redactValue(") {
		t.Fatalf("redaction must walk metadata values, not only the message")
	}
	// Cycle guard: metadata routinely carries request/error objects with parent
	// references, and walking one without a guard hangs the logger.
	if !strings.Contains(impl, "WeakSet<object>") {
		t.Fatalf("metadata walk must guard against cycles")
	}
	// Module-level /g regexes carry lastIndex state; without a reset the same
	// input redacts differently on a second call.
	if !strings.Contains(impl, "pattern.lastIndex = 0;") {
		t.Fatalf("global regexes must have lastIndex reset so redaction is idempotent")
	}
}

// The logger chassis is NOT emitted: TypeScript generation is hard client-only
// (browser bundles) and the chassis logger is built on Winston, a Node library.
// The templates are latent, maintained for a future Node-side TS target.
//
// Pin that fact. Someone reading Redact.ts.tmpl sees a complete, correct
// redactor and reasonably concludes it protects generated clients today -- it
// does not. If logger/ is ever added to the emit allowlist this test fails,
// which is the moment to re-verify the redaction against a real generated
// bundle rather than assuming the template-level guards above are sufficient.
func TestTypeScriptLoggerChassisIsNotEmitted(t *testing.T) {
	src, err := os.ReadFile("libs.go")
	if err != nil {
		t.Fatalf("read libs.go: %v", err)
	}
	if strings.Contains(string(src), `"logger/WinstonConfigurator.ts.tmpl"`) {
		t.Fatalf("logger chassis is now emitted -- re-verify credential redaction " +
			"against a REAL generated bundle, and confirm Winston is acceptable " +
			"in a browser target before accepting this change")
	}
}
