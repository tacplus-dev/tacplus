// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

const traceImport = `import "sys/trace.proto";`

// The trace contract is a single source of truth shared by emission
// (TraceFieldsProto) and detection (InjectTraceFields idempotency check), so
// the field numbers/names cannot drift between the two (OF-7).
const (
	traceIDFieldNumber = parser.TraceIDFieldNumber
	noopFieldNumber    = 201
	traceIDFieldName   = parser.TraceIDFieldName
	noopFieldName      = "noop"
)

// IsTraceField reports whether a message field is one of the two tacplus
// injects into every user-defined message. Name and number are both checked
// because InjectTraceFields skips a message that already defines either name at
// a different number, and such a field is the service author's own.
//
// Callers are generators that must not publish the injected fields as part of a
// message's user-facing contract — the MCP tool schema is the first.
func IsTraceField(name string, number int) bool {
	return (name == traceIDFieldName && number == traceIDFieldNumber) ||
		(name == noopFieldName && number == noopFieldNumber)
}

// IsNoopField reports whether a message field is the injected noop flag: the
// trace-round dry run, on which the record dispatch writes no rows.
func IsNoopField(name string, number int) bool {
	return name == noopFieldName && number == noopFieldNumber
}

// TraceFieldsProto is the proto-syntax field definitions for trace fields.
// Used by InjectTraceFields and AppendStateServiceToProtos so auto-appended
// message types carry trace fields without needing a second injection pass.
var TraceFieldsProto = fmt.Sprintf("  string %s = %d;\n  bool %s = %d;\n",
	traceIDFieldName, traceIDFieldNumber, noopFieldName, noopFieldNumber)

// IsTraceExemptSysMessage reports whether a message of the sys contract
// (relPath under sys/) is one of the two kinds that carry no field 200:
// sys/spec.proto's option schemas, read by the compiler and never on the wire,
// and trace.TraceEvent, which carries the trace it reports as trace_id = 1.
func IsTraceExemptSysMessage(relPath, msgName string) bool {
	switch filepath.ToSlash(relPath) {
	case "sys/spec.proto":
		return true
	case "sys/trace.proto":
		return msgName == "TraceEvent"
	}
	return false
}

// InjectTraceFields injects trace fields into every user-defined message and
// every message of the sys contract but the IsTraceExemptSysMessage ones
// (#327) in the proto directory. Each message gets:
//
//	string trace_id = 200;
//	bool noop = 201;
//
// A non-empty trace_id activates tracing: each service publishes a TraceEvent
// to the trace topic as it processes the message. Herald routes events to the
// client stream that owns the trace_id. noop=true skips the handler (dry run).
//
// inputDir is the flat proto directory used for descriptor parsing.
// tempProtoDir is the working copy where modifications are written.
// SkippedTraceTypes returns from InjectTraceFields: set of fully-qualified
// message names that were skipped because they already define trace_id or noop.
type SkippedTraceTypes map[string]bool

func InjectTraceFields(tempProtoDir, inputDir string) (SkippedTraceTypes, error) {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser for trace injection: %w", err)
	}

	skipped := make(SkippedTraceTypes)

	// Collect all messages that need trace fields, grouped by file.
	type messageInfo struct {
		name string
	}
	fileMessages := make(map[string][]messageInfo)

	for relPath, fileDesc := range dp.GetAllFiles() {
		sysFile := IsSysProto(relPath)
		if IsInfraProtoFile(relPath, fileDesc) && !sysFile {
			continue
		}
		eventBusRPCMessages := eventBusTraceRPCMessages(relPath, fileDesc)

		// A generated <T>Public holds T's VISIBILITY_SHOW fields and nothing
		// else: a trace_id there would be copied from T by <T>ToPublic and handed
		// to the renderer. Recorded as skipped so nothing reads trace fields off it.
		publicViews := map[string]bool{}
		for _, msg := range fileDesc.Messages {
			if msg.Annotations["public_view"].BoolValue {
				publicViews[msg.Name+parser.PublicViewSuffix] = true
			}
		}

		for _, msg := range fileDesc.Messages {
			if isEventBusWireValueMessage(msg.Name) {
				continue
			}
			if publicViews[msg.Name] {
				skipped[fileDesc.Package+"."+msg.Name] = true
				continue
			}
			if eventBusRPCMessages != nil {
				if !eventBusRPCMessages[msg.Name] {
					continue
				}
			}
			if sysFile && IsTraceExemptSysMessage(relPath, msg.Name) {
				continue
			}
			// Check if trace fields already exist (idempotency).
			// Skip by trace field number (200/201) or by name (trace_id/noop)
			// to avoid conflicts with messages that already define trace_id.
			hasTrace, declaresTrace := false, false
			for _, f := range msg.Fields {
				if f.Number == traceIDFieldNumber || f.Number == noopFieldNumber || f.Name == traceIDFieldName || f.Name == noopFieldName {
					hasTrace = true
				}
				if f.Number == traceIDFieldNumber && f.Name == traceIDFieldName {
					declaresTrace = true
				}
			}
			// A sys message that declares trace_id = 200 itself
			// (sys/tokenrequest.proto, trace.TraceRequest) is tacplus's own
			// declaration of the same field, not an author's: not skipped.
			if hasTrace && sysFile && declaresTrace {
				continue
			}
			if hasTrace {
				fqn := fileDesc.Package + "." + msg.Name
				skipped[fqn] = true
				continue
			}

			fileMessages[relPath] = append(fileMessages[relPath], messageInfo{
				name: msg.Name,
			})
		}
	}

	if len(fileMessages) == 0 {
		return skipped, nil
	}

	// Apply injections to proto files in tempProtoDir.
	for relPath, messages := range fileMessages {
		protoPath := filepath.Join(tempProtoDir, relPath)
		content, err := os.ReadFile(protoPath)
		if err != nil {
			return skipped, fmt.Errorf("failed to read proto file %s: %w", relPath, err)
		}
		text := string(content)

		// Inject trace fields into each message (process in reverse order
		// of position to avoid offset shifts).
		type injection struct {
			name string
			pos  int
		}
		var injections []injection
		for _, m := range messages {
			pos := findMessageClosingBrace(text, m.name)
			if pos < 0 {
				zap.L().Warn("Could not find message definition for trace injection",
					zap.String("message", m.name), zap.String("file", relPath))
				continue
			}
			injections = append(injections, injection{name: m.name, pos: pos})
		}

		// Sort by position descending so insertions don't shift earlier positions.
		for i := 0; i < len(injections); i++ {
			for j := i + 1; j < len(injections); j++ {
				if injections[j].pos > injections[i].pos {
					injections[i], injections[j] = injections[j], injections[i]
				}
			}
		}

		var injectedNames []string
		for _, inj := range injections {
			text = text[:inj.pos] + TraceFieldsProto + text[inj.pos:]
			injectedNames = append(injectedNames, inj.name)
		}

		// trace_id and noop are primitive types — no trace import needed
		// for injected fields. SwapEmptyInputs adds the import when needed
		// for trace.TraceRequest.

		if err := os.WriteFile(protoPath, []byte(text), 0644); err != nil {
			return skipped, fmt.Errorf("failed to write modified proto %s: %w", relPath, err)
		}

		zap.L().Info("Injected trace fields into proto file",
			zap.String("file", relPath),
			zap.Strings("messages", injectedNames))
	}

	return skipped, nil
}

// CarriesInjectedTrace reports whether the message typeFqn (e.g.
// "ballsports.Player"), as parsed by dp from the post-injection proto
// directory, carries the trace_id InjectTraceFields added. False for a type
// that is not found, lives in an infra proto outside sys/ (never injected), was recorded in
// skipped (a <T>Public view, or a message defining its own trace_id/noop), or
// simply has no trace_id at field 200.
//
// Storage paths use it to decide, at generation time, whether to clear the
// trace before persisting: a stored value never keeps a trace (#313).
func CarriesInjectedTrace(dp *parser.DescriptorParser, typeFqn string, skipped map[string]bool) bool {
	if dp == nil || typeFqn == "" || skipped[typeFqn] {
		return false
	}
	idx := strings.LastIndex(typeFqn, ".")
	if idx <= 0 {
		return false
	}
	pkg, name := typeFqn[:idx], typeFqn[idx+1:]
	for relPath, fileDesc := range dp.GetAllFiles() {
		if fileDesc == nil || fileDesc.Package != pkg {
			continue
		}
		for i := range fileDesc.Messages {
			msg := &fileDesc.Messages[i]
			if msg.Name != name {
				continue
			}
			if IsInfraProtoFile(relPath, fileDesc) && !IsSysProto(relPath) {
				return false
			}
			for _, f := range msg.Fields {
				if f.Number == traceIDFieldNumber && f.Name == traceIDFieldName {
					return true
				}
			}
			return false
		}
	}
	return false
}

// FieldTypeCarriesInjectedTrace is CarriesInjectedTrace for a message field's
// type as the descriptor parser reports it: bare ("Player", resolved in
// filePackage) or package-qualified ("ballsports.Player").
func FieldTypeCarriesInjectedTrace(dp *parser.DescriptorParser, filePackage, fieldType string, skipped map[string]bool) bool {
	fieldType = strings.TrimPrefix(fieldType, ".")
	if fieldType == "" {
		return false
	}
	if !strings.Contains(fieldType, ".") {
		fieldType = filePackage + "." + fieldType
	}
	return CarriesInjectedTrace(dp, fieldType, skipped)
}

func eventBusTraceRPCMessages(relPath string, fileDesc *parser.FileDescriptor) map[string]bool {
	if fileDesc == nil || !IsEventBusSupportProto(relPath) {
		return nil
	}
	names := make(map[string]bool)
	for _, svc := range fileDesc.Services {
		for _, method := range svc.Methods {
			addTraceRPCMessage(names, method.InputType)
			addTraceRPCMessage(names, method.OutputType)
		}
	}
	return names
}

func addTraceRPCMessage(names map[string]bool, typeName string) {
	typeName = strings.TrimPrefix(typeName, ".")
	if typeName == "" || strings.HasPrefix(typeName, "google.protobuf.") || strings.HasPrefix(typeName, "trace.") {
		return
	}
	if idx := strings.LastIndex(typeName, "."); idx >= 0 {
		typeName = typeName[idx+1:]
	}
	names[typeName] = true
}

func isEventBusWireValueMessage(name string) bool {
	return name == "EventBusRecordMeta" || strings.HasSuffix(name, "SubscribeRecord")
}

// addImport inserts an import line after the last existing import statement,
// or after the package declaration if there are no imports.
func addImport(text, importLine string) string {
	lines := strings.Split(text, "\n")
	lastImportIdx := -1
	packageIdx := -1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import ") {
			lastImportIdx = i
		}
		if strings.HasPrefix(trimmed, "package ") {
			packageIdx = i
		}
	}

	insertAfter := lastImportIdx
	if insertAfter < 0 {
		insertAfter = packageIdx
	}
	if insertAfter < 0 {
		// Fallback: insert at the top.
		return importLine + "\n" + text
	}

	// Insert after the target line.
	result := make([]string, 0, len(lines)+1)
	result = append(result, lines[:insertAfter+1]...)
	result = append(result, importLine)
	result = append(result, lines[insertAfter+1:]...)
	return strings.Join(result, "\n")
}

// rpcEmptyInput matches RPC definitions with google.protobuf.Empty as the input type.
var rpcEmptyInput = regexp.MustCompile(`(rpc\s+\w+\s*)\(google\.protobuf\.Empty\)`)

// SwapEmptyInputs replaces google.protobuf.Empty input types with trace.TraceRequest
// in all user-defined service proto files. This ensures every RPC — including read-only
// endpoints that take no parameters — can carry trace fields on the wire.
// The handler interface stays arg-free; the wrapper handles TraceRequest transparently.
//
// Must be called after InjectTraceFields (which adds the trace import to files with
// messages). For files that only have RPCs (no messages), the trace import is added here.
func SwapEmptyInputs(protoDir string) error {
	return filepath.Walk(protoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}

		relPath, err := filepath.Rel(protoDir, path)
		if err != nil {
			return err
		}
		if IsInfraProto(relPath) {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(content)

		if !rpcEmptyInput.MatchString(text) {
			return nil
		}

		// Replace (google.protobuf.Empty) with (trace.TraceRequest) in input position
		newText := rpcEmptyInput.ReplaceAllString(text, "${1}(trace.TraceRequest)")

		// Add trace import if not already present
		if !strings.Contains(newText, traceImport) {
			newText = addImport(newText, traceImport)
		}

		// Remove google/protobuf/empty.proto import if no remaining references
		if !strings.Contains(newText, "google.protobuf.Empty") {
			newText = strings.Replace(newText, "import \"google/protobuf/empty.proto\";\n", "", 1)
		}

		if newText != text {
			if err := os.WriteFile(path, []byte(newText), 0644); err != nil {
				return fmt.Errorf("failed to write %s: %w", relPath, err)
			}
			zap.L().Info("Swapped Empty inputs to TraceRequest", zap.String("file", relPath))
		}

		return nil
	})
}
