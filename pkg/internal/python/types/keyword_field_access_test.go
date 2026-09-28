// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// keywordFieldProtoFile is a proto file whose messages carry fields named after
// Python keywords. protobuf-python keeps the raw proto name on the message
// object, so generated field access must go through getattr/setattr — dot access
// to "from" is a SyntaxError, and "from_" is an attribute that does not exist.
func keywordFieldProtoFile() model.ProtoFile {
	strDefault := map[string]model.AnnotationValue{
		"default_string": {Type: model.AnnotationTypeString, StringValue: "x"},
	}
	return model.ProtoFile{
		Package: "kw",
		RelPath: "kw/kw.proto",
		Enums: []model.Enum{
			{
				Name: "KwParamKey",
				Values: []model.EnumValue{
					{Name: "KW_PARAM_KEY_FROM", Number: 1, JsonValue: "from"},
					{Name: "KW_PARAM_KEY_NORMAL_FIELD", Number: 2, JsonValue: "normal_field"},
				},
			},
		},
		Messages: []model.Message{
			{
				Name:           "Foo",
				ParamMapConfig: &model.ParamMapConfig{KeyEnum: "KwParamKey"},
				Fields: []model.Field{
					{Name: "from", Type: "string", Number: 1, Annotations: strDefault, ParamKey: "KW_PARAM_KEY_FROM"},
					{Name: "normal_field", Type: "string", Number: 2, Annotations: strDefault, ParamKey: "KW_PARAM_KEY_NORMAL_FIELD"},
					{Name: "class", Type: "int64", Number: 3, Annotations: map[string]model.AnnotationValue{
						"default_int": {Type: model.AnnotationTypeInt, IntValue: 7},
					}},
					{Name: "import", Type: "string", Number: 4, IsRepeated: true},
					{Name: "global", Type: "Bar", Number: 5, IsMessage: true},
				},
			},
			{
				Name: "Bar",
				Fields: []model.Field{
					{Name: "lambda", Type: "string", Number: 1},
				},
			},
		},
	}
}

// forbiddenKeywordAccess are the dot-access spellings that either fail to parse
// (bare keyword) or resolve to a nonexistent attribute (trailing underscore).
var forbiddenKeywordAccess = []string{
	"msg.from", "msg.from_", "msg.class", "msg.class_",
	"msg.import", "msg.import_", "msg.global", "msg.global_",
	"dst.from", "dst.from_", "src.from", "src.from_",
	"dst.class", "dst.class_", "src.class", "src.class_",
	"dst.import", "dst.import_", "src.import", "src.import_",
	"dst.global", "dst.global_", "src.global", "src.global_",
	"dst.lambda", "dst.lambda_", "src.lambda", "src.lambda_",
	"result.from", "result.from_", "m.from", "m.from_",
	"m.class", "m.class_", "m.import", "m.import_",
	"state.from", "state.from_",
}

func assertNoKeywordDotAccess(t *testing.T, label, src string) {
	t.Helper()
	for _, bad := range forbiddenKeywordAccess {
		if strings.Contains(src, bad) {
			t.Errorf("%s contains keyword dot-access %q:\n%s", label, bad, src)
		}
	}
}

// assertPythonCompiles shells out to the interpreter used for generation and
// compiles the emitted source, so a SyntaxError fails the test.
func assertPythonCompiles(t *testing.T, label, src string) {
	t.Helper()
	py := pythonGrpcToolsInterpreter()
	if _, err := exec.LookPath(py); err != nil {
		t.Skipf("python interpreter %q not available: %v", py, err)
	}
	path := filepath.Join(t.TempDir(), "generated.py")
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatalf("write %s: %v", label, err)
	}
	out, err := exec.Command(py, "-m", "py_compile", path).CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed to compile: %v\n%s\nsource:\n%s", label, err, out, src)
	}
}

func TestPythonHelpersUseGetattrForKeywordFields(t *testing.T) {
	pf := keywordFieldProtoFile()

	for _, tc := range []struct {
		name  string
		write func(*strings.Builder, model.ProtoFile) error
		want  []string
	}{
		{"defaults", writeDefaults, []string{`getattr(msg, 'from')`, `setattr(msg, 'from', `, "msg.normal_field"}},
		{"undefaults", writeUndefaults, []string{`getattr(msg, 'from')`, `setattr(msg, 'from', `, "msg.normal_field"}},
		{"merge", writeMerge, []string{`getattr(dst, 'from')`, `setattr(dst, 'from', getattr(src, 'from'))`, "dst.normal_field"}},
		{"from_map", writeFromMap, []string{`setattr(result, 'from', value)`, "result.normal_field"}},
		{"validation", writeValidation, []string{`len(getattr(msg, 'from')`, "msg.normal_field"}},
		{"envvar", func(b *strings.Builder, pf model.ProtoFile) error { writeEnvVar(b, pf); return nil },
			[]string{`getattr(msg, 'from')`, `setattr(result, 'from', _val)`, "msg.normal_field"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			if err := tc.write(&b, pf); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			src := b.String()
			if src == "" {
				t.Fatalf("%s produced no output", tc.name)
			}
			for _, want := range tc.want {
				if !strings.Contains(src, want) {
					t.Errorf("%s missing %q:\n%s", tc.name, want, src)
				}
			}
			assertNoKeywordDotAccess(t, tc.name, src)
			assertPythonCompiles(t, tc.name, src)
		})
	}
}

func TestPythonStateWrapperUsesGetattrForKeywordBoundaries(t *testing.T) {
	rendered := renderPyStateWrapperForTest(t, pyStateWrapperData{
		PackageName:          "kw",
		PbImportFrom:         "....types.kw",
		TraceImportFrom:      "....types.sys.trace",
		ExceptionsImportFrom: "....exceptions",
		StateRootName:        "KwState",
		Singletons: []pySingletonBoundary{
			{FieldName: "from_", ProtoFieldName: "from", FieldPascal: "From", TypeName: "Foo"},
			{FieldName: "normal_field", ProtoFieldName: "normal_field", FieldPascal: "NormalField", TypeName: "Bar"},
		},
	})

	for _, want := range []string{
		`getattr(state, 'from').CopyFrom(self._from_)`,
		"state.normal_field.CopyFrom(self._normal_field)",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("state wrapper missing %q:\n%s", want, rendered)
		}
	}
	assertNoKeywordDotAccess(t, "statewrapper", rendered)
	assertPythonCompiles(t, "statewrapper", rendered)
}
