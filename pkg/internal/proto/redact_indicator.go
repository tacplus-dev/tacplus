// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// The injected companion field number is parser.RedactIndicatorFieldBase + the
// source field's own number, so it is STABLE (tied to the source number), HIGH and
// collision-free (the reserved band is enforced by parser.ValidateReservedFieldNumbers,
// which rejects any author field >= the base), and REMOVED WITH THE FIELD.

var messageOpenRe = regexp.MustCompile(`^message\s+(\w+)\s*\{`)

// redactIndicator is one injected companion field.
type redactIndicator struct {
	name   string
	number int32
}

// InjectRedactIndicatorFields rewrites the proto text in tempProtoDir so every
// message with (spec.redact) fields gains a companion "<field>_redacted" bool
// (number redactIndicatorFieldBase + the source field number). A redacted scalar
// is indistinguishable from a genuine zero, so the sanitize transform sets this
// bool to true when it clears/anonymizes the field, letting the consumer tell a
// redacted value from an authentic empty one. Because the field is injected into
// the record's own proto BEFORE native compilation, it appears in every language's
// generated type (Go struct / Rust struct / Java class) for free — unset at rest,
// set only on the sanitized egress copy.
//
// inputDir is unused (kept for signature parity with the other proto injectors).
func InjectRedactIndicatorFields(tempProtoDir, inputDir string) error {
	_ = inputDir
	dp, err := parser.NewDescriptorParser(tempProtoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for redact-indicator injection: %w", err)
	}

	// relPath -> messageName -> injected indicators.
	perFile := map[string]map[string][]redactIndicator{}

	for relPath, fd := range dp.GetAllFiles() {
		if IsInfraProto(relPath) {
			continue
		}
		for _, msg := range fd.Messages {
			existingNames := map[string]bool{}
			existingNumbers := map[int32]bool{}
			for _, f := range msg.Fields {
				existingNames[f.Name] = true
				existingNumbers[f.Number] = true
			}
			var inds []redactIndicator
			for _, f := range msg.Fields {
				ann, ok := f.Annotations["redact"]
				if !ok || ann.StringValue == "" {
					continue
				}
				name := f.Name + "_redacted"
				number := int32(parser.RedactIndicatorFieldBase) + f.Number
				// A collision means an author already occupies the injected name or
				// number — fail loud rather than emit a proto that will not compile.
				if existingNames[name] {
					return fmt.Errorf("%s: message %s already declares a field %q; the data-entitlement redacted indicator for %q needs that name",
						relPath, msg.Name, name, f.Name)
				}
				if existingNumbers[number] {
					return fmt.Errorf("%s: message %s already uses field number %d, which the data-entitlement redacted indicator for %q requires",
						relPath, msg.Name, number, f.Name)
				}
				inds = append(inds, redactIndicator{name: name, number: number})
			}
			if len(inds) > 0 {
				if perFile[relPath] == nil {
					perFile[relPath] = map[string][]redactIndicator{}
				}
				perFile[relPath][msg.Name] = inds
			}
		}
	}

	for relPath, byMsg := range perFile {
		filePath := filepath.Join(tempProtoDir, relPath)
		content, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read %s for redact-indicator injection: %w", relPath, err)
		}
		rewritten, err := injectRedactIndicatorText(string(content), byMsg)
		if err != nil {
			return fmt.Errorf("%s: %w", relPath, err)
		}
		if err := os.WriteFile(filePath, []byte(rewritten), 0644); err != nil {
			return fmt.Errorf("write %s after redact-indicator injection: %w", relPath, err)
		}
	}
	return nil
}

// injectRedactIndicatorText inserts the companion bool declarations before the
// closing brace of each named message. Messages are top-level (this repo declares
// no nested message types), so a message's closing brace is a "}" at column 0;
// field/message option blocks close with an INDENTED "}", so they are never
// mistaken for the message close.
func injectRedactIndicatorText(src string, byMsg map[string][]redactIndicator) (string, error) {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines)+4*len(byMsg))
	injected := map[string]bool{}
	current := ""

	for _, line := range lines {
		if current == "" {
			if m := messageOpenRe.FindStringSubmatch(line); m != nil {
				current = m[1]
			}
			out = append(out, line)
			continue
		}
		// Inside message `current`. Its close is a "}" at column 0.
		if strings.HasPrefix(line, "}") {
			if inds, ok := byMsg[current]; ok {
				out = append(out, "")
				out = append(out, "  // tacplus data-entitlement: per-field redacted indicators. Set true by")
				out = append(out, "  // the generated sanitize transform when the paired field is redacted;")
				out = append(out, "  // lets a consumer distinguish a redacted value from a genuine empty one.")
				for _, in := range inds {
					out = append(out, fmt.Sprintf("  bool %s = %d;", in.name, in.number))
				}
				injected[current] = true
			}
			current = ""
		}
		out = append(out, line)
	}

	// Every message we planned to inject into must have been found and closed.
	for name := range byMsg {
		if !injected[name] {
			return "", fmt.Errorf("message %s: could not locate its closing brace to inject redacted indicators", name)
		}
	}
	return strings.Join(out, "\n"), nil
}
