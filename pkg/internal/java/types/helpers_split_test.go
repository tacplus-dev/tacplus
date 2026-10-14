// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

const helpersSplitTestPkg = "com.example.types.services.big"

// generateHelpersForTest runs generateHelpers for pf and returns the generated
// files under the package directory, keyed by file name.
func generateHelpersForTest(t *testing.T, pf model.ProtoFile) map[string]string {
	t.Helper()
	dir := t.TempDir()
	subdir := filepath.Join(strings.Split(helpersSplitTestPkg, ".")...)
	cfg := &tactype.Config{JavaPackageBase: "com.example"}
	if err := generateHelpers(pf, dir, subdir, "Big", cfg, nil, nil); err != nil {
		t.Fatalf("generateHelpers: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, subdir))
	if err != nil {
		t.Fatalf("read output dir: %v", err)
	}
	files := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, subdir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		files[e.Name()] = string(b)
	}
	return files
}

func bigHelpersProto(messages int) model.ProtoFile {
	pf := model.ProtoFile{Package: "big", RelPath: "services/big.proto"}
	for i := 0; i < messages; i++ {
		pf.Messages = append(pf.Messages, model.Message{
			Name: fmt.Sprintf("Record%d", i),
			Fields: []model.Field{
				{Name: "id", Type: "string", Number: 1},
				{Name: "count", Type: "int64", Number: 2},
				{Name: "labels", Type: "string", IsRepeated: true, Number: 3},
				{Name: "enabled", Type: "bool", Number: 4},
			},
		})
	}
	return pf
}

var publicStaticMethod = regexp.MustCompile(`(?m)^\s*public static [^=;(]*\b(\w+)\(`)

// A package with hundreds of messages (vservices/query: 481 messages, 7,227
// methods) overflowed javac's 65,535-entry constant pool in one Helpers class.
// Over budget the methods move to a HelpersPart chain that Helpers extends, so
// every class stays bounded and Helpers.x() still resolves for every caller.
func TestJavaHelpersSplitIntoBoundedParts(t *testing.T) {
	const messages = 200
	files := generateHelpersForTest(t, bigHelpersProto(messages))

	helpers := files["Helpers.java"]
	parts := len(files) - 1
	if parts < 2 {
		t.Fatalf("expected Helpers to be split, got files %v", len(files))
	}
	if !strings.Contains(helpers, fmt.Sprintf("public final class Helpers extends HelpersPart%d {", parts)) {
		t.Fatalf("Helpers must extend the last part HelpersPart%d:\n%s", parts, helpers)
	}
	if publicStaticMethod.MatchString(helpers) {
		t.Fatalf("Helpers must declare no methods once split:\n%s", helpers)
	}

	defined := map[string]int{}
	for i := 1; i <= parts; i++ {
		name := fmt.Sprintf("HelpersPart%d.java", i)
		src, ok := files[name]
		if !ok {
			t.Fatalf("missing %s; got %d files", name, len(files))
		}
		if len(src) > helpersClassBudget+4096 {
			t.Errorf("%s is %d bytes, over the %d-byte class budget", name, len(src), helpersClassBudget)
		}
		decl := fmt.Sprintf("public class HelpersPart%d {", i)
		if i > 1 {
			decl = fmt.Sprintf("public class HelpersPart%d extends HelpersPart%d {", i, i-1)
		}
		if !strings.Contains(src, decl) {
			t.Errorf("%s: missing %q", name, decl)
		}
		// Methods call each other unqualified; the static import resolves a call
		// to a method that landed in a later part of the chain.
		if !strings.Contains(src, "import static "+helpersSplitTestPkg+".Helpers.*;") {
			t.Errorf("%s: missing static import of Helpers", name)
		}
		for _, m := range publicStaticMethod.FindAllStringSubmatch(src, -1) {
			defined[m[1]]++
		}
	}

	for i := 0; i < messages; i++ {
		for _, method := range []string{"record%dToJson", "record%dFromJson", "validateRecord%d", "mergeRecord%d"} {
			name := fmt.Sprintf(method, i)
			if defined[name] != 1 {
				t.Errorf("%s defined %d times across parts, want 1", name, defined[name])
			}
		}
	}
}

func TestJavaHelpersUnderBudgetStaySingleClass(t *testing.T) {
	files := generateHelpersForTest(t, bigHelpersProto(1))
	if len(files) != 1 {
		t.Fatalf("expected only Helpers.java, got %d files", len(files))
	}
	helpers := files["Helpers.java"]
	if !strings.Contains(helpers, "public final class Helpers {") {
		t.Fatalf("small Helpers must not extend a part:\n%s", helpers)
	}
	if !strings.Contains(helpers, "public static String record0ToJson(") {
		t.Fatalf("small Helpers must keep its methods")
	}
}

func TestJavaClassMembersIgnoresBracesInLiteralsAndComments(t *testing.T) {
	src := `// ============ Section ============
/** Javadoc with a brace { */
public static final String OPEN = "{";
public static final char CLOSE = '}';
public static final java.util.logging.Logger LOG =
        java.util.logging.Logger.getLogger("x");
@Deprecated
public static boolean sniff(String s) {
// a stray } in a comment
return s.startsWith("{\"") && s.endsWith("}") /* } */;
}
public static final int[] TABLE = {
1, 2,
};
`
	got := javaClassMembers(src)
	want := []string{
		"// ============ Section ============\n/** Javadoc with a brace { */\npublic static final String OPEN = \"{\";\n",
		"public static final char CLOSE = '}';\n",
		"public static final java.util.logging.Logger LOG =\n        java.util.logging.Logger.getLogger(\"x\");\n",
		"@Deprecated\npublic static boolean sniff(String s) {\n// a stray } in a comment\nreturn s.startsWith(\"{\\\"\") && s.endsWith(\"}\") /* } */;\n}\n",
		"public static final int[] TABLE = {\n1, 2,\n};\n",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d members, want %d:\n%q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("member %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
	if strings.Join(got, "") != src {
		t.Fatalf("members must concatenate back to the source")
	}
}

func TestPackHelpersPartsRespectsBudget(t *testing.T) {
	members := []string{"aaaa", "bbbb", "cc", "dddddddddd", "e"}
	got := packHelpersParts(members, 8)
	want := []string{"aaaabbbb", "cc", "dddddddddd", "e"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("packHelpersParts = %q, want %q", got, want)
	}
}

// protobuf-java renames accessors whose PascalCase stem would collide with an
// inherited method: a field `class` is getClass_()/setClass_(), never getClass()
// (Object's) — verified against protoc 36.2 output.
func TestJavaFieldNameUpperHonorsProtobufReservedNames(t *testing.T) {
	cases := map[string]string{
		"class":                       "Class_",
		"default_instance_for_type":   "DefaultInstanceForType_",
		"parser_for_type":             "ParserForType_",
		"serialized_size":             "SerializedSize_",
		"all_fields":                  "AllFields_",
		"descriptor_for_type":         "DescriptorForType_",
		"initialization_error_string": "InitializationErrorString_",
		"unknown_fields":              "UnknownFields_",
		"cached_size":                 "CachedSize_",
		// only the whole stem is reserved:
		"class_list":               "ClassList",
		"memoized_serialized_size": "MemoizedSerializedSize",
		"hash_code":                "HashCode",
	}
	for in, want := range cases {
		if got := javaFieldNameUpper(in); got != want {
			t.Errorf("javaFieldNameUpper(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJavaHelpersUseMangledAccessorsForFieldNamedClass(t *testing.T) {
	pf := model.ProtoFile{Package: "big", RelPath: "services/big.proto", Messages: []model.Message{{
		Name: "Tagged",
		Fields: []model.Field{
			{Name: "class", Type: "string", Number: 1, Annotations: map[string]model.AnnotationValue{
				"assert_required": {Type: model.AnnotationTypeBool, BoolValue: true},
			}},
			{Name: "unknown_fields", Type: "string", IsRepeated: true, Number: 2},
		},
	}}}
	helpers := generateHelpersForTest(t, pf)["Helpers.java"]

	for _, want := range []string{"getClass_()", "setClass_(", "getUnknownFields_List()", "addAllUnknownFields_("} {
		if !strings.Contains(helpers, want) {
			t.Errorf("Helpers missing mangled accessor %q", want)
		}
	}
	for _, bad := range regexp.MustCompile(`\.(get|set|has|clear)(Class|UnknownFields)(List|Count)?\(`).FindAllString(helpers, -1) {
		t.Errorf("Helpers calls unmangled accessor %q", bad)
	}
}
