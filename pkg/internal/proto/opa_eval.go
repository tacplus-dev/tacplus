// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"go.uber.org/zap"
)

// opaEvalDef represents a single OPA eval definition found via spec.opa_eval.
type opaEvalDef struct {
	inputMsg   string // e.g. "EntitlementEvalInput"
	resultMsg  string // e.g. "EntitlementEvalResult"
	entryPoint string // e.g. "data.entitlements.profiles"
	pkg        string // e.g. "userentitlements"
	relPath    string // e.g. "opa/entitlements.proto"
}

// rpcName derives the RPC name from the input message name.
// Strips "EvalInput" or "Input" suffix, prepends "Eval".
// e.g. "EntitlementEvalInput" → "EvalEntitlement"
func (d opaEvalDef) rpcName() string {
	name := d.inputMsg
	name = strings.TrimSuffix(name, "EvalInput")
	name = strings.TrimSuffix(name, "Input")
	return "Eval" + name
}

// GenerateOpaEvalProto scans all proto files for spec.opa_eval annotations
// and appends an OpaEval service to each source proto file that defines them.
// The service lives in the same package as the annotated messages, so there is
// no separate generated/opa_eval.proto — the service is part of the vservices proto.
//
// Returns all source proto paths (e.g. ["vservices/backend-token.proto",
// "vservices/entitlements.proto"]) sorted alphabetically, or nil if none exist.
// Each path corresponds to a distinct OpaEval gRPC service in its own package.
func GenerateOpaEvalProto(tempProtoDir, inputDir string) ([]string, error) {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser for opa eval generation: %w", err)
	}

	// Collect all opa_eval definitions grouped by source file.
	type fileGroup struct {
		relPath string
		pkg     string
		defs    []opaEvalDef
	}
	var fileOrder []string
	fileMap := make(map[string]*fileGroup)

	for relPath, fd := range dp.GetAllFiles() {
		if IsInfraProto(relPath) {
			continue
		}
		for _, msg := range fd.Messages {
			entryPoint, ok := msg.Annotations["opa_eval"]
			if !ok || entryPoint.StringValue == "" {
				continue
			}
			resultAnn, hasResult := msg.Annotations["opa_eval_result"]
			if !hasResult || resultAnn.StringValue == "" {
				// Fail loud, never silently skip: a message that declares (spec.opa_eval)
				// but no (spec.opa_eval_result) is a misconfiguration — silently dropping
				// it would emit a service missing the OpaEval RPC the annotation asks for.
				return nil, fmt.Errorf(
					"message %q in %s declares (spec.opa_eval) but is missing (spec.opa_eval_result); "+
						"an OPA eval entry point requires a result message — add (spec.opa_eval_result) or remove (spec.opa_eval)",
					msg.Name, relPath)
			}
			if fileMap[relPath] == nil {
				fileMap[relPath] = &fileGroup{relPath: relPath, pkg: fd.Package}
				fileOrder = append(fileOrder, relPath)
			}
			fileMap[relPath].defs = append(fileMap[relPath].defs, opaEvalDef{
				inputMsg:   msg.Name,
				resultMsg:  resultAnn.StringValue,
				entryPoint: entryPoint.StringValue,
				pkg:        fd.Package,
				relPath:    relPath,
			})
		}
	}
	if len(fileOrder) == 0 {
		return nil, nil
	}
	sort.Strings(fileOrder)

	for _, relPath := range fileOrder {
		g := fileMap[relPath]
		sort.Slice(g.defs, func(i, j int) bool { return g.defs[i].inputMsg < g.defs[j].inputMsg })
		if err := appendOpaEvalToSourceFile(tempProtoDir, g.relPath, g.defs); err != nil {
			return nil, err
		}
	}

	return fileOrder, nil
}

// IsOpaServer checks whether the service identified by selfPackage is the
// OPA evaluator (imports sys/opalib.proto). Returns true if the service should
// register the OpaEval gRPC server.
func IsOpaServer(inputDir, selfPackage string) (bool, error) {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return false, fmt.Errorf("failed to create descriptor parser for OPA server check: %w", err)
	}
	// GetAllFiles returns a map, so its iteration order is nondeterministic. The
	// old code broke at the FIRST service-bearing file in selfPackage and only
	// checked THAT file's imports — a package whose service declaration and
	// sys/opalib.proto import live in different files would flip its answer with
	// map order. Accumulate across every file in the package instead: the OpaEval
	// server registers when the package has a service AND imports opalib anywhere.
	hasService := false
	importsOpalib := false
	for _, fd := range dp.GetAllFiles() {
		if fd.Package != selfPackage {
			continue
		}
		if fd.ServiceName != "" {
			hasService = true
		}
		for _, imp := range fd.Imports {
			if imp == "sys/opalib.proto" {
				importsOpalib = true
				break
			}
		}
	}
	return hasService && importsOpalib, nil
}

// appendOpaEvalToSourceFile appends the OpaEval service to the source proto file
// in tempProtoDir. Messages are in the same package so unqualified names are used.
func appendOpaEvalToSourceFile(tempProtoDir, sourceRelPath string, defs []opaEvalDef) error {
	filePath := filepath.Join(tempProtoDir, sourceRelPath)
	existing, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", sourceRelPath, err)
	}
	content := string(existing)

	// Ensure sys/spec.proto is imported (needed for spec.service_s2s option).
	content = ensureImport(content, "sys/spec.proto")

	// Build service block using unqualified message names (same package).
	var sb strings.Builder
	sb.WriteString("\n// Generated by tacplus from spec.opa_eval annotations.\n")
	sb.WriteString("// tacplus:generated-service\n")
	sb.WriteString("service OpaEval {\n")
	sb.WriteString("  option (spec.service_s2s) = {};\n")
	for _, d := range defs {
		sb.WriteString(fmt.Sprintf("\n  // entry_point: %s\n", d.entryPoint))
		sb.WriteString(fmt.Sprintf("  rpc %s(%s) returns (%s);\n", d.rpcName(), d.inputMsg, d.resultMsg))
	}
	sb.WriteString("}\n")

	content += sb.String()
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("appending OpaEval to %s: %w", sourceRelPath, err)
	}

	zap.L().Info("appended OpaEval service to source proto",
		zap.String("file", sourceRelPath),
		zap.Int("rpcs", len(defs)))
	return nil
}

// ensureImport adds an import statement to proto content if not already present.
func ensureImport(content, importPath string) string {
	if strings.Contains(content, fmt.Sprintf("%q", importPath)) {
		return content
	}
	// Insert after the last existing import line.
	lastImport := strings.LastIndex(content, "import \"")
	if lastImport < 0 {
		// No imports yet — insert after package declaration.
		pkgIdx := strings.Index(content, "package ")
		if pkgIdx >= 0 {
			end := strings.Index(content[pkgIdx:], "\n")
			if end >= 0 {
				insertAt := pkgIdx + end + 1
				return content[:insertAt] + fmt.Sprintf("import %q;\n", importPath) + content[insertAt:]
			}
		}
		return content
	}
	end := strings.Index(content[lastImport:], "\n")
	if end < 0 {
		return content + fmt.Sprintf("\nimport %q;\n", importPath)
	}
	insertAt := lastImport + end + 1
	return content[:insertAt] + fmt.Sprintf("import %q;\n", importPath) + content[insertAt:]
}

// CollectOpaEvalDefs returns the list of OPA eval definitions found in the
// proto directory. Used by SM generators to create typed client wrappers
// for services that declare spec.opa_import.
func CollectOpaEvalDefs(inputDir string) ([]OpaEvalInfo, error) {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser for opa eval collection: %w", err)
	}

	var infos []OpaEvalInfo
	for relPath, fd := range dp.GetAllFiles() {
		if IsInfraProto(relPath) {
			continue
		}
		for _, msg := range fd.Messages {
			entryPoint, ok := msg.Annotations["opa_eval"]
			if !ok || entryPoint.StringValue == "" {
				continue
			}
			resultAnn, hasResult := msg.Annotations["opa_eval_result"]
			if !hasResult || resultAnn.StringValue == "" {
				continue
			}
			rpcName := strings.TrimSuffix(msg.Name, "EvalInput")
			rpcName = strings.TrimSuffix(rpcName, "Input")
			rpcName = "Eval" + rpcName
			infos = append(infos, OpaEvalInfo{
				InputMsg:   msg.Name,
				ResultMsg:  resultAnn.StringValue,
				EntryPoint: entryPoint.StringValue,
				Pkg:        fd.Package,
				RelPath:    relPath,
				RPCName:    rpcName,
				InputFQN:   fd.Package + "." + msg.Name,
				ResultFQN:  fd.Package + "." + resultAnn.StringValue,
			})
		}
	}

	sort.Slice(infos, func(i, j int) bool {
		return infos[i].InputMsg < infos[j].InputMsg
	})

	return infos, nil
}

// OpaEvalInfo holds the resolved metadata for a single OPA eval definition.
// Exported for use by SM generators.
type OpaEvalInfo struct {
	InputMsg   string // e.g. "EntitlementEvalInput"
	ResultMsg  string // e.g. "EntitlementEvalResult"
	EntryPoint string // e.g. "data.entitlements.profiles"
	Pkg        string // e.g. "userentitlements"
	RelPath    string // e.g. "opa/entitlements.proto"
	RPCName    string // e.g. "EvalEntitlement"
	InputFQN   string // e.g. "opa_entitlements.EntitlementEvalInput"
	ResultFQN  string // e.g. "opa_entitlements.EntitlementEvalResult"
}
