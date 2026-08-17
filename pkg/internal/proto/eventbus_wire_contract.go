// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// The EventBus wire contract has two independent sources of truth:
//
//   - the CANONICAL eventbus.proto, authored in the platform proto repo, which
//     is what the Java broker actually serves and returns on the wire; and
//   - the SYNTHESIZED eventbus_support.proto that tacplus writes
//     (EnsureEventBusSupportProto), which is what every generated per-topic
//     publisher/subscriber client encodes and decodes with.
//
// Nothing structural links them: they live in different repos and are compiled
// into different binaries. When they drift, generation stays green, every
// tacplus gate stays green, and the failure only appears as a live client that
// cannot decode a server response. That has now happened twice — most recently
// PublishResponse field 2 being `string error` client-side and `int64 offset`
// server-side, which put the generated publisher into a permanent reconnect
// loop that only a pod-log read caught.
//
// This file is the gate that closes that gap. The canonical proto is already a
// build INPUT for every service that publishes events (it ships in proto-base),
// so the comparison needs no cross-repo fetch: both artifacts are on disk at
// generation time. Any drift in a field's name, number or type fails the build.

const (
	canonicalEventBusPackage = "eventbus"
	supportEventBusPackage   = "eventbus_support"
)

// wireCompareMode says how strictly a synthesized message must track canonical.
type wireCompareMode int

const (
	// wireExact: the message travels SERVER→CLIENT, so the synthesized shape
	// must match canonical field-for-field. Any extra, missing, renumbered or
	// retyped field means a response the client cannot decode.
	wireExact wireCompareMode = iota
	// wireSubset: the message travels CLIENT→SERVER. The client may omit
	// capabilities it does not expose, but every field it DOES send must carry
	// the canonical number and type.
	wireSubset
)

type wireContractRule struct {
	message string
	mode    wireCompareMode
	// reason is printed on failure so the message says why it matters, not
	// just that two files differ.
	reason string
}

// eventBusWireContract lists every message the synthesized support proto shares
// with the canonical eventbus proto. A message added to eventbus_support.proto
// that also exists in eventbus.proto MUST get an entry here.
var eventBusWireContract = []wireContractRule{
	{
		message: "PublishResponse",
		mode:    wireExact,
		reason:  "the broker returns this on every publish; a generated publisher that cannot decode it reconnects forever",
	},
	{
		message: "RecordMeta",
		mode:    wireExact,
		reason:  "the broker stamps this onto every delivered event; a generated subscriber decodes it",
	},
	{
		message: "SubscribeRequest",
		mode:    wireSubset,
		reason:  "generated subscribers send this to the broker; omitted capabilities are fine, wrong numbers/types are not",
	},
}

// ValidateEventBusSupportWireContract compares the synthesized EventBus support
// proto that generation is about to compile against the canonical eventbus
// proto in the source tree, and fails loud on any drift.
//
// supportProtoDir/supportRelPath locate the support proto as EnsureEventBusSupportProto
// left it — which is deliberately the file on DISK, not a freshly rendered
// string, so a stale checked-in copy is caught as well as a stale generator.
//
// sourceProtoDir is the flattened input tree. If it carries no canonical
// eventbus proto there is nothing to compare against and the check is skipped.
func ValidateEventBusSupportWireContract(supportProtoDir, supportRelPath, sourceProtoDir string) error {
	if supportProtoDir == "" || supportRelPath == "" || sourceProtoDir == "" {
		return nil
	}

	canonicalPaths, canonical, err := findCanonicalEventBusContract(sourceProtoDir)
	if err != nil {
		return err
	}
	if len(canonicalPaths) == 0 {
		zap.L().Info("EventBus wire contract check skipped: source tree declares no canonical eventbus wire contract",
			zap.String("source_proto_dir", sourceProtoDir))
		return nil
	}

	support, err := eventBusMessages(supportProtoDir, []string{filepath.ToSlash(supportRelPath)}, supportEventBusPackage)
	if err != nil {
		return fmt.Errorf("eventbus wire contract: parse synthesized %s: %w", supportRelPath, err)
	}

	var problems []string
	for _, rule := range eventBusWireContract {
		syn, haveSyn := support[rule.message]
		can, haveCan := canonical[rule.message]
		if !haveSyn {
			// The support proto is free not to carry a message at all.
			continue
		}
		if !haveCan {
			problems = append(problems, fmt.Sprintf(
				"%s: synthesized %s.%s has no counterpart in canonical package %s (%s)",
				rule.message, supportEventBusPackage, rule.message, canonicalEventBusPackage, rule.reason))
			continue
		}
		if diffs := compareWireShape(syn, can, rule.mode); len(diffs) > 0 {
			problems = append(problems, fmt.Sprintf("%s — %s:\n    %s",
				rule.message, rule.reason, strings.Join(diffs, "\n    ")))
		}
	}
	if len(problems) == 0 {
		zap.L().Info("EventBus wire contract verified against canonical proto",
			zap.Strings("canonical", canonicalPaths),
			zap.String("synthesized", filepath.ToSlash(supportRelPath)))
		return nil
	}

	return fmt.Errorf(`EventBus wire contract drift: the synthesized %s does not match the canonical eventbus proto.

Generated clients encode/decode with the synthesized proto; the broker uses the
canonical one. These MUST agree on the wire.

  synthesized: %s (in %s)
  canonical:   %s (in %s)

%s

Fix the side that is wrong — tacplus's EnsureEventBusSupportProto
(pkg/internal/proto/eventbus_support.go) or the canonical eventbus.proto — and
regenerate. Do not silence this check`,
		filepath.ToSlash(supportRelPath),
		filepath.ToSlash(supportRelPath), supportProtoDir,
		strings.Join(canonicalPaths, ", "), sourceProtoDir,
		strings.Join(problems, "\n\n"))
}

// isInjectedTraceField reports whether a field was put there by tacplus's own
// trace injection rather than authored in either proto. Injection runs over the
// service tree but not over the synthesized support proto, so these fields are
// present on one side by construction and are not part of the contract the two
// repos have to agree on.
func isInjectedTraceField(f parser.FieldDescriptor) bool {
	return (f.Number == traceIDFieldNumber && f.Name == traceIDFieldName) ||
		(f.Number == noopFieldNumber && f.Name == noopFieldName)
}

func authoredFields(fields []parser.FieldDescriptor) []parser.FieldDescriptor {
	out := make([]parser.FieldDescriptor, 0, len(fields))
	for _, f := range fields {
		if isInjectedTraceField(f) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// compareWireShape returns a human-readable list of the differences between a
// synthesized message and its canonical counterpart.
func compareWireShape(syn, can parser.MessageDescriptor, mode wireCompareMode) []string {
	synFields := authoredFields(syn.Fields)
	canFields := authoredFields(can.Fields)

	canByNumber := make(map[int32]parser.FieldDescriptor, len(canFields))
	for _, f := range canFields {
		canByNumber[f.Number] = f
	}
	synByNumber := make(map[int32]parser.FieldDescriptor, len(synFields))
	for _, f := range synFields {
		synByNumber[f.Number] = f
	}

	var diffs []string
	for _, sf := range synFields {
		cf, ok := canByNumber[sf.Number]
		if !ok {
			diffs = append(diffs, fmt.Sprintf(
				"field %d (%s %s) exists only in the synthesized proto; canonical has no field %d",
				sf.Number, wireTypeOf(sf), sf.Name, sf.Number))
			continue
		}
		if wireTypeOf(sf) != wireTypeOf(cf) {
			diffs = append(diffs, fmt.Sprintf(
				"field %d type mismatch: synthesized %s %s, canonical %s %s",
				sf.Number, wireTypeOf(sf), sf.Name, wireTypeOf(cf), cf.Name))
			continue
		}
		if sf.Name != cf.Name {
			diffs = append(diffs, fmt.Sprintf(
				"field %d name mismatch: synthesized %q, canonical %q",
				sf.Number, sf.Name, cf.Name))
		}
	}
	if mode == wireExact {
		for _, cf := range canFields {
			if _, ok := synByNumber[cf.Number]; !ok {
				diffs = append(diffs, fmt.Sprintf(
					"field %d (%s %s) exists only in the canonical proto; the synthesized message must carry it",
					cf.Number, wireTypeOf(cf), cf.Name))
			}
		}
	}

	sort.Strings(diffs)
	return diffs
}

// wireTypeOf renders a field's type the way it would be written in a proto,
// which is exactly the granularity that matters on the wire.
func wireTypeOf(f parser.FieldDescriptor) string {
	t := f.Type
	if f.IsMap {
		t = fmt.Sprintf("map<%s, %s>", f.MapKeyType, f.MapValueType)
	} else if f.IsRepeated {
		t = "repeated " + t
	}
	return t
}

// findCanonicalEventBusContract locates the protos in the source tree that
// actually carry the canonical EventBus wire contract, and returns their
// source-relative paths together with every message they declare.
//
// The package name alone does NOT identify them. `eventbus` is the broker's
// service identity (eventbus.EventBusTag), so any proto that stands in for the
// broker declares that package — including presence-only stubs such as
// cross-test/consumer-contract's contracteventbus.proto, which exists purely so
// BuildTopicRegistry can resolve the broker tag and defines no wire messages at
// all. Treating such a stub as canonical failed generation for every message in
// eventBusWireContract as "missing" from a proto that never claimed to define
// them.
//
// A proto is canonical when it declares package eventbus AND declares at least
// one of the wire contract messages — i.e. it makes a claim about the wire shape
// that the synthesized proto has to agree with. Once a file is selected, every
// contract rule is enforced against it as before: a canonical proto that carries
// RecordMeta but drops PublishResponse is still drift, and is still fatal.
func findCanonicalEventBusContract(sourceProtoDir string) ([]string, map[string]parser.MessageDescriptor, error) {
	candidates, err := findEventBusPackageProtos(sourceProtoDir)
	if err != nil {
		return nil, nil, err
	}
	if len(candidates) == 0 {
		return nil, nil, nil
	}

	p, err := parser.NewDescriptorParserForFiles(sourceProtoDir, candidates)
	if err != nil {
		return nil, nil, fmt.Errorf("eventbus wire contract: parse canonical proto: %w", err)
	}

	contractMessages := make(map[string]bool, len(eventBusWireContract))
	for _, rule := range eventBusWireContract {
		contractMessages[rule.message] = true
	}

	var paths []string
	messages := make(map[string]parser.MessageDescriptor)
	for relPath, fd := range p.GetAllFiles() {
		if fd == nil || fd.Package != canonicalEventBusPackage {
			continue
		}
		carriesContract := false
		for _, msg := range fd.Messages {
			if contractMessages[msg.Name] {
				carriesContract = true
				break
			}
		}
		if !carriesContract {
			zap.L().Info("EventBus wire contract: proto declares package eventbus but no contract message; not canonical",
				zap.String("proto", filepath.ToSlash(relPath)))
			continue
		}
		paths = append(paths, filepath.ToSlash(relPath))
		for _, msg := range fd.Messages {
			messages[msg.Name] = msg
		}
	}
	sort.Strings(paths)
	return paths, messages, nil
}

// findEventBusPackageProtos returns the source-relative paths of every proto in
// the tree that declares the `eventbus` package. This is the cheap prefilter for
// findCanonicalEventBusContract, not the identification itself. The synthesized
// support proto declares package eventbus_support, so it can never match.
func findEventBusPackageProtos(sourceProtoDir string) ([]string, error) {
	var paths []string
	err := filepath.Walk(sourceProtoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".proto") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !declaresPackage(string(data), canonicalEventBusPackage) {
			return nil
		}
		rel, relErr := filepath.Rel(sourceProtoDir, path)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("eventbus wire contract: scan %s: %w", sourceProtoDir, err)
	}
	sort.Strings(paths)
	return paths, nil
}

// declaresPackage reports whether proto text declares exactly the given package.
func declaresPackage(text, pkg string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "//") {
			continue
		}
		if line == "package "+pkg+";" {
			return true
		}
	}
	return false
}

// eventBusMessages parses the named protos and returns every top-level message
// declared in the given package, keyed by message name.
func eventBusMessages(protoDir string, relPaths []string, pkg string) (map[string]parser.MessageDescriptor, error) {
	p, err := parser.NewDescriptorParserForFiles(protoDir, relPaths)
	if err != nil {
		return nil, err
	}
	out := make(map[string]parser.MessageDescriptor)
	for _, fd := range p.GetAllFiles() {
		if fd.Package != pkg {
			continue
		}
		for _, msg := range fd.Messages {
			out[msg.Name] = msg
		}
	}
	return out, nil
}
