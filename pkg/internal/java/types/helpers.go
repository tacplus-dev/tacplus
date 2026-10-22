// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
)

// helpersTemplateData holds all pre-computed data for the helpers template.
type helpersTemplateData struct {
	Header      string
	PackagePath string
	Imports     string
	Body        string
	Part        int    // HelpersPart<Part>; zero for Helpers itself
	Extends     string // superclass in the HelpersPart chain, if any
}

// helpersClassBudget bounds the generated source bytes of one Helpers class.
// Every method a class declares or calls costs entries in its constant pool,
// which the JVM caps at 65,535; a package with hundreds of messages (the query
// vservice) overflows one class. Over budget, the body is split into a chain
// HelpersPart1 <- ... <- HelpersPartN <- Helpers, so Helpers.x() still resolves
// (static methods are inherited) and no call site changes. The budget keeps
// each class at a small fraction of the limit.
const helpersClassBudget = 512 << 10

// generateHelpers generates a single consolidated Helpers.java file containing all
// static helper methods: enum helpers, constants, serialization, validation, map helpers,
// and type defaults. Over helpersClassBudget the methods move to HelpersPart<N>
// classes that Helpers extends.
func generateHelpers(pf model.ProtoFile, baseDir, subdir, outerClassName string, config *tactype.Config, hasDefaults map[string]bool, svcRPCs []proto.ServiceRPCEntry, vsEntries []proto.VServiceEntry) error {
	outputDir := filepath.Join(baseDir, subdir)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	packagePath := strings.ReplaceAll(subdir, string(filepath.Separator), ".")

	// Build imports
	var imports strings.Builder
	imports.WriteString("import java.util.*;\n")
	if proto.EmitsEntitlementHelpers(pf.RelPath, pf.Package) && len(svcRPCs) > 0 {
		fmt.Fprintf(&imports, "import %s.grants.Grants;\n", config.JavaPackageBase)
	}

	if len(pf.Enums) > 0 || len(pf.Messages) > 0 {
		errorsPackage := config.JavaPackageBase + ".types.sys.errors"
		fmt.Fprintf(&imports, "import %s.ProtoException;\n", errorsPackage)
	}

	if len(pf.Messages) > 0 {
		imports.WriteString("import java.util.stream.Collectors;\n")
		imports.WriteString("import java.time.Duration;\n")
		imports.WriteString("import java.util.Base64;\n")
		imports.WriteString("import com.google.protobuf.*;\n")
		imports.WriteString("import com.google.protobuf.util.JsonFormat;\n")
		imports.WriteString("import com.google.gson.Gson;\n")
		imports.WriteString("import com.google.gson.GsonBuilder;\n")
		imports.WriteString("import com.google.gson.JsonObject;\n")
		imports.WriteString("import com.google.gson.JsonArray;\n")
		imports.WriteString("import com.google.gson.JsonElement;\n")
		imports.WriteString("import com.moandjiezana.toml.Toml;\n")
		imports.WriteString("import org.yaml.snakeyaml.Yaml;\n")

		validatorsPackage := strings.TrimSuffix(config.JavaPackageBase, ".types") + ".utils"
		if packagePath != validatorsPackage {
			fmt.Fprintf(&imports, "import %s.Validation;\n", validatorsPackage)
		}
	}

	// Import host outer class if any field has a default_host annotation
	if hasDefaultHostAnnotation(pf) {
		hostPackage := config.JavaPackageBase + ".types.sys.host"
		fmt.Fprintf(&imports, "import %s.HostOuterClass;\n", hostPackage)
	}

	// Build the body with all helper sections
	var body strings.Builder

	if len(pf.Enums) > 0 {
		body.WriteString("// ============ Enum Helpers ============\n")
		writeEnumHelpers(&body, pf, outerClassName)
	}

	if len(pf.Constants) > 0 || hasFieldDefaults(pf, hasDefaults) {
		body.WriteString("// ============ Constants ============\n")
		writeConstants(&body, pf)
	}

	if len(pf.Messages) > 0 {
		body.WriteString("// ============ Serialization ============\n")
		writeSerialization(&body, pf, outerClassName, config)
	}

	if len(pf.Messages) > 0 {
		body.WriteString("// ============ Validation ============\n")
		if err := writeValidation(&body, pf, outerClassName, config); err != nil {
			return err
		}
	}

	if len(pf.Messages) > 0 {
		body.WriteString("// ============ Egress Release (spec.redact) ============\n")
		if err := writeRelease(&body, pf, outerClassName); err != nil {
			return err
		}
	}

	writePublicView(&body, pf, outerClassName)

	if len(pf.Messages) > 0 {
		hasParamMap := false
		for _, msg := range pf.Messages {
			if msg.ParamMapConfig != nil {
				hasParamMap = true
				break
			}
		}
		if hasParamMap {
			body.WriteString("// ============ Map Helpers ============\n")
			writeFromMap(&body, pf, outerClassName)
		}
	}

	if hasFieldDefaults(pf, hasDefaults) {
		body.WriteString("// ============ TypeDefaults ============\n")
		writeTypeDefaults(&body, pf, outerClassName, config, hasDefaults)
		body.WriteString("// ============ TypeUndefaults ============\n")
		writeTypeUndefaults(&body, pf, outerClassName, hasDefaults)
	}

	if len(pf.Messages) > 0 {
		body.WriteString("// ============ Merge ============\n")
		writeMerge(&body, pf, outerClassName)
	}

	if len(pf.Messages) > 0 {
		body.WriteString("// ============ EnvVar ============\n")
		writeEnvVar(&body, pf, outerClassName, config)
	}

	// The filesystem and scope sections share private members across their
	// methods, so each must stay whole in one class if the body is split.
	var shared strings.Builder

	// Generate filesystem helpers (directoryFromFileSystem / directoryToFileSystem)
	if proto.EmitsFilesystemHelpers(pf.RelPath, pf.Package) {
		writeFilesystemHelpers(&shared, outerClassName)
	}

	// Generate scopeToEntitlements for the entitlements package.
	if proto.EmitsEntitlementHelpers(pf.RelPath, pf.Package) && len(svcRPCs) > 0 {
		shared.WriteString("// ============ Scope ============\n")
		writeScopeToEntitlements(&shared, svcRPCs, vsEntries)
	}

	data := helpersTemplateData{
		Header:      strings.TrimSuffix(header.GeneratedHeader(consts.LangJava, pf.RelPath), "\n"),
		PackagePath: packagePath,
		Imports:     imports.String(),
		Body:        body.String() + shared.String(),
	}

	if len(data.Body) > helpersClassBudget {
		units := javaClassMembers(body.String())
		if shared.Len() > 0 {
			units = append(units, shared.String())
		}
		for i, partBody := range packHelpersParts(units, helpersClassBudget) {
			part := data
			part.Part = i + 1
			part.Body = partBody
			if err := writeHelpersFile(part, "helpers_part.java.tmpl", filepath.Join(outputDir, fmt.Sprintf("HelpersPart%d.java", part.Part))); err != nil {
				return err
			}
			data.Extends = fmt.Sprintf("HelpersPart%d", part.Part)
		}
		data.Imports = ""
		data.Body = ""
	}

	return writeHelpersFile(data, "helpers.java.tmpl", filepath.Join(outputDir, "Helpers.java"))
}

// writeHelpersFile renders one Helpers class template to outputPath.
func writeHelpersFile(data helpersTemplateData, templateName, outputPath string) error {
	typeFS, err := fs.Sub(templates.FS, "java/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}
	content, err := fs.ReadFile(typeFS, templateName)
	if err != nil {
		return fmt.Errorf("failed to read helpers template: %w", err)
	}
	tmpl, err := template.New("helpers").Parse(string(content))
	if err != nil {
		return fmt.Errorf("failed to parse helpers template: %w", err)
	}

	var result strings.Builder
	if err := tmpl.Execute(&result, data); err != nil {
		return fmt.Errorf("failed to execute helpers template: %w", err)
	}

	return os.WriteFile(outputPath, []byte(result.String()), 0644)
}

// javaClassMembers splits generated class-body source into its top-level
// members (each method or field with its leading comments), in order. It tracks
// brace depth outside comments and string/char literals; a member ends on the
// first line that finishes at depth 0 with '}' or ';'.
func javaClassMembers(src string) []string {
	var members []string
	depth, start, inComment := 0, 0, false
	for lineStart := 0; lineStart < len(src); {
		lineEnd := len(src)
		if nl := strings.IndexByte(src[lineStart:], '\n'); nl >= 0 {
			lineEnd = lineStart + nl + 1
		}
		var last byte
		for i := lineStart; i < lineEnd; i++ {
			c := src[i]
			switch {
			case inComment:
				if c == '*' && i+1 < lineEnd && src[i+1] == '/' {
					inComment = false
					i++
				}
				continue
			case c == '/' && i+1 < lineEnd && src[i+1] == '/':
				i = lineEnd
				continue
			case c == '/' && i+1 < lineEnd && src[i+1] == '*':
				inComment = true
				i++
				continue
			case c == '"' || c == '\'':
				for i++; i < lineEnd && src[i] != c; i++ {
					if src[i] == '\\' {
						i++
					}
				}
			case c == '{':
				depth++
			case c == '}':
				depth--
			}
			if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				last = c
			}
		}
		lineStart = lineEnd
		if depth == 0 && !inComment && (last == '}' || last == ';') {
			members = append(members, src[start:lineEnd])
			start = lineEnd
		}
	}
	if start < len(src) {
		members = append(members, src[start:])
	}
	return members
}

// packHelpersParts concatenates members in order into class bodies of at most
// budget bytes each; a single member larger than budget gets a body of its own.
func packHelpersParts(members []string, budget int) []string {
	var parts []string
	var cur strings.Builder
	for _, m := range members {
		if cur.Len() > 0 && cur.Len()+len(m) > budget {
			parts = append(parts, cur.String())
			cur.Reset()
		}
		cur.WriteString(m)
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

// writeEnumHelpers writes enum helper static methods into the Helpers class
// enumOuterClass returns the element-specific outer class if set, falling back to the default.
func enumOuterClass(e model.Enum, defaultOuter string) string {
	if e.OuterClass != "" {
		return e.OuterClass
	}
	return defaultOuter
}

// msgOuterClass returns the element-specific outer class if set, falling back to the default.
func msgOuterClass(m model.Message, defaultOuter string) string {
	if m.OuterClass != "" {
		return m.OuterClass
	}
	return defaultOuter
}

func writeEnumHelpers(b *strings.Builder, pf model.ProtoFile, outerClassName string) {
	for _, enum := range pf.Enums {
		writeEnumHelperMethods(b, enum, enumOuterClass(enum, outerClassName))
	}
}

// writeEnumHelperMethods writes parse/toString/validValues/parseMultiple for a single enum
func writeEnumHelperMethods(b *strings.Builder, enum model.Enum, outerClassName string) {
	// Filter out sentinel values (UNDEFINED/NO_ERROR/UNSPECIFIED)
	var validValues []model.EnumValue
	for _, v := range enum.Values {
		if !model.IsEnumSentinel(v.Name) {
			validValues = append(validValues, v)
		}
	}

	if len(validValues) == 0 {
		return
	}

	// Fully qualified enum name
	fullEnumName := outerClassName + "." + enum.Name

	// parse() method - switch expression
	fmt.Fprintf(b, "public static %s parse%s(String s) throws ProtoException {\n", fullEnumName, enum.Name)
	b.WriteString("if (s == null || s.isEmpty()) {\n")
	b.WriteString("throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"Empty string cannot be parsed as \" + ")
	fmt.Fprintf(b, "%s.class.getSimpleName());\n", fullEnumName)
	b.WriteString("}\n")
	b.WriteString("String trimmed = s.trim();\n")
	b.WriteString("if (trimmed.isEmpty()) {\n")
	b.WriteString("throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"Empty string cannot be parsed as \" + ")
	fmt.Fprintf(b, "%s.class.getSimpleName());\n", fullEnumName)
	b.WriteString("}\n")
	b.WriteString("try {\n")
	b.WriteString("int numeric = Integer.parseInt(trimmed);\n")
	fmt.Fprintf(b, "%s parsed = %s.forNumber(numeric);\n", fullEnumName, fullEnumName)
	b.WriteString("if (parsed != null && parsed.getNumber() != 0) {\n")
	b.WriteString("return parsed;\n")
	b.WriteString("}\n")
	b.WriteString("} catch (NumberFormatException ignored) {}\n")
	b.WriteString("String normalized = trimmed.toLowerCase().replace('_', '-');\n")
	b.WriteString("return switch (normalized) {\n")

	for _, v := range validValues {
		aliases := javaEnumParseAliases(enum, v)
		for i, alias := range aliases {
			if i == 0 {
				fmt.Fprintf(b, "case %q", alias)
			} else {
				fmt.Fprintf(b, ", %q", alias)
			}
		}

		fmt.Fprintf(b, " -> %s.%s;\n", fullEnumName, v.Name)
	}

	b.WriteString("default -> throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"Invalid \" + ")
	fmt.Fprintf(b, "%s.class.getSimpleName() + \": \" + s + \", valid values are: \" + ", fullEnumName)
	fmt.Fprintf(b, "String.join(\", \", %sValidValues()));\n", strutil.ToLowerCamelCase(enum.Name))
	b.WriteString("};\n")
	b.WriteString("}\n")

	// toString() method - switch expression
	fmt.Fprintf(b, "public static String %sToString(%s value) {\n", strutil.ToLowerCamelCase(enum.Name), fullEnumName)
	b.WriteString("return switch (value) {\n")

	for _, v := range validValues {
		shortName := extractEnumShortName(enum.Name, v.Name)
		if v.JsonValue != "" {
			fmt.Fprintf(b, "case %s -> %q;\n", v.Name, v.JsonValue)
		} else {
			fmt.Fprintf(b, "case %s -> %q;\n", v.Name, shortName)
		}
	}

	b.WriteString("default -> \"undefined\";\n")
	b.WriteString("};\n")
	b.WriteString("}\n")

	// validValues() method
	fmt.Fprintf(b, "public static List<String> %sValidValues() {\n", strutil.ToLowerCamelCase(enum.Name))
	b.WriteString("return Arrays.asList(\n")
	for i, v := range validValues {
		shortName := extractEnumShortName(enum.Name, v.Name)
		if i < len(validValues)-1 {
			fmt.Fprintf(b, "%q,\n", shortName)
		} else {
			fmt.Fprintf(b, "%q\n", shortName)
		}
	}
	b.WriteString(");\n")
	b.WriteString("}\n")

	// parseMultiple() method - uses for loop (checked exceptions don't work with stream method references)
	fmt.Fprintf(b, "public static List<%s> parseMultiple%s(String s) throws ProtoException {\n", fullEnumName, enum.Name)
	fmt.Fprintf(b, "List<%s> result = new ArrayList<>();\n", fullEnumName)
	b.WriteString("for (String part : s.split(\",\")) {\n")
	b.WriteString("part = part.trim();\n")
	b.WriteString("if (!part.isEmpty()) {\n")
	fmt.Fprintf(b, "result.add(parse%s(part));\n", enum.Name)
	b.WriteString("}\n")
	b.WriteString("}\n")
	b.WriteString("return result;\n")
	b.WriteString("}\n")
}

// writeConstants writes public static final fields into the Helpers class
func writeConstants(b *strings.Builder, pf model.ProtoFile) {
	// Generate constants from proto-level constants
	for _, c := range pf.Constants {
		javaName := c.Name
		javaType := mapTypeToJava(c.Type)

		if c.Type == "" || javaType == "long" {
			javaType = inferJavaTypeFromValue(c.Value)
		}

		javaValue := convertValueToJava(c.Value, javaType)
		fmt.Fprintf(b, "public static final %s %s = %s;\n", javaType, javaName, javaValue)
	}

	// Generate constants from field-level default annotations
	dupFields := findDuplicateDefaultFields(pf.Messages)
	for _, msg := range pf.Messages {
		for _, field := range msg.Fields {
			for _, annKey := range defaultAnnotationKeys {
				annVal, ok := field.Annotations[annKey]
				if !ok {
					continue
				}
				javaName := strutil.DefaultConstName(msg.Name, field.Name, dupFields)
				if annKey == "default_host" {
					javaName += "_PORT"
				}
				javaType, javaValue := defaultAnnotationToJava(annKey, annVal)
				fmt.Fprintf(b, "public static final %s %s = %s;\n", javaType, javaName, javaValue)
			}
		}
	}
	b.WriteString("\n")
}

// writeSerialization writes static serialization methods into the Helpers class.
// Uses field-by-field JSON walking (parity with Go's serial.go) — no JsonFormat.parser().
// Enums use parseXxx/xxxToString helpers, messages recurse via xxxFromJson/xxxToJson.
func writeSerialization(b *strings.Builder, pf model.ProtoFile, outerClassName string, config *tactype.Config) {
	crossHelpers := buildCrossPackageHelpersMap(pf, config)
	for _, msg := range pf.Messages {
		writeMessageSerialization(b, msg, msgOuterClass(msg, outerClassName), crossHelpers)
	}
}

// enumParseFunc returns the parse function call for an enum field,
// fully qualifying cross-package references.
func enumParseFunc(field model.Field, crossHelpers map[string]string) string {
	f := "parse" + field.Type
	if fq, ok := crossHelpers[field.TypePackage]; ok && field.TypePackage != "" {
		f = fq + "." + f
	}
	return f
}

// enumToStringFunc returns the toString function call for an enum field,
// fully qualifying cross-package references.
func enumToStringFunc(field model.Field, crossHelpers map[string]string) string {
	f := strutil.ToLowerCamelCase(field.Type) + "ToString"
	if fq, ok := crossHelpers[field.TypePackage]; ok && field.TypePackage != "" {
		f = fq + "." + f
	}
	return f
}

// msgFromJsonFunc returns the fromJson function call for a message field,
// fully qualifying cross-package references.
func msgFromJsonFunc(field model.Field, crossHelpers map[string]string) string {
	f := strutil.ToLowerFirst(field.Type) + "FromJson"
	if fq, ok := crossHelpers[field.TypePackage]; ok && field.TypePackage != "" {
		f = fq + "." + f
	}
	return f
}

// msgToJsonFunc returns the toJson function call for a message field,
// fully qualifying cross-package references.
func msgToJsonFunc(field model.Field, crossHelpers map[string]string) string {
	f := strutil.ToLowerFirst(field.Type) + "ToJson"
	if fq, ok := crossHelpers[field.TypePackage]; ok && field.TypePackage != "" {
		f = fq + "." + f
	}
	return f
}

// msgValidationFunc returns the validate method for a message field,
// fully qualifying cross-package generated Helpers when available.
func msgValidationFunc(typeName, typePackage string, crossHelpers map[string]string, suffix string) string {
	if typeName == "" || isWellKnownTypePackage(typePackage) {
		return ""
	}
	f := "validate" + typeName + suffix + "Depth"
	if typePackage == "" {
		return f
	}
	if fq, ok := crossHelpers[typePackage]; ok {
		return fq + "." + f
	}
	return ""
}

// mapKeyParseExpr returns the Java expression to convert a JSON string key to the
// appropriate map key type. JSON object keys are always strings, so for non-string
// map key types we need to parse them.
func mapKeyParseExpr(mapKeyType string) string {
	switch mapKeyType {
	case "string":
		return "entry.getKey()"
	case "int32", "sint32", "sfixed32", "fixed32":
		return "Integer.parseInt(entry.getKey())"
	case "uint32":
		return "Integer.parseUnsignedInt(entry.getKey())"
	case "int64", "sint64", "sfixed64", "fixed64":
		return "Long.parseLong(entry.getKey())"
	case "uint64":
		return "Long.parseUnsignedLong(entry.getKey())"
	case "bool":
		return "Boolean.parseBoolean(entry.getKey())"
	default:
		return "entry.getKey()"
	}
}

// jsonFieldNames returns both the camelCase and snake_case names for a proto field.
// JSON from JsonFormat uses camelCase; JSON from YAML→Gson uses snake_case.
func jsonFieldNames(protoName string) []string {
	camel := strutil.ToLowerCamelCase(protoName)
	if camel == protoName {
		return []string{camel}
	}
	return []string{camel, protoName}
}

// writeGetJsonElement generates code to find a JSON element by checking the
// canonical key (json_key override or camelCase), snake_case fallback, and any json_aliases.
func writeGetJsonElement(b *strings.Builder, protoName, varName string, extraKeys ...string) {
	names := jsonFieldNames(protoName)
	// Append any extra keys (json_key, json_aliases) that aren't already present
	seen := make(map[string]bool, len(names)+len(extraKeys))
	for _, n := range names {
		seen[n] = true
	}
	for _, k := range extraKeys {
		if k != "" && !seen[k] {
			names = append(names, k)
			seen[k] = true
		}
	}
	if len(names) == 1 {
		fmt.Fprintf(b, "JsonElement %s = obj.get(%q);\n", varName, names[0])
	} else {
		// First key is the primary, chain fallbacks with has/get
		fmt.Fprintf(b, "JsonElement %s = null;\n", varName)
		for _, key := range names {
			fmt.Fprintf(b, "if (%s == null) %s = obj.get(%q);\n", varName, varName, key)
		}
	}
}

// protoTypeToJavaType converts proto scalar types to Java types for Gson extraction.
func protoTypeToJavaType(protoType string) string {
	switch protoType {
	case "string":
		return "String"
	case "int32", "sint32", "sfixed32":
		return "int"
	case "int64", "sint64", "sfixed64":
		return "long"
	case "uint32", "fixed32":
		return "int"
	case "uint64", "fixed64":
		return "long"
	case "float":
		return "float"
	case "double":
		return "double"
	case "bool":
		return "boolean"
	case "bytes":
		return "String" // base64-encoded in JSON
	default:
		return protoType
	}
}

// writeFromJsonFieldAssignment generates the Java code to read a field from
// a Gson JsonObject and set it on a protobuf builder. This is the Java parity
// equivalent of Go's computeFromJSONAssignment.
func writeFromJsonFieldAssignment(b *strings.Builder, field model.Field, _fullTypeName string, crossHelpers map[string]string) {
	javaName := javaFieldNameUpper(field.Name)
	elVar := toJavaFieldName(field.Name) + "El"

	// Build extra keys: json_key override + json_aliases
	var extraKeys []string
	if field.JsonKey != "" {
		extraKeys = append(extraKeys, field.JsonKey)
	}
	extraKeys = append(extraKeys, field.JsonAliases...)
	writeGetJsonElement(b, field.Name, elVar, extraKeys...)
	fmt.Fprintf(b, "if (%s != null && !%s.isJsonNull()) {\n", elVar, elVar)

	if field.IsMap {
		writeFromJsonMapField(b, field, javaName, elVar, crossHelpers)
	} else if field.IsOneOf {
		writeFromJsonOneOfField(b, field, javaName, elVar, crossHelpers)
	} else if field.IsEnum {
		writeFromJsonEnumField(b, field, javaName, elVar, crossHelpers)
	} else if field.IsMessage {
		writeFromJsonMessageField(b, field, javaName, elVar, crossHelpers)
	} else {
		writeFromJsonScalarField(b, field, javaName, elVar)
	}

	b.WriteString("}\n")
}

func writeFromJsonEnumField(b *strings.Builder, field model.Field, javaName, elVar string, crossHelpers map[string]string) {
	parseFunc := enumParseFunc(field, crossHelpers)
	if field.IsRepeated {
		fmt.Fprintf(b, "if (%s.isJsonArray()) {\n", elVar)
		fmt.Fprintf(b, "for (JsonElement item : %s.getAsJsonArray()) {\n", elVar)
		b.WriteString("if (item.isJsonPrimitive()) {\n")
		fmt.Fprintf(b, "try { builder.add%s(%s(item.getAsString())); } catch (Exception e) {}\n", javaName, parseFunc)
		b.WriteString("}\n")
		b.WriteString("}\n")
		b.WriteString("}\n")
	} else {
		fmt.Fprintf(b, "if (%s.isJsonPrimitive()) {\n", elVar)
		fmt.Fprintf(b, "try { builder.set%s(%s(%s.getAsString())); } catch (Exception e) {}\n", javaName, parseFunc, elVar)
		b.WriteString("}\n")
	}
}

func writeFromJsonMessageField(b *strings.Builder, field model.Field, javaName, elVar string, crossHelpers map[string]string) {
	if isWellKnownTypePackage(field.TypePackage) {
		// Well-known types: use JsonFormat for these since they have special JSON representations
		if field.IsRepeated {
			fmt.Fprintf(b, "if (%s.isJsonArray()) {\n", elVar)
			fmt.Fprintf(b, "for (JsonElement item : %s.getAsJsonArray()) {\n", elVar)
			b.WriteString("if (item.isJsonObject()) {\n")
			b.WriteString("try {\n")
			// Use JsonFormat only for well-known types
			fmt.Fprintf(b, "com.google.protobuf.%s.Builder wkb = com.google.protobuf.%s.newBuilder();\n", field.Type, field.Type)
			b.WriteString("JsonFormat.parser().merge(item.toString(), wkb);\n")
			fmt.Fprintf(b, "builder.add%s(wkb.build());\n", javaName)
			b.WriteString("} catch (Exception e) {}\n")
			b.WriteString("}\n")
			b.WriteString("}\n")
			b.WriteString("}\n")
		} else {
			fmt.Fprintf(b, "if (%s.isJsonObject()) {\n", elVar)
			b.WriteString("try {\n")
			fmt.Fprintf(b, "com.google.protobuf.%s.Builder wkb = com.google.protobuf.%s.newBuilder();\n", field.Type, field.Type)
			fmt.Fprintf(b, "JsonFormat.parser().merge(%s.toString(), wkb);\n", elVar)
			fmt.Fprintf(b, "builder.set%s(wkb.build());\n", javaName)
			b.WriteString("} catch (Exception e) {}\n")
			b.WriteString("}\n")
		}
		return
	}
	fromFunc := msgFromJsonFunc(field, crossHelpers)
	if field.IsRepeated {
		fmt.Fprintf(b, "if (%s.isJsonArray()) {\n", elVar)
		fmt.Fprintf(b, "for (JsonElement item : %s.getAsJsonArray()) {\n", elVar)
		b.WriteString("if (item.isJsonObject()) {\n")
		fmt.Fprintf(b, "try { builder.add%s(%s(item.toString())); } catch (Exception e) {}\n", javaName, fromFunc)
		b.WriteString("}\n")
		b.WriteString("}\n")
		b.WriteString("}\n")
	} else {
		fmt.Fprintf(b, "if (%s.isJsonObject()) {\n", elVar)
		fmt.Fprintf(b, "try { builder.set%s(%s(%s.toString())); } catch (Exception e) {}\n", javaName, fromFunc, elVar)
		b.WriteString("}\n")
	}
}

func writeFromJsonMapField(b *strings.Builder, field model.Field, javaName, elVar string, crossHelpers map[string]string) {
	fmt.Fprintf(b, "if (%s.isJsonObject()) {\n", elVar)
	fmt.Fprintf(b, "for (java.util.Map.Entry<String, JsonElement> entry : %s.getAsJsonObject().entrySet()) {\n", elVar)
	b.WriteString("JsonElement val = entry.getValue();\n")
	b.WriteString("if (val != null && !val.isJsonNull()) {\n")

	// JSON object keys are always strings; parse to the correct map key type
	keyExpr := mapKeyParseExpr(field.MapKeyType)

	if field.MapValueIsMessage {
		if isWellKnownTypePackage(field.MapValueTypePackage) {
			b.WriteString("try {\n")
			fmt.Fprintf(b, "com.google.protobuf.%s.Builder wkb = com.google.protobuf.%s.newBuilder();\n", field.MapValueType, field.MapValueType)
			b.WriteString("JsonFormat.parser().merge(val.toString(), wkb);\n")
			fmt.Fprintf(b, "builder.put%s(%s, wkb.build());\n", javaName, keyExpr)
			b.WriteString("} catch (Exception e) {}\n")
		} else {
			fromFunc := strutil.ToLowerFirst(field.MapValueType) + "FromJson"
			if fq, ok := crossHelpers[field.MapValueTypePackage]; ok && field.MapValueTypePackage != "" {
				fromFunc = fq + "." + fromFunc
			}
			fmt.Fprintf(b, "try { builder.put%s(%s, %s(val.toString())); } catch (Exception e) {}\n", javaName, keyExpr, fromFunc)
		}
	} else {
		// Scalar or enum map values
		switch field.MapValueType {
		case "string":
			fmt.Fprintf(b, "builder.put%s(%s, val.getAsString());\n", javaName, keyExpr)
		case "int32", "sint32", "sfixed32", "fixed32", "uint32":
			fmt.Fprintf(b, "builder.put%s(%s, val.getAsInt());\n", javaName, keyExpr)
		case "int64", "sint64", "sfixed64", "fixed64", "uint64":
			fmt.Fprintf(b, "builder.put%s(%s, val.getAsLong());\n", javaName, keyExpr)
		case "bool":
			fmt.Fprintf(b, "builder.put%s(%s, val.getAsBoolean());\n", javaName, keyExpr)
		case "float":
			fmt.Fprintf(b, "builder.put%s(%s, val.getAsFloat());\n", javaName, keyExpr)
		case "double":
			fmt.Fprintf(b, "builder.put%s(%s, val.getAsDouble());\n", javaName, keyExpr)
		default:
			fmt.Fprintf(b, "builder.put%s(%s, val.getAsString());\n", javaName, keyExpr)
		}
	}

	b.WriteString("}\n")
	b.WriteString("}\n")
	b.WriteString("}\n")
}

func writeFromJsonOneOfField(b *strings.Builder, field model.Field, javaName, elVar string, crossHelpers map[string]string) {
	if field.IsEnum {
		parseFunc := enumParseFunc(field, crossHelpers)
		fmt.Fprintf(b, "if (%s.isJsonPrimitive()) {\n", elVar)
		fmt.Fprintf(b, "try { builder.set%s(%s(%s.getAsString())); } catch (Exception e) {}\n", javaName, parseFunc, elVar)
		b.WriteString("}\n")
	} else if field.IsMessage {
		if isWellKnownTypePackage(field.TypePackage) {
			fmt.Fprintf(b, "if (%s.isJsonObject()) {\n", elVar)
			b.WriteString("try {\n")
			fmt.Fprintf(b, "com.google.protobuf.%s.Builder wkb = com.google.protobuf.%s.newBuilder();\n", field.Type, field.Type)
			fmt.Fprintf(b, "JsonFormat.parser().merge(%s.toString(), wkb);\n", elVar)
			fmt.Fprintf(b, "builder.set%s(wkb.build());\n", javaName)
			b.WriteString("} catch (Exception e) {}\n")
			b.WriteString("}\n")
		} else {
			fromFunc := msgFromJsonFunc(field, crossHelpers)
			fmt.Fprintf(b, "if (%s.isJsonObject()) {\n", elVar)
			fmt.Fprintf(b, "try { builder.set%s(%s(%s.toString())); } catch (Exception e) {}\n", javaName, fromFunc, elVar)
			b.WriteString("}\n")
		}
	} else {
		// Scalar oneof
		writeFromJsonScalarField(b, field, javaName, elVar)
	}
}

func writeFromJsonScalarField(b *strings.Builder, field model.Field, javaName, elVar string) {
	if field.IsRepeated {
		fmt.Fprintf(b, "if (%s.isJsonArray()) {\n", elVar)
		fmt.Fprintf(b, "for (JsonElement item : %s.getAsJsonArray()) {\n", elVar)
		b.WriteString("if (item.isJsonPrimitive()) {\n")
		switch field.Type {
		case "string":
			fmt.Fprintf(b, "builder.add%s(item.getAsString());\n", javaName)
		case "int32", "sint32", "sfixed32", "fixed32", "uint32":
			fmt.Fprintf(b, "builder.add%s(item.getAsInt());\n", javaName)
		case "int64", "sint64", "sfixed64", "fixed64", "uint64":
			fmt.Fprintf(b, "builder.add%s(item.getAsLong());\n", javaName)
		case "bool":
			fmt.Fprintf(b, "builder.add%s(item.getAsBoolean());\n", javaName)
		case "float":
			fmt.Fprintf(b, "builder.add%s(item.getAsFloat());\n", javaName)
		case "double":
			fmt.Fprintf(b, "builder.add%s(item.getAsDouble());\n", javaName)
		case "bytes":
			fmt.Fprintf(b, "builder.add%s(com.google.protobuf.ByteString.copyFrom(Base64.getDecoder().decode(item.getAsString())));\n", javaName)
		default:
			fmt.Fprintf(b, "builder.add%s(item.getAsString());\n", javaName)
		}
		b.WriteString("}\n")
		b.WriteString("}\n")
		b.WriteString("}\n")
	} else {
		fmt.Fprintf(b, "if (%s.isJsonPrimitive()) {\n", elVar)
		switch field.Type {
		case "string":
			fmt.Fprintf(b, "builder.set%s(%s.getAsString());\n", javaName, elVar)
		case "int32", "sint32", "sfixed32", "fixed32", "uint32":
			fmt.Fprintf(b, "builder.set%s(%s.getAsInt());\n", javaName, elVar)
		case "int64", "sint64", "sfixed64", "fixed64", "uint64":
			fmt.Fprintf(b, "builder.set%s(%s.getAsLong());\n", javaName, elVar)
		case "bool":
			fmt.Fprintf(b, "builder.set%s(%s.getAsBoolean());\n", javaName, elVar)
		case "float":
			fmt.Fprintf(b, "builder.set%s(%s.getAsFloat());\n", javaName, elVar)
		case "double":
			fmt.Fprintf(b, "builder.set%s(%s.getAsDouble());\n", javaName, elVar)
		case "bytes":
			fmt.Fprintf(b, "builder.set%s(com.google.protobuf.ByteString.copyFrom(Base64.getDecoder().decode(%s.getAsString())));\n", javaName, elVar)
		default:
			fmt.Fprintf(b, "builder.set%s(%s.getAsString());\n", javaName, elVar)
		}
		b.WriteString("}\n")
	}
}

// writeToJsonFieldAssignment generates code to serialize a field from a protobuf
// message into a Gson JsonObject. Parity with Go's computeToJSONAssignment.
func writeToJsonFieldAssignment(b *strings.Builder, field model.Field, _fullTypeName string, crossHelpers map[string]string) {
	javaName := javaFieldNameUpper(field.Name)
	jsonName := field.Name // snake_case, matching Go's json tags
	if field.JsonKey != "" {
		jsonName = field.JsonKey
	}

	if field.IsMap {
		writeToJsonMapField(b, field, javaName, jsonName, crossHelpers)
	} else if field.IsOneOf {
		writeToJsonOneOfField(b, field, javaName, jsonName, crossHelpers)
	} else if field.IsEnum {
		writeToJsonEnumField(b, field, javaName, jsonName, crossHelpers)
	} else if field.IsMessage {
		writeToJsonMessageField(b, field, javaName, jsonName, crossHelpers)
	} else {
		writeToJsonScalarField(b, field, javaName, jsonName)
	}
}

func writeToJsonEnumField(b *strings.Builder, field model.Field, javaName, jsonName string, crossHelpers map[string]string) {
	toStrFunc := enumToStringFunc(field, crossHelpers)
	if field.IsRepeated {
		fmt.Fprintf(b, "if (msg.get%sCount() > 0) {\n", javaName)
		b.WriteString("JsonArray arr = new JsonArray();\n")
		fmt.Fprintf(b, "for (int i = 0; i < msg.get%sCount(); i++) {\n", javaName)
		fmt.Fprintf(b, "arr.add(%s(msg.get%s(i)));\n", toStrFunc, javaName)
		b.WriteString("}\n")
		fmt.Fprintf(b, "obj.add(%q, arr);\n", jsonName)
		b.WriteString("}\n")
	} else {
		fmt.Fprintf(b, "if (msg.get%sValue() != 0) {\n", javaName)
		fmt.Fprintf(b, "obj.addProperty(%q, %s(msg.get%s()));\n", jsonName, toStrFunc, javaName)
		b.WriteString("}\n")
	}
}

func writeToJsonMessageField(b *strings.Builder, field model.Field, javaName, jsonName string, crossHelpers map[string]string) {
	if isWellKnownTypePackage(field.TypePackage) {
		// Well-known types: use JsonFormat
		if field.IsRepeated {
			fmt.Fprintf(b, "if (msg.get%sCount() > 0) {\n", javaName)
			b.WriteString("JsonArray arr = new JsonArray();\n")
			fmt.Fprintf(b, "for (int i = 0; i < msg.get%sCount(); i++) {\n", javaName)
			b.WriteString("try {\n")
			fmt.Fprintf(b, "arr.add(gson.fromJson(JsonFormat.printer().print(msg.get%s(i)), JsonElement.class));\n", javaName)
			b.WriteString("} catch (Exception e) {}\n")
			b.WriteString("}\n")
			fmt.Fprintf(b, "obj.add(%q, arr);\n", jsonName)
			b.WriteString("}\n")
		} else {
			fmt.Fprintf(b, "if (msg.has%s()) {\n", javaName)
			b.WriteString("try {\n")
			fmt.Fprintf(b, "obj.add(%q, gson.fromJson(JsonFormat.printer().print(msg.get%s()), JsonElement.class));\n", jsonName, javaName)
			b.WriteString("} catch (Exception e) {}\n")
			b.WriteString("}\n")
		}
		return
	}
	toFunc := msgToJsonFunc(field, crossHelpers)
	if field.IsRepeated {
		fmt.Fprintf(b, "if (msg.get%sCount() > 0) {\n", javaName)
		b.WriteString("JsonArray arr = new JsonArray();\n")
		fmt.Fprintf(b, "for (int i = 0; i < msg.get%sCount(); i++) {\n", javaName)
		b.WriteString("try {\n")
		fmt.Fprintf(b, "arr.add(gson.fromJson(%s(msg.get%s(i)), JsonElement.class));\n", toFunc, javaName)
		b.WriteString("} catch (Exception e) {}\n")
		b.WriteString("}\n")
		fmt.Fprintf(b, "obj.add(%q, arr);\n", jsonName)
		b.WriteString("}\n")
	} else {
		fmt.Fprintf(b, "if (msg.has%s()) {\n", javaName)
		b.WriteString("try {\n")
		fmt.Fprintf(b, "obj.add(%q, gson.fromJson(%s(msg.get%s()), JsonElement.class));\n", jsonName, toFunc, javaName)
		b.WriteString("} catch (Exception e) {}\n")
		b.WriteString("}\n")
	}
}

func writeToJsonMapField(b *strings.Builder, field model.Field, javaName, jsonName string, crossHelpers map[string]string) {
	fmt.Fprintf(b, "if (msg.get%sCount() > 0) {\n", javaName)
	b.WriteString("JsonObject mapObj = new JsonObject();\n")
	fmt.Fprintf(b, "for (var entry : msg.get%sMap().entrySet()) {\n", javaName)

	// For non-string map keys, convert to String for JSON property names
	keyExpr := "entry.getKey()"
	if field.MapKeyType != "string" {
		keyExpr = "String.valueOf(entry.getKey())"
	}

	if field.MapValueIsMessage {
		if isWellKnownTypePackage(field.MapValueTypePackage) {
			b.WriteString("try {\n")
			fmt.Fprintf(b, "mapObj.add(%s, gson.fromJson(JsonFormat.printer().print(entry.getValue()), JsonElement.class));\n", keyExpr)
			b.WriteString("} catch (Exception e) {}\n")
		} else {
			toFunc := strutil.ToLowerFirst(field.MapValueType) + "ToJson"
			if fq, ok := crossHelpers[field.MapValueTypePackage]; ok && field.MapValueTypePackage != "" {
				toFunc = fq + "." + toFunc
			}
			fmt.Fprintf(b, "try { mapObj.add(%s, gson.fromJson(%s(entry.getValue()), JsonElement.class)); } catch (Exception e) {}\n", keyExpr, toFunc)
		}
	} else {
		switch field.MapValueType {
		case "string":
			fmt.Fprintf(b, "mapObj.addProperty(%s, entry.getValue());\n", keyExpr)
		case "int32", "sint32", "sfixed32", "fixed32", "uint32":
			fmt.Fprintf(b, "mapObj.addProperty(%s, entry.getValue());\n", keyExpr)
		case "int64", "sint64", "sfixed64", "fixed64", "uint64":
			fmt.Fprintf(b, "mapObj.addProperty(%s, entry.getValue());\n", keyExpr)
		case "bool":
			fmt.Fprintf(b, "mapObj.addProperty(%s, entry.getValue());\n", keyExpr)
		case "float", "double":
			fmt.Fprintf(b, "mapObj.addProperty(%s, entry.getValue());\n", keyExpr)
		default:
			fmt.Fprintf(b, "mapObj.addProperty(%s, String.valueOf(entry.getValue()));\n", keyExpr)
		}
	}
	b.WriteString("}\n")
	fmt.Fprintf(b, "obj.add(%q, mapObj);\n", jsonName)
	b.WriteString("}\n")
}

func writeToJsonOneOfField(b *strings.Builder, field model.Field, javaName, jsonName string, crossHelpers map[string]string) {
	if field.IsEnum {
		toStrFunc := enumToStringFunc(field, crossHelpers)
		fmt.Fprintf(b, "if (msg.get%sValue() != 0) {\n", javaName)
		fmt.Fprintf(b, "obj.addProperty(%q, %s(msg.get%s()));\n", jsonName, toStrFunc, javaName)
		b.WriteString("}\n")
	} else if field.IsMessage {
		if isWellKnownTypePackage(field.TypePackage) {
			fmt.Fprintf(b, "if (msg.has%s()) {\n", javaName)
			b.WriteString("try {\n")
			fmt.Fprintf(b, "obj.add(%q, gson.fromJson(JsonFormat.printer().print(msg.get%s()), JsonElement.class));\n", jsonName, javaName)
			b.WriteString("} catch (Exception e) {}\n")
			b.WriteString("}\n")
		} else {
			toFunc := msgToJsonFunc(field, crossHelpers)
			fmt.Fprintf(b, "if (msg.has%s()) {\n", javaName)
			b.WriteString("try {\n")
			fmt.Fprintf(b, "obj.add(%q, gson.fromJson(%s(msg.get%s()), JsonElement.class));\n", jsonName, toFunc, javaName)
			b.WriteString("} catch (Exception e) {}\n")
			b.WriteString("}\n")
		}
	} else {
		// Scalar oneof
		writeToJsonScalarField(b, field, javaName, jsonName)
	}
}

func writeToJsonScalarField(b *strings.Builder, field model.Field, javaName, jsonName string) {
	if field.IsRepeated {
		fmt.Fprintf(b, "if (msg.get%sCount() > 0) {\n", javaName)
		b.WriteString("JsonArray arr = new JsonArray();\n")
		fmt.Fprintf(b, "for (int i = 0; i < msg.get%sCount(); i++) {\n", javaName)
		if field.Type == "bytes" {
			fmt.Fprintf(b, "arr.add(Base64.getEncoder().encodeToString(msg.get%s(i).toByteArray()));\n", javaName)
		} else {
			fmt.Fprintf(b, "arr.add(msg.get%s(i));\n", javaName)
		}
		b.WriteString("}\n")
		fmt.Fprintf(b, "obj.add(%q, arr);\n", jsonName)
		b.WriteString("}\n")
	} else {
		switch field.Type {
		case "string":
			fmt.Fprintf(b, "if (!msg.get%s().isEmpty()) {\n", javaName)
			fmt.Fprintf(b, "obj.addProperty(%q, msg.get%s());\n", jsonName, javaName)
			b.WriteString("}\n")
		case "bool":
			fmt.Fprintf(b, "if (msg.get%s()) {\n", javaName)
			fmt.Fprintf(b, "obj.addProperty(%q, msg.get%s());\n", jsonName, javaName)
			b.WriteString("}\n")
		case "bytes":
			fmt.Fprintf(b, "if (!msg.get%s().isEmpty()) {\n", javaName)
			fmt.Fprintf(b, "obj.addProperty(%q, Base64.getEncoder().encodeToString(msg.get%s().toByteArray()));\n", jsonName, javaName)
			b.WriteString("}\n")
		default:
			// Numeric types — emit if non-zero
			fmt.Fprintf(b, "if (msg.get%s() != 0) {\n", javaName)
			fmt.Fprintf(b, "obj.addProperty(%q, msg.get%s());\n", jsonName, javaName)
			b.WriteString("}\n")
		}
	}
}

// protoTypeToJavaBoxed returns Java boxed types for map iteration.
func protoTypeToJavaBoxed(protoType string) string {
	switch protoType {
	case "string":
		return "String"
	case "int32", "sint32", "sfixed32", "fixed32", "uint32":
		return "Integer"
	case "int64", "sint64", "sfixed64", "fixed64", "uint64":
		return "Long"
	case "float":
		return "Float"
	case "double":
		return "Double"
	case "bool":
		return "Boolean"
	default:
		return "String"
	}
}

// writeMessageSerialization writes serialization methods for a single message type.
// Field-by-field JSON walking for parity with Go's serial.go — no JsonFormat.parser().
func writeMessageSerialization(b *strings.Builder, msg model.Message, outerClassName string, crossHelpers map[string]string) {
	fullTypeName := outerClassName + "." + msg.Name
	lowerName := strutil.ToLowerFirst(msg.Name)

	// toJson method — field-by-field serialization using xxxToString for enums, xxxToJson for messages
	fmt.Fprintf(b, "public static String %sToJson(%s msg) throws ProtoException {\n", lowerName, fullTypeName)
	b.WriteString("if (msg == null) {\n")
	fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"%s is null\");\n", msg.Name)
	b.WriteString("}\n")
	b.WriteString("try {\n")
	b.WriteString("Gson gson = new Gson();\n")
	b.WriteString("JsonObject obj = new JsonObject();\n")
	for _, field := range msg.Fields {
		writeToJsonFieldAssignment(b, field, fullTypeName, crossHelpers)
	}
	b.WriteString("return gson.toJson(obj);\n")
	b.WriteString("} catch (ProtoException e) {\n")
	b.WriteString("throw e;\n")
	b.WriteString("} catch (Exception e) {\n")
	fmt.Fprintf(b, "throw new ProtoException(ProtoException.INTERNAL, \"Failed to serialize %s to JSON: \" + e.getMessage(), e);\n", msg.Name)
	b.WriteString("}\n")
	b.WriteString("}\n")

	// toJsonPretty method — same as toJson (pretty formatting done by Gson)
	fmt.Fprintf(b, "public static String %sToJsonPretty(%s msg) throws ProtoException {\n", lowerName, fullTypeName)
	b.WriteString("if (msg == null) {\n")
	fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"%s is null\");\n", msg.Name)
	b.WriteString("}\n")
	b.WriteString("try {\n")
	b.WriteString("Gson gson = new GsonBuilder().setPrettyPrinting().create();\n")
	b.WriteString("JsonObject obj = new JsonObject();\n")
	for _, field := range msg.Fields {
		writeToJsonFieldAssignment(b, field, fullTypeName, crossHelpers)
	}
	b.WriteString("return gson.toJson(obj);\n")
	b.WriteString("} catch (ProtoException e) {\n")
	b.WriteString("throw e;\n")
	b.WriteString("} catch (Exception e) {\n")
	fmt.Fprintf(b, "throw new ProtoException(ProtoException.INTERNAL, \"Failed to serialize %s to JSON: \" + e.getMessage(), e);\n", msg.Name)
	b.WriteString("}\n")
	b.WriteString("}\n")

	// toBinary method (unchanged)
	fmt.Fprintf(b, "public static byte[] %sToBinary(%s msg) throws ProtoException {\n", lowerName, fullTypeName)
	b.WriteString("if (msg == null) {\n")
	fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"%s is null\");\n", msg.Name)
	b.WriteString("}\n")
	b.WriteString("return msg.toByteArray();\n")
	b.WriteString("}\n")

	// fromBytes method (unchanged - delegates to fromJson/fromYaml/etc)
	fmt.Fprintf(b, "public static %s %sFromBytes(byte[] data) throws ProtoException {\n", fullTypeName, lowerName)
	b.WriteString("if (data == null || data.length == 0) {\n")
	b.WriteString("throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"data is null or empty\");\n")
	b.WriteString("}\n")
	b.WriteString("String dataStr = new String(data).trim();\n")
	b.WriteString("if (dataStr.isEmpty()) {\n")
	b.WriteString("throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"empty data after trimming\");\n")
	b.WriteString("}\n")
	b.WriteString("if (dataStr.startsWith(\"{\") || dataStr.startsWith(\"[\")) {\n")
	b.WriteString("try {\n")
	fmt.Fprintf(b, "return %sFromJson(dataStr);\n", lowerName)
	b.WriteString("} catch (Exception e) {\n")
	b.WriteString("// Fall through to try other formats\n")
	b.WriteString("}\n")
	b.WriteString("}\n")
	b.WriteString("if (dataStr.contains(\":\") && !dataStr.contains(\"=\")) {\n")
	b.WriteString("try {\n")
	fmt.Fprintf(b, "return %sFromYaml(dataStr);\n", lowerName)
	b.WriteString("} catch (Exception e) {\n")
	b.WriteString("// Fall through to try other formats\n")
	b.WriteString("}\n")
	b.WriteString("}\n")
	b.WriteString("if (dataStr.contains(\"=\")) {\n")
	b.WriteString("try {\n")
	fmt.Fprintf(b, "return %sFromToml(dataStr);\n", lowerName)
	b.WriteString("} catch (Exception e) {\n")
	b.WriteString("// Fall through to try binary\n")
	b.WriteString("}\n")
	b.WriteString("}\n")
	b.WriteString("try {\n")
	fmt.Fprintf(b, "return %sFromBinary(data);\n", lowerName)
	b.WriteString("} catch (Exception e) {\n")
	fmt.Fprintf(b, "throw new ProtoException(ProtoException.INTERNAL, \"Failed to parse %s: unsupported format or invalid data: \" + e.getMessage(), e);\n", msg.Name)
	b.WriteString("}\n")
	b.WriteString("}\n")

	// fromJson method — field-by-field JSON walking. No JsonFormat.parser().
	// Parity with Go's FromJSON: parse with Gson, walk each field, use parseXxx
	// for enums and xxxFromJson for nested messages.
	fmt.Fprintf(b, "public static %s %sFromJson(String json) throws ProtoException {\n", fullTypeName, lowerName)
	b.WriteString("if (json == null || json.isEmpty()) {\n")
	b.WriteString("throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"json is null or empty\");\n")
	b.WriteString("}\n")
	b.WriteString("try {\n")
	fmt.Fprintf(b, "%s.Builder builder = %s.newBuilder();\n", fullTypeName, fullTypeName)
	if len(msg.Fields) > 0 {
		b.WriteString("Gson gson = new Gson();\n")
		b.WriteString("JsonObject obj = gson.fromJson(json, JsonObject.class);\n")
		for _, field := range msg.Fields {
			writeFromJsonFieldAssignment(b, field, fullTypeName, crossHelpers)
		}
	}
	b.WriteString("return builder.build();\n")
	b.WriteString("} catch (ProtoException e) {\n")
	b.WriteString("throw e;\n")
	b.WriteString("} catch (Exception e) {\n")
	fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"Failed to parse %s from JSON: \" + e.getMessage(), e);\n", msg.Name)
	b.WriteString("}\n")
	b.WriteString("}\n")

	// fromBinary method (unchanged)
	fmt.Fprintf(b, "public static %s %sFromBinary(byte[] data) throws ProtoException {\n", fullTypeName, lowerName)
	b.WriteString("if (data == null || data.length == 0) {\n")
	b.WriteString("throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"data is null or empty\");\n")
	b.WriteString("}\n")
	b.WriteString("try {\n")
	fmt.Fprintf(b, "return %s.parseFrom(data);\n", fullTypeName)
	b.WriteString("} catch (InvalidProtocolBufferException e) {\n")
	fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"Failed to parse %s from binary: \" + e.getMessage(), e);\n", msg.Name)
	b.WriteString("}\n")
	b.WriteString("}\n")

	// toYaml method — uses toJson for enum correctness
	fmt.Fprintf(b, "public static String %sToYaml(%s msg) throws ProtoException {\n", lowerName, fullTypeName)
	b.WriteString("if (msg == null) {\n")
	fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"%s is null\");\n", msg.Name)
	b.WriteString("}\n")
	b.WriteString("try {\n")
	fmt.Fprintf(b, "String json = %sToJson(msg);\n", lowerName)
	b.WriteString("Gson gson = new Gson();\n")
	// TypeToken provides full generic type info, so no unchecked cast needed
	b.WriteString("Map<String, Object> map = gson.fromJson(json, new com.google.gson.reflect.TypeToken<Map<String, Object>>(){}.getType());\n")
	b.WriteString("Yaml yaml = new Yaml();\n")
	b.WriteString("return yaml.dump(map);\n")
	b.WriteString("} catch (ProtoException e) {\n")
	b.WriteString("throw e;\n")
	b.WriteString("} catch (Exception e) {\n")
	fmt.Fprintf(b, "throw new ProtoException(ProtoException.INTERNAL, \"Failed to serialize %s to YAML: \" + e.getMessage(), e);\n", msg.Name)
	b.WriteString("}\n")
	b.WriteString("}\n")

	// fromYaml method (unchanged — already delegates to fromJson)
	fmt.Fprintf(b, "public static %s %sFromYaml(String yamlStr) throws ProtoException {\n", fullTypeName, lowerName)
	b.WriteString("if (yamlStr == null || yamlStr.isEmpty()) {\n")
	b.WriteString("throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"yamlStr is null or empty\");\n")
	b.WriteString("}\n")
	b.WriteString("try {\n")
	b.WriteString("Yaml yaml = new Yaml();\n")
	b.WriteString("@SuppressWarnings(\"unchecked\")\n")
	b.WriteString("Map<String, Object> map = (Map<String, Object>) yaml.load(yamlStr);\n")
	b.WriteString("Gson gson = new Gson();\n")
	b.WriteString("String json = gson.toJson(map);\n")
	fmt.Fprintf(b, "return %sFromJson(json);\n", lowerName)
	b.WriteString("} catch (ProtoException e) {\n")
	b.WriteString("throw e;\n")
	b.WriteString("} catch (Exception e) {\n")
	fmt.Fprintf(b, "throw new ProtoException(ProtoException.INTERNAL, \"Failed to parse %s from YAML: \" + e.getMessage(), e);\n", msg.Name)
	b.WriteString("}\n")
	b.WriteString("}\n")

	// toToml method — uses toJson for enum correctness
	fmt.Fprintf(b, "public static String %sToToml(%s msg) throws ProtoException {\n", lowerName, fullTypeName)
	b.WriteString("if (msg == null) {\n")
	fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"%s is null\");\n", msg.Name)
	b.WriteString("}\n")
	b.WriteString("try {\n")
	fmt.Fprintf(b, "String json = %sToJson(msg);\n", lowerName)
	b.WriteString("Gson gson = new Gson();\n")
	// TypeToken provides full generic type info, so no unchecked cast needed
	b.WriteString("Map<String, Object> map = gson.fromJson(json, new com.google.gson.reflect.TypeToken<Map<String, Object>>(){}.getType());\n")
	b.WriteString("StringBuilder toml = new StringBuilder();\n")
	b.WriteString("if (map == null || map.isEmpty()) {\n")
	fmt.Fprintf(b, "return \"# Empty %s\\n\";\n", msg.Name)
	b.WriteString("}\n")
	b.WriteString("for (Map.Entry<String, Object> entry : map.entrySet()) {\n")
	b.WriteString("toml.append(entry.getKey()).append(\" = \");\n")
	b.WriteString("if (entry.getValue() instanceof String) {\n")
	b.WriteString("toml.append(\"\\\"\").append(entry.getValue()).append(\"\\\"\");\n")
	b.WriteString("} else {\n")
	b.WriteString("toml.append(entry.getValue());\n")
	b.WriteString("}\n")
	b.WriteString("toml.append(\"\\n\");\n")
	b.WriteString("}\n")
	b.WriteString("return toml.toString();\n")
	b.WriteString("} catch (ProtoException e) {\n")
	b.WriteString("throw e;\n")
	b.WriteString("} catch (Exception e) {\n")
	fmt.Fprintf(b, "throw new ProtoException(ProtoException.INTERNAL, \"Failed to serialize %s to TOML: \" + e.getMessage(), e);\n", msg.Name)
	b.WriteString("}\n")
	b.WriteString("}\n")

	// fromToml method (unchanged — already delegates to fromJson)
	fmt.Fprintf(b, "public static %s %sFromToml(String tomlStr) throws ProtoException {\n", fullTypeName, lowerName)
	b.WriteString("if (tomlStr == null || tomlStr.isEmpty()) {\n")
	b.WriteString("throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"tomlStr is null or empty\");\n")
	b.WriteString("}\n")
	b.WriteString("try {\n")
	b.WriteString("Toml toml = new Toml().read(tomlStr);\n")
	b.WriteString("Map<String, Object> map = toml.toMap();\n")
	b.WriteString("Gson gson = new Gson();\n")
	b.WriteString("String json = gson.toJson(map);\n")
	fmt.Fprintf(b, "return %sFromJson(json);\n", lowerName)
	b.WriteString("} catch (ProtoException e) {\n")
	b.WriteString("throw e;\n")
	b.WriteString("} catch (Exception e) {\n")
	fmt.Fprintf(b, "throw new ProtoException(ProtoException.INTERNAL, \"Failed to parse %s from TOML: \" + e.getMessage(), e);\n", msg.Name)
	b.WriteString("}\n")
	b.WriteString("}\n")
}

// writeFromMap writes static fromMap/toMap methods into the Helpers class
func writeFromMap(b *strings.Builder, pf model.ProtoFile, outerClassName string) {
	for _, msg := range pf.Messages {
		if msg.ParamMapConfig != nil {
			writeFromMapMethods(b, msg, pf, msgOuterClass(msg, outerClassName))
		}
	}
}

// writeFromMapMethods writes fromMap and toMap methods for a single message
func writeFromMapMethods(b *strings.Builder, msg model.Message, pf model.ProtoFile, outerClassName string) {
	config := msg.ParamMapConfig
	keyEnumType := config.KeyEnum

	// Find the key enum
	var keyEnum *model.Enum
	for i := range pf.Enums {
		if pf.Enums[i].Name == keyEnumType {
			keyEnum = &pf.Enums[i]
			break
		}
	}

	if keyEnum == nil {
		return
	}

	// Build a map of enum value name -> json_return_value
	enumValueToJsonValue := make(map[string]string)
	for _, enumVal := range keyEnum.Values {
		if enumVal.JsonValue != "" {
			enumValueToJsonValue[enumVal.Name] = enumVal.JsonValue
		}
	}

	// Generate fromMap method
	fmt.Fprintf(b, "public static %s.%s.Builder %sFromMap(Map<String, String> params) throws ProtoException {\n",
		outerClassName, msg.Name, strutil.ToLowerFirst(msg.Name))
	fmt.Fprintf(b, "%s.%s.Builder builder = %s.%s.newBuilder();\n", outerClassName, msg.Name, outerClassName, msg.Name)

	b.WriteString("for (Map.Entry<String, String> entry : params.entrySet()) {\n")
	b.WriteString("String key = entry.getKey();\n")
	b.WriteString("String value = entry.getValue();\n")

	fmt.Fprintf(b, "%s.%s paramKey;\n", outerClassName, keyEnumType)
	b.WriteString("try {\n")
	fmt.Fprintf(b, "paramKey = %s.%s.valueOf(key.toUpperCase().replace('-', '_'));\n", outerClassName, keyEnumType)
	b.WriteString("} catch (IllegalArgumentException e) {\n")
	if config.Strict {
		b.WriteString("throw new ProtoException(ProtoException.INVALID_ARGUMENT,\"Invalid parameter key: \" + key);\n")
	} else {
		b.WriteString("continue; // Ignore unknown keys in non-strict mode\n")
	}
	b.WriteString("}\n")

	b.WriteString("switch (paramKey) {\n")

	for _, field := range msg.Fields {
		if field.ParamKey == "" {
			continue
		}

		fmt.Fprintf(b, "case %s -> {\n", field.ParamKey)

		setterName := "set" + javaFieldNameUpper(field.Name)

		isEnum := field.IsEnum || isEnumType(field.Type, pf.Enums)

		if field.IsRepeated {
			b.WriteString("if (value != null && !value.isEmpty()) {\n")
			if isEnum {
				b.WriteString("String[] parts = value.split(\",\");\n")
				b.WriteString("for (String part : parts) {\n")
				b.WriteString("try {\n")
				fmt.Fprintf(b, "builder.add%s(%s.%s.valueOf(part.toUpperCase().replace('-', '_')));\n",
					javaFieldNameUpper(field.Name), outerClassName, field.Type)
				b.WriteString("} catch (IllegalArgumentException e) {\n")
				b.WriteString("// Ignore invalid enum values\n")
				b.WriteString("}\n")
				b.WriteString("}\n")
			} else {
				fmt.Fprintf(b, "builder.addAll%s(Arrays.asList(value.split(\",\")));\n",
					javaFieldNameUpper(field.Name))
			}
			b.WriteString("}\n")
		} else if isEnum {
			b.WriteString("try {\n")
			fmt.Fprintf(b, "builder.%s(%s.%s.valueOf(value.toUpperCase().replace('-', '_')));\n",
				setterName, outerClassName, field.Type)
			b.WriteString("} catch (IllegalArgumentException e) {\n")
			b.WriteString("// Ignore invalid enum values\n")
			b.WriteString("}\n")
		} else if isIntegerType(field.Type) {
			b.WriteString("try {\n")
			if field.Type == "int64" {
				fmt.Fprintf(b, "builder.%s(Long.parseLong(value));\n", setterName)
			} else {
				fmt.Fprintf(b, "builder.%s(Integer.parseInt(value));\n", setterName)
			}
			b.WriteString("} catch (NumberFormatException e) {\n")
			b.WriteString("// Ignore invalid numbers\n")
			b.WriteString("}\n")
		} else if isFloatType(field.Type) {
			b.WriteString("try {\n")
			fmt.Fprintf(b, "builder.%s(Double.parseDouble(value));\n", setterName)
			b.WriteString("} catch (NumberFormatException e) {\n")
			b.WriteString("// Ignore invalid numbers\n")
			b.WriteString("}\n")
		} else {
			fmt.Fprintf(b, "builder.%s(value);\n", setterName)
		}
		b.WriteString("}\n") // close case arrow block
	}

	b.WriteString("default -> {\n")
	b.WriteString("// Unknown enum value\n")
	b.WriteString("}\n")
	b.WriteString("}\n")
	b.WriteString("}\n")
	b.WriteString("return builder;\n")
	b.WriteString("}\n")

	// Generate toMap method
	fmt.Fprintf(b, "public static Map<String, String> %sToMap(%s.%s m) {\n",
		strutil.ToLowerFirst(msg.Name), outerClassName, msg.Name)
	b.WriteString("Map<String, String> result = new HashMap<>();\n")
	b.WriteString("if (m == null) {\n")
	b.WriteString("return result;\n")
	b.WriteString("}\n")

	for _, field := range msg.Fields {
		if field.ParamKey == "" {
			continue
		}

		jsonKey, hasJsonKey := enumValueToJsonValue[field.ParamKey]
		if !hasJsonKey {
			continue
		}

		getterName := "get" + javaFieldNameUpper(field.Name)

		isEnum := field.IsEnum || isEnumType(field.Type, pf.Enums)

		if field.IsRepeated {
			getterListName := getterName + "List"
			fmt.Fprintf(b, "if (!m.%s().isEmpty()) {\n", getterListName)
			if isEnum {
				fmt.Fprintf(b, "result.put(\"%s\", m.%s().stream()\n", jsonKey, getterListName)
				b.WriteString(".map(e -> e.name().toLowerCase().replace('_', '-'))\n")
				b.WriteString(".collect(Collectors.joining(\",\")));\n")
			} else {
				fmt.Fprintf(b, "result.put(\"%s\", String.join(\",\", m.%s()));\n", jsonKey, getterListName)
			}
			b.WriteString("}\n")
		} else if isEnum {
			fmt.Fprintf(b, "if (m.has%s() && m.%s() != %s.%s.UNRECOGNIZED) {\n",
				javaFieldNameUpper(field.Name), getterName,
				outerClassName, field.Type)
			fmt.Fprintf(b, "result.put(\"%s\", m.%s().name().toLowerCase().replace('_', '-'));\n", jsonKey, getterName)
			b.WriteString("}\n")
		} else if strings.Contains(field.Type, "int") || strings.Contains(field.Type, "double") || strings.Contains(field.Type, "float") { // proto scalar type names (int32, int64, sint32, double, float)
			fmt.Fprintf(b, "if (m.has%s()) {\n", javaFieldNameUpper(field.Name))
			fmt.Fprintf(b, "result.put(\"%s\", String.valueOf(m.%s()));\n", jsonKey, getterName)
			b.WriteString("}\n")
		} else {
			fmt.Fprintf(b, "if (m.has%s() && !m.%s().isEmpty()) {\n",
				javaFieldNameUpper(field.Name), getterName)
			fmt.Fprintf(b, "result.put(\"%s\", m.%s());\n", jsonKey, getterName)
			b.WriteString("}\n")
		}
	}

	b.WriteString("return result;\n")
	b.WriteString("}\n")
}

// hasDefaultHostAnnotation returns true if any field in any message has a default_host annotation.
func hasDefaultHostAnnotation(pf model.ProtoFile) bool {
	for _, msg := range pf.Messages {
		for _, field := range msg.Fields {
			if av, ok := field.Annotations["default_host"]; ok && av.HostValue != nil {
				return true
			}
		}
	}
	return false
}

// messagesWithDefaults returns the set of messages across all parsed files that
// have defaults, keyed by model.DefaultsKey. A message has defaults if it has
// direct default annotations on its fields, if it has assert_required message
// fields (which need nil-initialization), or if it has a non-repeated, non-map
// message field whose type (same package or another) has defaults.
func messagesWithDefaults(allFiles []model.ProtoFile) map[string]bool {
	direct := make(map[string]bool)
	for _, pf := range allFiles {
		for _, msg := range pf.Messages {
			for _, field := range msg.Fields {
				for _, key := range defaultAnnotationKeys {
					if _, ok := field.Annotations[key]; ok {
						direct[model.DefaultsKey(pf.Package, msg.Name)] = true
					}
				}
				if _, ok := field.Annotations["default_const"]; ok {
					direct[model.DefaultsKey(pf.Package, msg.Name)] = true
				}
				// assert_required on message fields — Defaults must initialize nil fields
				if field.IsMessage && !field.IsRepeated && !field.IsMap && !field.IsOneOf && field.TypePackage == "" {
					if _, ok := field.Annotations["assert_required"]; ok {
						direct[model.DefaultsKey(pf.Package, msg.Name)] = true
					}
				}
			}
		}
	}

	return model.ExpandDefaults(allFiles, direct)
}

// writeTypeDefaults writes type defaults methods that apply default values to message builders
func writeTypeDefaults(b *strings.Builder, pf model.ProtoFile, outerClassName string, config *tactype.Config, hasDefaults map[string]bool) {
	crossHelpers := buildCrossPackageHelpersMap(pf, config)
	dupFields := findDuplicateDefaultFields(pf.Messages)

	for _, msg := range pf.Messages {
		fullTypeName := msgOuterClass(msg, outerClassName) + "." + msg.Name

		fmt.Fprintf(b, "/** Applies default values to zero-valued fields of %s. */\n", msg.Name)
		fmt.Fprintf(b, "public static %s.Builder %sDefaults(%s msg) {\n",
			fullTypeName, strutil.ToLowerFirst(msg.Name), fullTypeName)
		fmt.Fprintf(b, "if (msg == null) { return null; }\n")
		fmt.Fprintf(b, "%s.Builder builder = msg.toBuilder();\n", fullTypeName)

		// Direct field defaults
		for _, field := range msg.Fields {
			for _, annKey := range defaultAnnotationKeys {
				_, ok := field.Annotations[annKey]
				if !ok {
					continue
				}

				javaFieldName := javaFieldNameUpper(field.Name)
				getterName := "get" + javaFieldName
				constName := strutil.DefaultConstName(msg.Name, field.Name, dupFields)
				setterName := "set" + javaFieldName

				switch annKey {
				case "default_string":
					fmt.Fprintf(b, "if (msg.%s().isEmpty()) {\n", getterName)
					fmt.Fprintf(b, "builder.%s(Helpers.%s);\n", setterName, constName)
					b.WriteString("}\n")
				case "default_int":
					fmt.Fprintf(b, "if (msg.%s() == 0) {\n", getterName)
					if field.Type == "int32" || field.Type == "uint32" || field.Type == "sint32" || field.Type == "fixed32" || field.Type == "sfixed32" {
						fmt.Fprintf(b, "builder.%s((int) Helpers.%s);\n", setterName, constName)
					} else {
						fmt.Fprintf(b, "builder.%s(Helpers.%s);\n", setterName, constName)
					}
					b.WriteString("}\n")
				case "default_float":
					fmt.Fprintf(b, "if (msg.%s() == 0.0) {\n", getterName)
					fmt.Fprintf(b, "builder.%s(Helpers.%s);\n", setterName, constName)
					b.WriteString("}\n")
				case "default_bool":
					fmt.Fprintf(b, "if (!msg.%s()) {\n", getterName)
					fmt.Fprintf(b, "builder.%s(Helpers.%s);\n", setterName, constName)
					b.WriteString("}\n")
				case "default_host":
					av := field.Annotations[annKey]
					if av.HostValue != nil {
						fmt.Fprintf(b, "if (!msg.has%s()) {\n", javaFieldName)
						fmt.Fprintf(b, "builder.%s(HostOuterClass.Host.newBuilder().setHost(%q).setPort(%d).build());\n",
							setterName, av.HostValue.Host, av.HostValue.Port)
						b.WriteString("}\n")
					}
				}
			}

			// default_const: references a file-level constant by name
			if av, ok := field.Annotations["default_const"]; ok {
				refName := av.StringValue // e.g., "DEFAULT_PORT"
				javaFieldName := javaFieldNameUpper(field.Name)
				getterName := "get" + javaFieldName
				setterName := "set" + javaFieldName

				// Determine type from the file-level constant
				constType := ""
				for _, c := range pf.Constants {
					if c.Name == refName {
						constType = c.Type
						break
					}
				}

				switch constType {
				case "String", "string":
					fmt.Fprintf(b, "if (msg.%s().isEmpty()) {\n", getterName)
					fmt.Fprintf(b, "builder.%s(Helpers.%s);\n", setterName, refName)
					b.WriteString("}\n")
				default: // int64/long, float64/double
					fmt.Fprintf(b, "if (msg.%s() == 0) {\n", getterName)
					if field.Type == "int32" || field.Type == "uint32" || field.Type == "sint32" || field.Type == "fixed32" || field.Type == "sfixed32" {
						fmt.Fprintf(b, "builder.%s((int) Helpers.%s);\n", setterName, refName)
					} else {
						fmt.Fprintf(b, "builder.%s(Helpers.%s);\n", setterName, refName)
					}
					b.WriteString("}\n")
				}
			}
		}

		// Sub-message defaults (including assert_required initialization)
		for _, field := range msg.Fields {
			if field.IsOneOf || field.IsRepeated || field.IsMap || !field.IsMessage {
				continue
			}

			_, isRequired := field.Annotations["assert_required"]
			if field.TypePackage != "" {
				// Cross-package sub-message: apply its defaults when present, through the
				// other package's Helpers. assert_required initialization stays same-package.
				if !isRequired && hasDefaults[model.DefaultsKey(field.TypePackage, field.Type)] {
					javaFieldName := javaFieldNameUpper(field.Name)
					fmt.Fprintf(b, "if (msg.has%s()) {\n", javaFieldName)
					fmt.Fprintf(b, "builder.set%s(%s.%sDefaults(msg.get%s()).build());\n",
						javaFieldName, crossHelpers[field.TypePackage], strutil.ToLowerFirst(field.Type), javaFieldName)
					b.WriteString("}\n")
				}
				continue
			}
			transitive := hasDefaults[model.DefaultsKey(pf.Package, field.Type)]

			if !isRequired && !transitive {
				continue
			}

			javaFieldName := javaFieldNameUpper(field.Name)
			subFullType := fullTypeName[:strings.LastIndex(fullTypeName, ".")] + "." + field.Type

			if isRequired {
				// assert_required: initialize if not set, then apply defaults
				fmt.Fprintf(b, "if (!msg.has%s()) {\n", javaFieldName)
				fmt.Fprintf(b, "builder.set%s(%s.getDefaultInstance());\n", javaFieldName, subFullType)
				b.WriteString("}\n")
				if transitive {
					// Re-read from builder since we may have just set the default
					fmt.Fprintf(b, "builder.set%s(%sDefaults(builder.get%s()).build());\n",
						javaFieldName, strutil.ToLowerFirst(field.Type), javaFieldName)
				}
			} else {
				// Optional: only apply defaults if already set
				fmt.Fprintf(b, "if (msg.has%s()) {\n", javaFieldName)
				fmt.Fprintf(b, "builder.set%s(%sDefaults(msg.get%s()).build());\n",
					javaFieldName, strutil.ToLowerFirst(field.Type), javaFieldName)
				b.WriteString("}\n")
			}
		}

		b.WriteString("return builder;\n")
		b.WriteString("}\n")
	}
}

// writeTypeUndefaults generates the inverse of writeTypeDefaults: resets fields
// that match their default values back to zero so only user overrides are persisted.
func writeTypeUndefaults(b *strings.Builder, pf model.ProtoFile, outerClassName string, hasDefaults map[string]bool) {
	dupFields := findDuplicateDefaultFields(pf.Messages)

	for _, msg := range pf.Messages {
		fullTypeName := msgOuterClass(msg, outerClassName) + "." + msg.Name

		fmt.Fprintf(b, "/** Resets %s fields that match defaults back to zero for minimal persistence. */\n", msg.Name)
		fmt.Fprintf(b, "public static %s.Builder %sUndefaults(%s msg) {\n",
			fullTypeName, strutil.ToLowerFirst(msg.Name), fullTypeName)
		fmt.Fprintf(b, "if (msg == null) { return null; }\n")
		fmt.Fprintf(b, "%s.Builder builder = msg.toBuilder();\n", fullTypeName)

		for _, field := range msg.Fields {
			for _, annKey := range defaultAnnotationKeys {
				_, ok := field.Annotations[annKey]
				if !ok {
					continue
				}

				javaFieldName := javaFieldNameUpper(field.Name)
				getterName := "get" + javaFieldName
				constName := strutil.DefaultConstName(msg.Name, field.Name, dupFields)
				setterName := "set" + javaFieldName
				clearName := "clear" + javaFieldName

				switch annKey {
				case "default_string":
					fmt.Fprintf(b, "if (msg.%s().equals(Helpers.%s)) {\n", getterName, constName)
					fmt.Fprintf(b, "builder.%s();\n", clearName)
					b.WriteString("}\n")
				case "default_int":
					fmt.Fprintf(b, "if (msg.%s() == Helpers.%s) {\n", getterName, constName)
					fmt.Fprintf(b, "builder.%s(0);\n", setterName)
					b.WriteString("}\n")
				case "default_float":
					fmt.Fprintf(b, "if (msg.%s() == Helpers.%s) {\n", getterName, constName)
					fmt.Fprintf(b, "builder.%s(0.0);\n", setterName)
					b.WriteString("}\n")
				case "default_bool":
					fmt.Fprintf(b, "if (msg.%s() == Helpers.%s) {\n", getterName, constName)
					fmt.Fprintf(b, "builder.%s(false);\n", setterName)
					b.WriteString("}\n")
				case "default_host":
					av := field.Annotations[annKey]
					if av.HostValue != nil {
						fmt.Fprintf(b, "if (msg.has%s() && msg.%s().getHost().equals(%q) && msg.%s().getPort() == %d) {\n",
							javaFieldName, getterName, av.HostValue.Host, getterName, av.HostValue.Port)
						fmt.Fprintf(b, "builder.%s();\n", clearName)
						b.WriteString("}\n")
					}
				}
			}

			// default_const: references a file-level constant by name
			if av, ok := field.Annotations["default_const"]; ok {
				refName := av.StringValue
				javaFieldName := javaFieldNameUpper(field.Name)
				getterName := "get" + javaFieldName
				setterName := "set" + javaFieldName
				clearName := "clear" + javaFieldName

				constType := ""
				for _, c := range pf.Constants {
					if c.Name == refName {
						constType = c.Type
						break
					}
				}

				switch constType {
				case "String", "string":
					fmt.Fprintf(b, "if (msg.%s().equals(Helpers.%s)) {\n", getterName, refName)
					fmt.Fprintf(b, "builder.%s();\n", clearName)
					b.WriteString("}\n")
				default:
					fmt.Fprintf(b, "if (msg.%s() == Helpers.%s) {\n", getterName, refName)
					fmt.Fprintf(b, "builder.%s(0);\n", setterName)
					b.WriteString("}\n")
				}
			}
		}

		// Sub-message undefaults
		for _, field := range msg.Fields {
			if field.IsOneOf || field.IsRepeated || field.IsMap || !field.IsMessage || field.TypePackage != "" {
				continue
			}
			if !hasDefaults[model.DefaultsKey(pf.Package, field.Type)] {
				continue
			}
			javaFieldName := javaFieldNameUpper(field.Name)
			fmt.Fprintf(b, "if (msg.has%s()) {\n", javaFieldName)
			fmt.Fprintf(b, "builder.set%s(%sUndefaults(msg.get%s()).build());\n",
				javaFieldName, strutil.ToLowerFirst(field.Type), javaFieldName)
			b.WriteString("}\n")
		}

		b.WriteString("return builder;\n")
		b.WriteString("}\n")
	}
}

// writeMerge generates merge methods for every message in the proto file.
// Each method fills zero-valued fields in dst from src. dst takes precedence.
// Returns a Builder so callers can chain .build().
func writeMerge(b *strings.Builder, pf model.ProtoFile, outerClassName string) {
	// Build set of messages that will have merge methods (non-empty fields)
	hasMerge := map[string]bool{}
	for _, msg := range pf.Messages {
		if len(msg.Fields) > 0 {
			hasMerge[msg.Name] = true
		}
	}

	for _, msg := range pf.Messages {
		if len(msg.Fields) == 0 {
			continue
		}

		fullTypeName := msgOuterClass(msg, outerClassName) + "." + msg.Name
		funcName := "merge" + msg.Name

		fmt.Fprintf(b, "public static %s.Builder %s(%s dst, %s src) {\n",
			fullTypeName, funcName, fullTypeName, fullTypeName)
		fmt.Fprintf(b, "%s.Builder builder = dst.toBuilder();\n", fullTypeName)

		// Collect oneof groups so we can handle them after scalar fields
		oneofGroups := map[string][]model.Field{} // oneofName -> fields
		var oneofOrder []string                   // preserve insertion order

		for _, field := range msg.Fields {
			// Skip oneof fields — handled below as groups
			if field.IsOneOf {
				if _, seen := oneofGroups[field.OneOfName]; !seen {
					oneofOrder = append(oneofOrder, field.OneOfName)
				}
				oneofGroups[field.OneOfName] = append(oneofGroups[field.OneOfName], field)
				continue
			}

			javaFieldName := javaFieldNameUpper(field.Name)
			getterName := "get" + javaFieldName
			setterName := "set" + javaFieldName

			switch {
			case field.IsMap:
				mapGetterName := "get" + javaFieldName + "Map"
				putAllName := "putAll" + javaFieldName
				putName := "put" + javaFieldName
				if field.MapValueIsMessage && field.MapValueTypePackage == "" && hasMerge[field.MapValueType] {
					// Map with same-package message value: merge matching entries
					subFuncName := "merge" + field.MapValueType
					javaKeyType := protoTypeToJavaBoxed(field.MapKeyType)
					fmt.Fprintf(b, "if (dst.%s().isEmpty()) { builder.%s(src.%s()); }\n",
						mapGetterName, putAllName, mapGetterName)
					fmt.Fprintf(b, "else { for (%s mk : src.%s().keySet()) {\n",
						javaKeyType, mapGetterName)
					fmt.Fprintf(b, "if (dst.%s().containsKey(mk)) { builder.%s(mk, %s(dst.%s().get(mk), src.%s().get(mk)).build()); }\n",
						mapGetterName, putName, subFuncName, mapGetterName, mapGetterName)
					fmt.Fprintf(b, "else { builder.%s(mk, src.%s().get(mk)); }\n",
						putName, mapGetterName)
					b.WriteString("} }\n")
				} else {
					fmt.Fprintf(b, "if (dst.%s().isEmpty()) { builder.%s(src.%s()); }\n",
						mapGetterName, putAllName, mapGetterName)
				}
			case field.IsRepeated:
				listGetterName := "get" + javaFieldName + "List"
				addAllName := "addAll" + javaFieldName
				fmt.Fprintf(b, "if (dst.%s().isEmpty()) { builder.%s(src.%s()); }\n",
					listGetterName, addAllName, listGetterName)
			case field.IsMessage:
				hasName := "has" + javaFieldName
				if field.TypePackage == "" && hasMerge[field.Type] {
					// Same-package message with merge method: recurse if both have it
					subFuncName := "merge" + field.Type
					fmt.Fprintf(b, "if (!dst.%s() && src.%s()) { builder.%s(src.%s()); }\n",
						hasName, hasName, setterName, getterName)
					fmt.Fprintf(b, "else if (dst.%s() && src.%s()) { builder.%s(%s(dst.%s(), src.%s()).build()); }\n",
						hasName, hasName, setterName, subFuncName, getterName, getterName)
				} else {
					// Cross-package or empty message: take src if dst doesn't have it
					fmt.Fprintf(b, "if (!dst.%s() && src.%s()) { builder.%s(src.%s()); }\n",
						hasName, hasName, setterName, getterName)
				}
			case field.IsEnum:
				valueGetterName := "get" + javaFieldName + "Value"
				valueSetter := "set" + javaFieldName + "Value"
				fmt.Fprintf(b, "if (dst.%s() == 0) { builder.%s(src.%s()); }\n",
					valueGetterName, valueSetter, valueGetterName)
			case field.Type == "string":
				fmt.Fprintf(b, "if (dst.%s().isEmpty()) { builder.%s(src.%s()); }\n",
					getterName, setterName, getterName)
			case field.Type == "bool":
				fmt.Fprintf(b, "if (!dst.%s()) { builder.%s(src.%s()); }\n",
					getterName, setterName, getterName)
			case field.Type == "bytes":
				fmt.Fprintf(b, "if (dst.%s().isEmpty()) { builder.%s(src.%s()); }\n",
					getterName, setterName, getterName)
			case isIntegerType(field.Type):
				fmt.Fprintf(b, "if (dst.%s() == 0) { builder.%s(src.%s()); }\n",
					getterName, setterName, getterName)
			case isFloatType(field.Type):
				fmt.Fprintf(b, "if (dst.%s() == 0.0) { builder.%s(src.%s()); }\n",
					getterName, setterName, getterName)
			}
		}

		// Handle oneof groups: if dst has no variant set, copy from src
		for _, oneofName := range oneofOrder {
			fields := oneofGroups[oneofName]
			caseGetter := "get" + strutil.ToCamelCase(oneofName) + "Case"
			// Protobuf Java strips underscores from the oneof name in the NOT_SET constant:
			// e.g., oneof "start_from" → STARTFROM_NOT_SET (not START_FROM_NOT_SET)
			notSetValue := fullTypeName + "." + strutil.ToCamelCase(oneofName) + "Case." + strings.ToUpper(strings.ReplaceAll(oneofName, "_", "")) + "_NOT_SET"

			fmt.Fprintf(b, "if (dst.%s() == %s) {\n", caseGetter, notSetValue)
			fmt.Fprintf(b, "switch (src.%s()) {\n", caseGetter)
			for _, f := range fields {
				enumValue := strings.ToUpper(f.Name)
				fieldPascal := javaFieldNameUpper(f.Name)
				fmt.Fprintf(b, "case %s: builder.set%s(src.get%s()); break;\n",
					enumValue, fieldPascal, fieldPascal)
			}
			b.WriteString("default: break;\n")
			b.WriteString("}\n") // switch
			b.WriteString("}\n") // if
		}

		b.WriteString("return builder;\n")
		b.WriteString("}\n")
	}
}

// writeScopeToEntitlements generates the scopeToEntitlements static method for the entitlements
// Helpers class. The method scopes a full TokenEntitlements down to only exact grant tokens relevant to
// targetAud (the JWT audience string, which equals the proto package name of the target service).
// vServices are co-scoped with the physical service that hosts them (HostLabel).
// Returns an empty TokenEntitlements for unknown service names.
// Callers that need special handling for the IAM service itself (for example, returning the full
// entitlements for default-audience tokens) should check before delegating to this method.
func writeScopeToEntitlements(b *strings.Builder, svcRPCs []proto.ServiceRPCEntry, vsEntries []proto.VServiceEntry) {
	// Build hostTag → vservice names map for co-scoping.
	vsMap := make(map[string][]string)
	for _, vs := range vsEntries {
		vsMap[vs.HostLabel] = append(vsMap[vs.HostLabel], vs.Name)
	}

	b.WriteString("/**\n")
	b.WriteString(" * Scopes a full TokenEntitlements down to only the grants for targetAud.\n")
	b.WriteString(" * azp constraints are preserved whenever any grants survive.\n")
	b.WriteString(" * Returns an empty result for unknown service names.\n")
	b.WriteString(" */\n")
	b.WriteString("private static final java.util.logging.Logger SCOPE_LOG =\n")
	b.WriteString("        java.util.logging.Logger.getLogger(Helpers.class.getName() + \".scopeToEntitlements\");\n")
	b.WriteString("public static Entitlements.TokenEntitlements scopeToEntitlements(\n")
	b.WriteString("        Entitlements.TokenEntitlements entitlements, String targetAud) {\n")
	b.WriteString("    var b = Entitlements.TokenEntitlements.newBuilder();\n")
	b.WriteString("    java.util.Set<String> packages = new java.util.HashSet<>();\n")
	b.WriteString("    switch (targetAud) {\n")

	for _, svc := range svcRPCs {
		coVS := vsMap[svc.GrpcName]
		if len(coVS) == 0 {
			fmt.Fprintf(b, "        case %q -> packages.add(%q);\n",
				svc.GrpcName, svc.GrpcName)
		} else {
			fmt.Fprintf(b, "        case %q -> {\n", svc.GrpcName)
			fmt.Fprintf(b, "            packages.add(%q);\n", svc.GrpcName)
			for _, vsName := range coVS {
				fmt.Fprintf(b, "            packages.add(%q); // vservice\n", vsName)
			}
			b.WriteString("        }\n")
		}
	}

	b.WriteString("        default -> { return b.build(); }\n")
	b.WriteString("    }\n")
	b.WriteString("    java.util.List<String> unknownGrants = Grants.unknownTokens(entitlements.getGrantsList());\n")
	b.WriteString("    if (!unknownGrants.isEmpty()) {\n")
	b.WriteString("        SCOPE_LOG.warning(\"dropping unknown service_entitlements grant(s) while scoping for audience \"\n")
	b.WriteString("                + targetAud + \": \" + unknownGrants);\n")
	b.WriteString("    }\n")
	b.WriteString("    java.util.TreeSet<String> scoped = new java.util.TreeSet<>();\n")
	b.WriteString("    for (String grant : entitlements.getGrantsList()) {\n")
	b.WriteString("        for (String token : Grants.normalize(java.util.List.of(grant))) {\n")
	b.WriteString("            var grantInfo = Grants.lookup(token);\n")
	b.WriteString("            if (grantInfo.isPresent() && packages.contains(grantInfo.get().pkg)) {\n")
	b.WriteString("                scoped.add(token);\n")
	b.WriteString("            }\n")
	b.WriteString("        }\n")
	b.WriteString("    }\n")
	b.WriteString("    for (String token : scoped) {\n")
	b.WriteString("        b.addGrants(token);\n")
	b.WriteString("    }\n")
	b.WriteString("    b.addAllAzp(entitlements.getAzpList());\n")
	b.WriteString("    return b.build();\n")
	b.WriteString("}\n")
}
