// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os/exec"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	// The compiled spec package registers every (spec.*) extension; it is the
	// independent list the reference is checked against.
	_ "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

const repoRoot = "../.."

func requireProtoc(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not on PATH")
	}
}

// Every option spec.proto declares must have its own section on the page for
// where it is set, and a row in the index.
func TestReferenceCoversEveryOption(t *testing.T) {
	requireProtoc(t)
	pages, err := annotationPages(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	index := string(pages[refDir+"/index.md"])
	n := 0
	protoregistry.GlobalTypes.RangeExtensions(func(xt protoreflect.ExtensionType) bool {
		x := xt.TypeDescriptor()
		if x.ParentFile().Path() != specProtoFile {
			return true
		}
		n++
		sc, ok := scopeFor(x.ContainingMessage().FullName())
		if !ok {
			t.Errorf("spec.%s extends %s, which has no reference page", x.Name(), x.ContainingMessage().FullName())
			return true
		}
		page := string(pages[refDir+"/"+sc.Page])
		if heading := "## `spec." + string(x.Name()) + "` {#" + optionAnchor(x) + "}"; !strings.Contains(page, heading) {
			t.Errorf("%s: missing %q", sc.Page, heading)
		}
		if row := "| [`spec." + string(x.Name()) + "`](" + sc.Page + "#" + optionAnchor(x) + ")"; !strings.Contains(index, row) {
			t.Errorf("index.md: missing row %q", row)
		}
		return true
	})
	if n == 0 {
		t.Fatal("no spec.proto extensions registered; is target/proto/spec generated (just generate)?")
	}
}

// Every message and enum in spec.proto has an entry on the types page, so
// every type link from an option resolves.
func TestReferenceCoversEveryType(t *testing.T) {
	requireProtoc(t)
	fd, err := loadSpec(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	types := string(renderTypes(fd))
	var check func(protoreflect.MessageDescriptors)
	checkEnums := func(es protoreflect.EnumDescriptors) {
		for i := 0; i < es.Len(); i++ {
			if a := "{#" + typeAnchor(es.Get(i)) + "}"; !strings.Contains(types, a) {
				t.Errorf("types.md: missing %s", a)
			}
		}
	}
	check = func(ms protoreflect.MessageDescriptors) {
		for i := 0; i < ms.Len(); i++ {
			m := ms.Get(i)
			if m.IsMapEntry() {
				continue
			}
			if a := "{#" + typeAnchor(m) + "}"; !strings.Contains(types, a) {
				t.Errorf("types.md: missing %s", a)
			}
			checkEnums(m.Enums())
			check(m.Messages())
		}
	}
	check(fd.Messages())
	checkEnums(fd.Enums())
}

// The published documents stage without a broken relative link.
func TestStageDocs(t *testing.T) {
	if err := stageDocs(repoRoot, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestRewriteLinks(t *testing.T) {
	pages := map[string]string{
		"docs/USER.md":    "guide/user-guide.md",
		"docs/API.md":     "guide/generated-api.md",
		"CONTRIBUTING.md": "project/contributing.md",
	}
	exists := func(p string) (bool, bool) {
		switch p {
		case "docs/DEVELOPMENT.md":
			return true, false
		case "roadmap":
			return true, true
		}
		return false, false
	}
	for _, tc := range []struct{ in, want string }{
		{"see [API](API.md).", "see [API](generated-api.md)."},
		{"see [API](API.md#parity \"t\").", "see [API](generated-api.md#parity \"t\")."},
		{"[c](../CONTRIBUTING.md)", "[c](../project/contributing.md)"},
		{"[d](DEVELOPMENT.md#x)", "[d](https://github.com/tacplus-dev/tacplus/blob/main/docs/DEVELOPMENT.md#x)"},
		{"[r](../roadmap/)", "[r](https://github.com/tacplus-dev/tacplus/tree/main/roadmap)"},
		{"[a](#anchor) [w](https://example.com/x.md)", "[a](#anchor) [w](https://example.com/x.md)"},
		{"`[x](API.md)` and [y](API.md)", "`[x](API.md)` and [y](generated-api.md)"},
		{"```\n[x](API.md)\n```", "```\n[x](API.md)\n```"},
		{"[ref]: API.md", "[ref]: generated-api.md"},
	} {
		got, err := rewriteLinks(tc.in, "docs/USER.md", "guide/user-guide.md", pages, exists)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("rewriteLinks(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
	if _, err := rewriteLinks("[x](MISSING.md)", "docs/USER.md", "guide/user-guide.md", pages, exists); err == nil {
		t.Error("a link to a missing file must be an error")
	}
}

func TestRenderComment(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{" a <vservice> *b* [c]", "a &lt;vservice&gt; \\*b\\* \\[c\\]\n\n"},
		{" keep `a_b <c>` code", "keep `a_b <c>` code\n\n"},
		{" Example: option (spec.x) = {a: 1};", "Example: `option (spec.x) = {a: 1};`\n\n"},
		{" one\n two\n\n three", "one  \ntwo\n\nthree\n\n"},
		{" Example:\n   message M {\n     int32 a = 1;\n   }", "Example:\n\n```protobuf\nmessage M {\n  int32 a = 1;\n}\n```\n\n"},
		{" Rules:\n - first\n   wrapped\n - second", "Rules:\n\n- first  \n    wrapped\n- second\n\n"},
		{" 50 retired", "\\50 retired\n\n"},
	} {
		if got := renderComment(tc.in); got != tc.want {
			t.Errorf("renderComment(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

func TestSummary(t *testing.T) {
	got := summary(" service_state marks a message as a state root. More here.\n Example: x")
	if want := `service\_state marks a message as a state root.`; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}
