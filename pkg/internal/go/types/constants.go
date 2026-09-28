// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"io/fs"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/codefile"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/tmplutil"
	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// constantTemplateData holds pre-computed data for the constants template.
type constantTemplateData struct {
	Constants     []constantEntry
	FieldDefaults []defaultConstant
	Defaults      []defaultsFunc
}

// constantEntry holds a single constant for template rendering.
type constantEntry struct {
	GoName string
	Type   string
	Value  string
}

// defaultsFunc holds data for a TypeDefaults function.
type defaultsFunc struct {
	MsgName     string
	Assignments []string // pre-computed assignment statements
}

// writeConstants writes the constants section into the helpers builder using a template.
func writeConstants(b *strings.Builder, pf model.ProtoFile) {
	fieldDefaults := collectFieldDefaultConstants(pf)

	// Build constant entries
	var constants []constantEntry
	for _, c := range pf.Constants {
		constants = append(constants, constantEntry{
			GoName: strutil.UpperSnakeCaseToPascalCase(c.Name),
			Type:   c.Type,
			Value:  c.Value,
		})
	}

	data := constantTemplateData{
		Constants:     constants,
		FieldDefaults: fieldDefaults,
	}

	typeFS, err := fs.Sub(templates.FS, "go/types")
	if err != nil {
		// Fallback to inline generation
		writeConstantsInline(b, pf, fieldDefaults)
		return
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "constants.go.tmpl", funcMap, data)
	if err != nil {
		// Fallback to inline generation
		writeConstantsInline(b, pf, fieldDefaults)
		return
	}

	b.Write(result)
}

// writeConstantsInline is the fallback inline generation for constants.
func writeConstantsInline(b *strings.Builder, pf model.ProtoFile, fieldDefaults []defaultConstant) {
	f := codefile.New(codefile.Go)

	f.Section("Constants")
	f.P("const (")
	f.Indent()
	for _, c := range pf.Constants {
		goName := strutil.UpperSnakeCaseToPascalCase(c.Name)
		if c.Type != "" {
			f.P("%s %s = %s", goName, c.Type, c.Value)
		} else {
			f.P("%s = %s", goName, c.Value)
		}
	}

	for _, dc := range fieldDefaults {
		f.P("%s %s = %s", dc.Name, dc.Type, dc.Value)
	}
	f.Dedent()
	f.P(")")

	b.WriteString(f.String())
}

// messagesWithDefaults computes the transitive set of message names that have defaults.
// A message has defaults if it has direct default annotations on its fields, if it
// has assert_required message fields (which need nil-initialization), or if it
// has a non-repeated, non-map, same-package message field whose type has defaults.
func messagesWithDefaults(pf model.ProtoFile) map[string]bool {
	direct := make(map[string]bool)
	for _, msg := range pf.Messages {
		for _, field := range msg.Fields {
			// Direct default annotations
			for _, key := range []string{"default_string", "default_int", "default_float", "default_bool", "default_const", "default_host"} {
				if _, ok := field.Annotations[key]; ok {
					direct[msg.Name] = true
					break
				}
			}
			if direct[msg.Name] {
				break
			}
			// assert_required on message fields — Defaults must initialize nil fields
			if field.IsMessage && !field.IsRepeated && !field.IsMap && !field.IsOneOf && field.TypePackage == "" {
				if _, ok := field.Annotations["assert_required"]; ok {
					direct[msg.Name] = true
					break
				}
			}
		}
	}

	// Fixed-point expansion
	changed := true
	for changed {
		changed = false
		for _, msg := range pf.Messages {
			if direct[msg.Name] {
				continue
			}
			for _, field := range msg.Fields {
				if field.IsMessage && !field.IsRepeated && !field.IsMap && field.TypePackage == "" && direct[field.Type] {
					direct[msg.Name] = true
					changed = true
					break
				}
			}
		}
	}

	return direct
}

// writeDefaults generates TypeDefaults functions for every message in the proto file.
// Each function mutates the message in place, setting zero-valued fields to their defaults.
// Messages without defaults get an empty function (nil guard + return) so callers can
// unconditionally call Defaults without checking whether the type has default annotations.
func writeDefaults(b *strings.Builder, pf model.ProtoFile) {
	hasDefaults := messagesWithDefaults(pf)
	dupFields := findDuplicateDefaultFieldsGo(pf.Messages)

	// Build lookup from file-level constant name → constant type (for default_const)
	constTypes := make(map[string]string) // e.g., "DEFAULT_PORT" → "int64"
	for _, c := range pf.Constants {
		constTypes[c.Name] = c.Type
	}

	f := codefile.New(codefile.Go)

	for _, msg := range pf.Messages {
		// Collect direct field defaults (literal default_int, default_string, etc.)
		var defaults []fieldDefault
		for _, field := range msg.Fields {
			for _, key := range []string{"default_string", "default_int", "default_float", "default_bool"} {
				if _, ok := field.Annotations[key]; ok {
					constName := strutil.UpperSnakeCaseToPascalCase(strutil.DefaultConstName(msg.Name, field.Name, dupFields))
					goFieldName := toGoFieldName(field.Name)
					defaults = append(defaults, fieldDefault{
						FieldName: goFieldName,
						ConstName: constName,
						AnnKey:    key,
						Field:     field,
					})
				}
			}
			// default_const: references a file-level constant by name
			if av, ok := field.Annotations["default_const"]; ok {
				refName := av.StringValue // e.g., "DEFAULT_PORT"
				goConstName := strutil.UpperSnakeCaseToPascalCase(refName)
				goFieldName := toGoFieldName(field.Name)
				// Determine the annKey equivalent from the constant's type
				annKey := constTypeToAnnKey(constTypes[refName])
				defaults = append(defaults, fieldDefault{
					FieldName: goFieldName,
					ConstName: goConstName,
					AnnKey:    annKey,
					Field:     field,
				})
			}
		}

		// Collect sub-message fields that have transitive defaults or assert_required
		type subMsgCall struct {
			GoField     string
			FuncName    string
			GoType      string // Go type name (for initializing nil fields)
			InitIfNil   bool   // true for assert_required fields — initialize if nil
			HasDefaults bool   // true if the sub-message has its own defaults function
		}
		var subMsgCalls []subMsgCall
		for _, field := range msg.Fields {
			// Skip oneof fields — they use interface types that can't be accessed as direct struct fields
			if field.IsOneOf || field.IsRepeated || field.IsMap {
				continue
			}
			if !field.IsMessage || field.TypePackage != "" {
				continue
			}

			_, isRequired := field.Annotations["assert_required"]
			transitive := hasDefaults[field.Type]

			if isRequired || transitive {
				subMsgCalls = append(subMsgCalls, subMsgCall{
					GoField:     toGoFieldName(field.Name),
					FuncName:    field.Type + "Defaults",
					GoType:      field.Type,
					InitIfNil:   isRequired,
					HasDefaults: transitive,
				})
			}
		}

		f.Section(fmt.Sprintf("TypeDefaults: %s", msg.Name))
		f.Comment("%sDefaults applies field-level default values to zero-valued fields.", msg.Name)
		f.Open("func %sDefaults(v *%s)", msg.Name, msg.Name)
		f.P("if v == nil { return }")

		for _, d := range defaults {
			castExpr := d.ConstName
			// Handle type casting for int64 constant → int32 field
			if d.AnnKey == "default_int" && (d.Field.Type == "int32" || d.Field.Type == "sint32" || d.Field.Type == "sfixed32") {
				castExpr = fmt.Sprintf("int32(%s)", d.ConstName)
			} else if d.AnnKey == "default_int" && (d.Field.Type == "uint32" || d.Field.Type == "fixed32") {
				castExpr = fmt.Sprintf("uint32(%s)", d.ConstName)
			} else if d.AnnKey == "default_int" && (d.Field.Type == "uint64" || d.Field.Type == "fixed64") {
				castExpr = fmt.Sprintf("uint64(%s)", d.ConstName)
			} else if d.AnnKey == "default_float" && d.Field.Type == "float" {
				castExpr = fmt.Sprintf("float32(%s)", d.ConstName)
			}

			switch d.AnnKey {
			case "default_string":
				f.P("if v.%s == \"\" {v.%s = %s}", d.FieldName, d.FieldName, castExpr)
			case "default_int":
				f.P("if v.%s == 0 {v.%s = %s}", d.FieldName, d.FieldName, castExpr)
			case "default_float":
				f.P("if v.%s == 0.0 {v.%s = %s}", d.FieldName, d.FieldName, castExpr)
			case "default_bool":
				f.P("if !v.%s {v.%s = %s}", d.FieldName, d.FieldName, castExpr)
			}
		}

		// default_host: initialize host.Host fields with default values
		for _, field := range msg.Fields {
			av, ok := field.Annotations["default_host"]
			if !ok || av.HostValue == nil {
				continue
			}
			goFieldName := toGoFieldName(field.Name)
			f.P("if v.%s == nil { v.%s = &%s.Host{Host: %q, Port: %d} }",
				goFieldName, goFieldName, field.TypePackage, av.HostValue.Host, av.HostValue.Port)
		}

		for _, sc := range subMsgCalls {
			if sc.InitIfNil {
				// assert_required message fields: initialize if nil, then apply defaults
				f.P("if v.%s == nil { v.%s = &%s{} }", sc.GoField, sc.GoField, sc.GoType)
				if sc.HasDefaults {
					f.P("%s(v.%s)", sc.FuncName, sc.GoField)
				}
			} else {
				// Optional message fields: only apply defaults if already set
				f.P("if v.%s != nil { %s(v.%s) }", sc.GoField, sc.FuncName, sc.GoField)
			}
		}

		f.Close()
	}

	b.WriteString(f.String())
}

// constTypeToAnnKey maps a Go constant type to the equivalent default annotation key.
// An empty type denotes a string constant (the parser's historical encoding for
// spec.string_const), so it must map to default_string — not the numeric default.
// Mapping "" to default_int generated `field == 0` for string fields whose default
// referenced a string constant, breaking every consumer of that model.
func constTypeToAnnKey(goType string) string {
	switch goType {
	case "string", "":
		return "default_string"
	case "int64":
		return "default_int"
	case "float64":
		return "default_float"
	case "bool":
		return "default_bool"
	default:
		return "default_int"
	}
}

// writeUndefaults generates the inverse of writeDefaults: for every message it emits
// a function that resets fields to their zero values when they match the default.
// This allows configs to be stored with only user overrides, so future default
// changes propagate automatically. Messages without defaults get an empty function
// (nil guard + return) so callers can unconditionally call Undefaults.
func writeUndefaults(b *strings.Builder, pf model.ProtoFile) {
	hasDefaults := messagesWithDefaults(pf)
	dupFields := findDuplicateDefaultFieldsGo(pf.Messages)

	// Build lookup from file-level constant name → constant type (for default_const)
	constTypes := make(map[string]string)
	for _, c := range pf.Constants {
		constTypes[c.Name] = c.Type
	}

	f := codefile.New(codefile.Go)

	for _, msg := range pf.Messages {
		// Collect direct field defaults
		var defaults []fieldDefault
		for _, field := range msg.Fields {
			for _, key := range []string{"default_string", "default_int", "default_float", "default_bool"} {
				if _, ok := field.Annotations[key]; ok {
					constName := strutil.UpperSnakeCaseToPascalCase(strutil.DefaultConstName(msg.Name, field.Name, dupFields))
					goFieldName := toGoFieldName(field.Name)
					defaults = append(defaults, fieldDefault{
						FieldName: goFieldName,
						ConstName: constName,
						AnnKey:    key,
						Field:     field,
					})
				}
			}
			// default_const: references a file-level constant by name
			if av, ok := field.Annotations["default_const"]; ok {
				refName := av.StringValue
				goConstName := strutil.UpperSnakeCaseToPascalCase(refName)
				goFieldName := toGoFieldName(field.Name)
				annKey := constTypeToAnnKey(constTypes[refName])
				defaults = append(defaults, fieldDefault{
					FieldName: goFieldName,
					ConstName: goConstName,
					AnnKey:    annKey,
					Field:     field,
				})
			}
		}

		// Collect sub-message fields with transitive defaults
		type subMsgCall struct {
			GoField     string
			FuncName    string
			HasDefaults bool
		}
		var subMsgCalls []subMsgCall
		for _, field := range msg.Fields {
			if field.IsOneOf || field.IsRepeated || field.IsMap {
				continue
			}
			if !field.IsMessage || field.TypePackage != "" {
				continue
			}
			if hasDefaults[field.Type] {
				subMsgCalls = append(subMsgCalls, subMsgCall{
					GoField:     toGoFieldName(field.Name),
					FuncName:    field.Type + "Undefaults",
					HasDefaults: true,
				})
			}
		}

		f.Section(fmt.Sprintf("TypeUndefaults: %s", msg.Name))
		f.Comment("%sUndefaults resets fields that match their default values back to zero.", msg.Name)
		f.Comment("Use before persisting to store only explicit user overrides.")
		f.Open("func %sUndefaults(v *%s)", msg.Name, msg.Name)
		f.P("if v == nil { return }")

		for _, d := range defaults {
			castExpr := d.ConstName
			if d.AnnKey == "default_int" && (d.Field.Type == "int32" || d.Field.Type == "sint32" || d.Field.Type == "sfixed32") {
				castExpr = fmt.Sprintf("int32(%s)", d.ConstName)
			} else if d.AnnKey == "default_int" && (d.Field.Type == "uint32" || d.Field.Type == "fixed32") {
				castExpr = fmt.Sprintf("uint32(%s)", d.ConstName)
			} else if d.AnnKey == "default_int" && (d.Field.Type == "uint64" || d.Field.Type == "fixed64") {
				castExpr = fmt.Sprintf("uint64(%s)", d.ConstName)
			} else if d.AnnKey == "default_float" && d.Field.Type == "float" {
				castExpr = fmt.Sprintf("float32(%s)", d.ConstName)
			}

			switch d.AnnKey {
			case "default_string":
				f.P("if v.%s == %s {v.%s = \"\"}", d.FieldName, castExpr, d.FieldName)
			case "default_int":
				f.P("if v.%s == %s {v.%s = 0}", d.FieldName, castExpr, d.FieldName)
			case "default_float":
				f.P("if v.%s == %s {v.%s = 0.0}", d.FieldName, castExpr, d.FieldName)
			case "default_bool":
				f.P("if v.%s == %s {v.%s = false}", d.FieldName, castExpr, d.FieldName)
			}
		}

		// default_host: reset to nil if matches default
		for _, field := range msg.Fields {
			av, ok := field.Annotations["default_host"]
			if !ok || av.HostValue == nil {
				continue
			}
			goFieldName := toGoFieldName(field.Name)
			f.P("if v.%s != nil && v.%s.Host == %q && v.%s.Port == %d { v.%s = nil }",
				goFieldName, goFieldName, av.HostValue.Host, goFieldName, av.HostValue.Port, goFieldName)
		}

		for _, sc := range subMsgCalls {
			f.P("if v.%s != nil { %s(v.%s) }", sc.GoField, sc.FuncName, sc.GoField)
		}

		f.Close()
	}

	b.WriteString(f.String())
}

// fieldDefault holds info for generating a single default assignment.
type fieldDefault struct {
	FieldName string
	ConstName string
	AnnKey    string
	Field     model.Field
}

// defaultConstant holds a derived constant from a field-level default annotation.
type defaultConstant struct {
	Name  string // PascalCase Go name, e.g. DefaultPartitionCount
	Type  string // Go type: string, int64, float64, bool
	Value string // Go literal, e.g. "hello", 271, 0.95, true
}

// collectFieldDefaultConstants iterates over all message fields looking for
// default_string, default_int, default_float, default_bool annotations and
// returns the corresponding Go constants.
func collectFieldDefaultConstants(pf model.ProtoFile) []defaultConstant {
	dupFields := findDuplicateDefaultFieldsGo(pf.Messages)
	var results []defaultConstant
	for _, msg := range pf.Messages {
		for _, field := range msg.Fields {
			for _, key := range []string{"default_string", "default_int", "default_float", "default_bool"} {
				av, ok := field.Annotations[key]
				if !ok {
					continue
				}
				// Derive constant name: field name "partition_count" →
				// "DEFAULT_PARTITION_COUNT" → PascalCase "DefaultPartitionCount"
				upperName := strutil.DefaultConstName(msg.Name, field.Name, dupFields)
				goName := strutil.UpperSnakeCaseToPascalCase(upperName)

				var goType, goValue string
				switch key {
				case "default_string":
					goType = "string"
					goValue = fmt.Sprintf("%q", av.StringValue)
				case "default_int":
					goType = "int64"
					goValue = fmt.Sprintf("%d", av.IntValue)
				case "default_float":
					goType = "float64"
					goValue = formatFloat64(av.FloatValue)
				case "default_bool":
					goType = "bool"
					goValue = fmt.Sprintf("%t", av.BoolValue)
				}

				results = append(results, defaultConstant{
					Name:  goName,
					Type:  goType,
					Value: goValue,
				})
			}
			// default_host: generate a DEFAULT_{FIELD}_PORT constant
			if av, ok := field.Annotations["default_host"]; ok && av.HostValue != nil {
				upperName := strutil.DefaultConstName(msg.Name, field.Name, dupFields) + "_PORT"
				goName := strutil.UpperSnakeCaseToPascalCase(upperName)
				results = append(results, defaultConstant{
					Name:  goName,
					Type:  "int64",
					Value: fmt.Sprintf("%d", av.HostValue.Port),
				})
			}
		}
	}
	return results
}

// findDuplicateDefaultFieldsGo wraps strutil.FindDuplicateDefaultFields for Go.
func findDuplicateDefaultFieldsGo(messages []model.Message) map[string]bool {
	defaultKeys := []string{"default_string", "default_int", "default_float", "default_bool", "default_host"}
	var entries []struct {
		MsgName    string
		FieldNames []string
	}
	for _, msg := range messages {
		var names []string
		for _, field := range msg.Fields {
			for _, key := range defaultKeys {
				if _, ok := field.Annotations[key]; ok {
					names = append(names, field.Name)
					break
				}
			}
		}
		if len(names) > 0 {
			entries = append(entries, struct {
				MsgName    string
				FieldNames []string
			}{msg.Name, names})
		}
	}
	return strutil.FindDuplicateDefaultFields(entries)
}

// hasFieldDefaults returns true if any message in pf has defaults, either
// direct default annotations or transitive defaults via sub-message fields.
func hasFieldDefaults(pf model.ProtoFile) bool {
	return len(messagesWithDefaults(pf)) > 0
}

// writeCapacityConstants emits MAX_SIZE_* and MAX_ITEMS_* constants for bounded fields.
// These constants enable Fleetwood to compute utilization rates.
func writeCapacityConstants(b *strings.Builder, pf model.ProtoFile) {
	var entries []string
	for _, msg := range pf.Messages {
		upperMsg := strings.ToUpper(msg.Name)
		for _, field := range msg.Fields {
			upperField := strings.ToUpper(field.Name)

			if field.Type == "string" || field.Type == "bytes" {
				if !field.IsRepeated && !field.IsMap {
					if ann, ok := field.Annotations["assert_max_size"]; ok {
						entries = append(entries, fmt.Sprintf("MaxSize%s%s int = %d", strutil.UpperSnakeCaseToPascalCase(upperMsg), strutil.UpperSnakeCaseToPascalCase(upperField), ann.IntValue))
					} else {
						entries = append(entries, fmt.Sprintf("MaxSize%s%s int = %d", strutil.UpperSnakeCaseToPascalCase(upperMsg), strutil.UpperSnakeCaseToPascalCase(upperField), collection.DefaultScalarBytes))
					}
				}
			}

			if field.IsRepeated || field.IsMap {
				if ann, ok := field.Annotations["assert_max_size"]; ok {
					entries = append(entries, fmt.Sprintf("MaxSize%s%s int = %d", strutil.UpperSnakeCaseToPascalCase(upperMsg), strutil.UpperSnakeCaseToPascalCase(upperField), ann.IntValue))
				} else {
					entries = append(entries, fmt.Sprintf("MaxSize%s%s int = %d", strutil.UpperSnakeCaseToPascalCase(upperMsg), strutil.UpperSnakeCaseToPascalCase(upperField), collection.DefaultCollCount))
				}
			}
		}
	}

	if len(entries) > 0 {
		f := codefile.New(codefile.Go)
		f.Section("Capacity Constants (for utilization monitoring)")
		f.P("const (")
		f.Indent()
		for _, e := range entries {
			f.P("%s", e)
		}
		f.Dedent()
		f.P(")")
		f.Blank()
		b.WriteString(f.String())
	}
}

// formatFloat64 formats a float64 value as a Go literal, ensuring it always
// contains a decimal point (e.g. "1.0" not "1").
func formatFloat64(f float64) string {
	s := fmt.Sprintf("%g", f)
	// Ensure the literal has a decimal point so Go treats it as float64.
	// Contains checks on the formatted string itself (not proto data).
	if !strings.Contains(s, ".") && !strings.Contains(s, "e") && !strings.Contains(s, "E") {
		s += ".0"
	}
	return s
}

// writeMerge generates Merge functions for every message in the proto file.
// Each function fills zero-valued fields in dst from src. dst takes precedence —
// non-zero fields are never overwritten. When both dst and src have a non-nil
// same-package sub-message, we recurse.
func writeMerge(b *strings.Builder, pf model.ProtoFile) {
	if len(pf.Messages) == 0 {
		return
	}

	// Build set of messages with fields so we know which same-package messages
	// have Merge functions (avoids calling undefined MergeXxx for empty messages).
	hasFields := make(map[string]bool)
	for _, m := range pf.Messages {
		if len(m.Fields) > 0 {
			hasFields[m.Name] = true
		}
	}

	f := codefile.New(codefile.Go)

	for _, msg := range pf.Messages {
		if len(msg.Fields) == 0 {
			continue
		}

		goMsgName := msg.Name
		funcName := "Merge" + goMsgName

		f.Section(fmt.Sprintf("Merge: %s", goMsgName))
		f.Comment("%s fills zero-valued fields in dst from src.", funcName)
		f.Open("func %s(dst, src *%s)", funcName, goMsgName)

		// Collect oneof groups for handling after scalar fields
		oneofGroups := map[string]bool{} // track unique oneof names
		var oneofNames []string          // preserve order

		for _, field := range msg.Fields {
			goField := toGoFieldName(field.Name)

			// Skip oneof fields — handled below as groups
			if field.IsOneOf {
				if !oneofGroups[field.OneOfName] {
					oneofGroups[field.OneOfName] = true
					oneofNames = append(oneofNames, field.OneOfName)
				}
				continue
			}

			// Optional (proto3 optional) fields are pointers in Go — nil means unset
			if field.IsOptional && !field.IsMessage && !field.IsRepeated && !field.IsMap {
				f.P("if dst.%s == nil { dst.%s = src.%s }", goField, goField, goField)
				continue
			}

			switch {
			case field.IsMap:
				if field.MapValueIsMessage && field.MapValueTypePackage == "" && hasFields[field.MapValueType] {
					// Map with same-package message value: merge matching entries
					subFuncName := "Merge" + field.MapValueType
					f.Open("if len(dst.%s) == 0", goField)
					f.P("dst.%s = src.%s", goField, goField)
					f.CloseOpen("else")
					f.Open("for k, srcVal := range src.%s", goField)
					f.Open("if dstVal, ok := dst.%s[k]; ok", goField)
					f.P("%s(dstVal, srcVal)", subFuncName)
					f.CloseOpen("else")
					f.P("dst.%s[k] = srcVal", goField)
					f.Close()
					f.Close()
					f.Close()
				} else {
					f.P("if len(dst.%s) == 0 { dst.%s = src.%s }", goField, goField, goField)
				}
			case field.IsRepeated:
				f.P("if len(dst.%s) == 0 { dst.%s = src.%s }", goField, goField, goField)
			case field.IsMessage:
				if field.TypePackage == "" && hasFields[field.Type] {
					// Same-package message with fields: recurse if both non-nil
					subFuncName := "Merge" + field.Type
					f.P("if dst.%s == nil { dst.%s = src.%s } else if src.%s != nil { %s(dst.%s, src.%s) }",
						goField, goField, goField, goField, subFuncName, goField, goField)
				} else {
					// Cross-package or empty message: take src if dst is nil, else keep dst
					f.P("if dst.%s == nil { dst.%s = src.%s }", goField, goField, goField)
				}
			case field.IsEnum:
				f.P("if dst.%s == 0 { dst.%s = src.%s }", goField, goField, goField)
			case field.Type == "string":
				f.P("if dst.%s == \"\" { dst.%s = src.%s }", goField, goField, goField)
			case field.Type == "bool":
				f.P("if !dst.%s { dst.%s = src.%s }", goField, goField, goField)
			case field.Type == "bytes":
				f.P("if len(dst.%s) == 0 { dst.%s = src.%s }", goField, goField, goField)
			case isIntegerType(field.Type):
				f.P("if dst.%s == 0 { dst.%s = src.%s }", goField, goField, goField)
			case isFloatType(field.Type):
				f.P("if dst.%s == 0 { dst.%s = src.%s }", goField, goField, goField)
			default:
				// Unknown type, skip
			}
		}

		// Handle oneof groups: in Go, oneof fields are interface types — nil means unset
		for _, oneofName := range oneofNames {
			goField := toGoFieldName(oneofName)
			f.P("if dst.%s == nil { dst.%s = src.%s }", goField, goField, goField)
		}

		f.Close()
	}

	b.WriteString(f.String())
}
