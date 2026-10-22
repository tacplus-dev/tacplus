// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

const (
	specProtoDir  = "pkg/proto"
	specProtoFile = "sys/spec.proto"
	refDir        = "reference/annotations"
	typesPage     = "types.md"
)

// scope is one place a (spec.*) option can be set: one extended
// google.protobuf.*Options message.
type scope struct {
	Extendee protoreflect.FullName
	Name     string // "file", "field", ...
	Title    string
	Page     string // under refDir
	Syntax   string // how an option is written there
}

// scopes is every extendee spec.proto may extend, in page order. An extension
// of anything else is an error, so a new kind of option cannot silently
// vanish from the reference.
var scopes = []scope{
	{"google.protobuf.FileOptions", "file", "File options", "file.md",
		"Set at the top level of a `.proto` file:\n\n```protobuf\noption (spec.service) = {name: \"notes\"};\n```"},
	{"google.protobuf.ServiceOptions", "service", "Service options", "service.md",
		"Set inside a `service` block:\n\n```protobuf\nservice NotesApi {\n  option (spec.service_c2s) = {auth_entitled_users: true};\n}\n```"},
	{"google.protobuf.MethodOptions", "method", "Method options", "method.md",
		"Set inside an `rpc` body:\n\n```protobuf\nrpc GetNote(GetNoteRequest) returns (GetNoteResponse) {\n  option (spec.rpc) = {principal: RPC_PRINCIPAL_USER};\n}\n```"},
	{"google.protobuf.MessageOptions", "message", "Message options", "message.md",
		"Set inside a `message` block:\n\n```protobuf\nmessage NotesState {\n  option (spec.service_state) = {name: \"NotesState\"};\n}\n```"},
	{"google.protobuf.FieldOptions", "field", "Field options", "field.md",
		"Set in brackets after a field:\n\n```protobuf\nstring text = 2 [(spec.assert_required) = true, (spec.assert_max_size) = 1024];\n```"},
	{"google.protobuf.EnumValueOptions", "enum value", "Enum value options", "enum-value.md",
		"Set in brackets after an enum value:\n\n```protobuf\nSTATUS_OK = 1 [(spec.json_return_value) = \"ok\"];\n```"},
}

// option is one (spec.*) extension.
type option struct {
	Desc  protoreflect.ExtensionDescriptor
	Scope scope
}

// annotationPages renders the annotation reference: an index page, one page
// per scope and one page of the option types. Keys are page paths under the
// docs directory.
func annotationPages(root string) (map[string][]byte, error) {
	fd, err := loadSpec(root)
	if err != nil {
		return nil, err
	}
	opts, err := collectOptions(fd)
	if err != nil {
		return nil, err
	}
	pages := map[string][]byte{}
	pages[refDir+"/index.md"] = renderIndex(opts)
	for _, sc := range scopes {
		pages[refDir+"/"+sc.Page] = renderScope(fd, sc, opts)
	}
	pages[refDir+"/"+typesPage] = renderTypes(fd)
	return pages, nil
}

// loadSpec compiles spec.proto with protoc into a descriptor set that keeps
// the source comments. Like tacplus itself, it runs $PROTOC_BINARY if set,
// else protoc from PATH.
func loadSpec(root string) (protoreflect.FileDescriptor, error) {
	protoc := os.Getenv("PROTOC_BINARY")
	if protoc == "" {
		var err error
		if protoc, err = exec.LookPath("protoc"); err != nil {
			return nil, fmt.Errorf("protoc not found in PATH (or set PROTOC_BINARY): %w", err)
		}
	}
	tmp, err := os.MkdirTemp("", "sitegen-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	descPath := filepath.Join(tmp, "spec.pb")
	cmd := exec.Command(protoc,
		"--include_imports", "--include_source_info",
		"--proto_path="+filepath.Join(root, specProtoDir),
		"--descriptor_set_out="+descPath,
		specProtoFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("protoc: %w\n%s", err, out)
	}
	raw, err := os.ReadFile(descPath)
	if err != nil {
		return nil, err
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &set); err != nil {
		return nil, err
	}
	files, err := protodesc.NewFiles(&set)
	if err != nil {
		return nil, err
	}
	return files.FindFileByPath(specProtoFile)
}

// collectOptions returns every extension declared in fd, at top level or
// nested in a message, in declaration order.
func collectOptions(fd protoreflect.FileDescriptor) ([]option, error) {
	var exts []protoreflect.ExtensionDescriptor
	var walk func(protoreflect.MessageDescriptors)
	addAll := func(xs protoreflect.ExtensionDescriptors) {
		for i := 0; i < xs.Len(); i++ {
			exts = append(exts, xs.Get(i))
		}
	}
	walk = func(ms protoreflect.MessageDescriptors) {
		for i := 0; i < ms.Len(); i++ {
			addAll(ms.Get(i).Extensions())
			walk(ms.Get(i).Messages())
		}
	}
	addAll(fd.Extensions())
	walk(fd.Messages())

	var opts []option
	for _, x := range exts {
		sc, ok := scopeFor(x.ContainingMessage().FullName())
		if !ok {
			return nil, fmt.Errorf("spec.%s extends %s, which the reference has no page for; add it to scopes in cmd/sitegen", x.Name(), x.ContainingMessage().FullName())
		}
		opts = append(opts, option{Desc: x, Scope: sc})
	}
	return opts, nil
}

func scopeFor(extendee protoreflect.FullName) (scope, bool) {
	for _, sc := range scopes {
		if sc.Extendee == extendee {
			return sc, true
		}
	}
	return scope{}, false
}

func optionAnchor(x protoreflect.ExtensionDescriptor) string { return "spec-" + string(x.Name()) }

// typeAnchor is the anchor of a message or enum on the types page: its name
// relative to the spec package, dots as dashes.
func typeAnchor(d protoreflect.Descriptor) string {
	rel := strings.TrimPrefix(string(d.FullName()), string(d.ParentFile().Package())+".")
	return "type-" + strings.ReplaceAll(rel, ".", "-")
}

func typeRelName(d protoreflect.Descriptor) string {
	return strings.TrimPrefix(string(d.FullName()), string(d.ParentFile().Package())+".")
}

// fieldType renders a field's type, linking message and enum types declared
// in spec.proto to the types page (typesLink is the page path prefix to use,
// "" on the types page itself).
func fieldType(f protoreflect.FieldDescriptor, typesLink string) string {
	if f.IsMap() {
		return "`map<" + scalarOrName(f.MapKey()) + ", " + scalarOrName(f.MapValue()) + ">`"
	}
	prefix := ""
	if f.Cardinality() == protoreflect.Repeated {
		prefix = "`repeated` "
	}
	var d protoreflect.Descriptor
	switch f.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		d = f.Message()
	case protoreflect.EnumKind:
		d = f.Enum()
	}
	if d != nil && d.ParentFile().Path() == specProtoFile {
		return prefix + "[`" + typeRelName(d) + "`](" + typesLink + "#" + typeAnchor(d) + ")"
	}
	return prefix + "`" + scalarOrName(f) + "`"
}

func scalarOrName(f protoreflect.FieldDescriptor) string {
	switch f.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return string(f.Message().FullName())
	case protoreflect.EnumKind:
		return string(f.Enum().FullName())
	}
	return f.Kind().String()
}

// comment is the declaration's leading comment plus its trailing comment.
func comment(d protoreflect.Descriptor) string {
	loc := d.ParentFile().SourceLocations().ByDescriptor(d)
	parts := []string{}
	for _, c := range []string{loc.LeadingComments, loc.TrailingComments} {
		if c = strings.TrimRight(c, "\n "); strings.TrimSpace(c) != "" {
			parts = append(parts, c)
		}
	}
	return strings.Join(parts, "\n\n")
}

const generatedNotice = "<!-- Generated by cmd/sitegen from pkg/proto/sys/spec.proto. Do not edit; edit the proto comments instead. -->\n\n"

const specSourceURL = repoBlobURL + specProtoDir + "/" + specProtoFile

func renderIndex(opts []option) []byte {
	var b bytes.Buffer
	b.WriteString(generatedNotice)
	b.WriteString("# Annotation reference\n\n")
	fmt.Fprintf(&b, "Every `(spec.*)` option tacplus reads, generated from [`%s/%s`](%s) and its comments on every build of this site. ", specProtoDir, specProtoFile, specSourceURL)
	b.WriteString("Import it with `import \"sys/spec.proto\";`; tacplus supplies the file to every build.\n\n")
	b.WriteString("Options are grouped by where they are set. An option whose type is a message is written as a text-format message literal (`{name: \"notes\"}`); its fields are listed under [option types](" + typesPage + ").\n\n")
	b.WriteString("| Set on | Options |\n|---|---|\n")
	for _, sc := range scopes {
		n := 0
		for _, o := range opts {
			if o.Scope.Extendee == sc.Extendee {
				n++
			}
		}
		fmt.Fprintf(&b, "| [%s](%s) | %d |\n", sc.Name, sc.Page, n)
	}
	b.WriteString("\n## All options\n\n")
	b.WriteString("| Option | Set on | Type | Summary |\n|---|---|---|---|\n")
	for _, o := range opts {
		fmt.Fprintf(&b, "| [`spec.%s`](%s#%s) | %s | %s | %s |\n",
			o.Desc.Name(), o.Scope.Page, optionAnchor(o.Desc), o.Scope.Name,
			escapeTableCell(fieldType(o.Desc, typesPage)), escapeTableCell(summary(comment(o.Desc))))
	}
	return b.Bytes()
}

func renderScope(fd protoreflect.FileDescriptor, sc scope, opts []option) []byte {
	var b bytes.Buffer
	b.WriteString(generatedNotice)
	fmt.Fprintf(&b, "# %s\n\n%s\n\n", sc.Title, sc.Syntax)
	n := 0
	for _, o := range opts {
		if o.Scope.Extendee != sc.Extendee {
			continue
		}
		n++
		x := o.Desc
		fmt.Fprintf(&b, "## `spec.%s` {#%s}\n\n", x.Name(), optionAnchor(x))
		fmt.Fprintf(&b, "**Set on** %s · **Type** %s · **Number** `%d`\n\n", sc.Name, fieldType(x, typesPage), x.Number())
		if c := comment(x); c != "" {
			b.WriteString(renderComment(c))
			b.WriteString("\n")
		} else {
			b.WriteString("*No description in spec.proto.*\n\n")
		}
	}
	if n == 0 {
		b.WriteString("spec.proto declares no options here.\n")
	}
	return b.Bytes()
}

func renderTypes(fd protoreflect.FileDescriptor) []byte {
	var b bytes.Buffer
	b.WriteString(generatedNotice)
	b.WriteString("# Option types\n\n")
	b.WriteString("The messages and enums declared in `spec.proto`: the types of message-valued options and of their fields.\n\n")
	var msgs func(protoreflect.MessageDescriptors)
	var enums func(protoreflect.EnumDescriptors)
	enums = func(es protoreflect.EnumDescriptors) {
		for i := 0; i < es.Len(); i++ {
			e := es.Get(i)
			fmt.Fprintf(&b, "## `%s` {#%s}\n\n*enum*\n\n", typeRelName(e), typeAnchor(e))
			if c := comment(e); c != "" {
				b.WriteString(renderComment(c) + "\n")
			}
			vs := e.Values()
			for j := 0; j < vs.Len(); j++ {
				v := vs.Get(j)
				fmt.Fprintf(&b, "- `%s` = `%d`\n", v.Name(), v.Number())
				if c := comment(v); c != "" {
					b.WriteString("\n" + indent(renderComment(c), "    "))
				}
			}
			b.WriteString("\n")
		}
	}
	msgs = func(ms protoreflect.MessageDescriptors) {
		for i := 0; i < ms.Len(); i++ {
			m := ms.Get(i)
			if m.IsMapEntry() {
				continue
			}
			fmt.Fprintf(&b, "## `%s` {#%s}\n\n*message*\n\n", typeRelName(m), typeAnchor(m))
			if c := comment(m); c != "" {
				b.WriteString(renderComment(c) + "\n")
			}
			fs := m.Fields()
			if fs.Len() == 0 {
				b.WriteString("No fields.\n\n")
			}
			for j := 0; j < fs.Len(); j++ {
				f := fs.Get(j)
				oneof := ""
				if o := f.ContainingOneof(); o != nil && !o.IsSynthetic() {
					oneof = fmt.Sprintf(" · oneof `%s`", o.Name())
				}
				fmt.Fprintf(&b, "- `%s` %s = `%d`%s\n", f.Name(), fieldType(f, ""), f.Number(), oneof)
				if c := comment(f); c != "" {
					b.WriteString("\n" + indent(renderComment(c), "    "))
				}
			}
			b.WriteString("\n")
			enums(m.Enums())
			msgs(m.Messages())
		}
	}
	msgs(fd.Messages())
	enums(fd.Enums())
	return b.Bytes()
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n") + "\n\n"
}

func escapeTableCell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }
