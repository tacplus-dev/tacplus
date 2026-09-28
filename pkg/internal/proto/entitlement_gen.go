// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// AppendEntitlementMessagesToProtos generates, for every entitlement-relevant
// message, a parallel "<T>Entitlement" proto message carrying the caller's per-field
// data-entitlement decision. Emitted as PROTO (before native compilation) so the
// entitlement type lands in every language for free, portable across the wire —
// then the per-language Sanitize<T> functions operate on it.
//
// Per relevant message T, <T>Entitlement holds:
//   - entitlement.EntitlementLevel <field> for each caller-decided (may_*) field,
//     numbered with the source field's own number;
//   - entitlement.EntitlementLevel <field> for a caller-decided message-typed field
//     (whole-message level);
//   - <Sub>Entitlement <field>_sub for a message-typed field whose subtree is
//     itself relevant (field-by-field recursion), numbered RedactIndicatorFieldBase
//   - the source number (same reserved band as the injected indicator).
//
// Hard-floor (is_*) fields carry no caller field — they are always applied — so a
// message with only is_* fields yields an (empty) <T>Entitlement, still paired with
// a Sanitize<T> that applies them. inputDir is unused (signature parity).
func AppendEntitlementMessagesToProtos(tempProtoDir, inputDir string) error {
	_ = inputDir
	dp, err := parser.NewDescriptorParser(tempProtoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for entitlement generation: %w", err)
	}

	// Build model files (for the resolver) keyed by relPath.
	fileByPath := map[string]model.ProtoFile{}
	var relPaths []string
	for relPath := range dp.GetAllFiles() {
		if IsInfraProto(relPath) {
			continue
		}
		mf, err := dp.ConvertToProtoFile(relPath)
		if err != nil {
			return fmt.Errorf("convert %s for entitlement generation: %w", relPath, err)
		}
		fileByPath[relPath] = mf
		relPaths = append(relPaths, relPath)
	}
	sort.Strings(relPaths)

	all := make([]model.ProtoFile, 0, len(fileByPath))
	for _, rp := range relPaths {
		all = append(all, fileByPath[rp])
	}
	resolver := parser.NewMessageResolver(all)

	for _, relPath := range relPaths {
		mf := fileByPath[relPath]
		var block strings.Builder
		emitted := false
		usesLevel := false
		for mi := range mf.Messages {
			msg := &mf.Messages[mi]
			tree := parser.BuildEntitlementTree(msg, mf.Package, resolver)
			if !tree.IsRelevant() {
				continue
			}
			if emitEntitlementMessage(&block, tree) {
				usesLevel = true
			}
			emitted = true
		}
		if !emitted {
			continue
		}
		filePath := filepath.Join(tempProtoDir, relPath)
		content, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read %s for entitlement generation: %w", relPath, err)
		}
		// Only import sys/entitlement.proto when an EntitlementLevel field is actually
		// emitted. A record with only hard-floor (is-*) fields yields empty
		// <T>Entitlement messages that reference no shared type — importing it there
		// would be an unused import (which strict protoc/plugin configs reject).
		if usesLevel && !strings.Contains(string(content), `"sys/entitlement.proto"`) {
			content = injectImports(content, []string{`import "sys/entitlement.proto";`})
		}
		out := string(content) + "\n// Auto-generated data-entitlement structures (tacplus)\n" + block.String()
		if err := os.WriteFile(filePath, []byte(out), 0644); err != nil {
			return fmt.Errorf("write %s after entitlement generation: %w", relPath, err)
		}
	}
	return nil
}

// emitEntitlementMessage writes one "<T>Entitlement" proto message from a tree and
// reports whether it emitted any entitlement.EntitlementLevel field (so the caller
// knows whether the sys/entitlement.proto import is needed).
func emitEntitlementMessage(b *strings.Builder, node *parser.EntitlementNode) (usesLevel bool) {
	fmt.Fprintf(b, "\nmessage %sEntitlement {\n", node.Message.Name)
	for _, ef := range node.Fields {
		callerDecided := ef.Mode == "may_redact" || ef.Mode == "may_anonymize"
		if callerDecided {
			// A caller-decided level for this field (leaf value or whole-message).
			fmt.Fprintf(b, "  entitlement.EntitlementLevel %s = %d;\n", ef.Field.Name, ef.Field.Number)
			usesLevel = true
		}
		// Field-by-field recursion into a same-package nested message.
		if ef.Field.IsMessage && ef.Nested != nil && sameEntitlementPackage(ef, node.Package) {
			subType := ef.Field.Type + "Entitlement"
			fmt.Fprintf(b, "  %s %s_sub = %d;\n", subType, ef.Field.Name, int(parser.RedactIndicatorFieldBase)+int(ef.Field.Number))
		}
	}
	b.WriteString("}\n")
	return usesLevel
}

// sameEntitlementPackage reports whether a message-typed field's target lives in
// the same package (so its <Inner>Entitlement is referable unqualified). Recursion
// into a cross-package message is whole-message-only.
func sameEntitlementPackage(ef parser.EntitlementField, pkg string) bool {
	return ef.Field.TypePackage == "" || ef.Field.TypePackage == pkg
}
