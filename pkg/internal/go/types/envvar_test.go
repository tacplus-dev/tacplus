package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

func TestComputeEnvVarPrefix(t *testing.T) {
	tests := []struct {
		relPath  string
		msgName  string
		expected string
	}{
		{"sys/jwk.proto", "Jwk", "PROTO_SYS_JWK_JWK"},
		{"sys/key.proto", "KeyConfig", "PROTO_SYS_KEY_KEY_CONFIG"},
		{"services/foo.proto", "FooConfig", "PROTO_SERVICES_FOO_FOO_CONFIG"},
		{"common/ballsports.proto", "BallConfig", "PROTO_COMMON_BALLSPORTS_BALL_CONFIG"},
	}

	for _, tt := range tests {
		t.Run(tt.relPath+"_"+tt.msgName, func(t *testing.T) {
			got := computeEnvVarPrefix(tt.relPath, tt.msgName)
			if got != tt.expected {
				t.Errorf("computeEnvVarPrefix(%q, %q) = %q, want %q", tt.relPath, tt.msgName, got, tt.expected)
			}
		})
	}
}

func TestGenerateMessageEnvVar(t *testing.T) {
	msg := model.Message{
		Name: "Jwk",
		Fields: []model.Field{
			{Name: "kty", Type: "KeyType", TypePackage: "key", IsEnum: true},
			{Name: "alg", Type: "string"},
			{Name: "kid", Type: "string"},
			{Name: "exp", Type: "int64"},
			{Name: "created", Type: "int64"},
			{Name: "enabled", Type: "bool"},
			{Name: "data", Type: "bytes"},
			{Name: "tags", Type: "string", IsRepeated: true},
		},
	}

	var b strings.Builder
	err := generateMessageEnvVar(&b, msg, "sys/jwk.proto")
	if err != nil {
		t.Fatalf("generateMessageEnvVar failed: %v", err)
	}

	output := b.String()

	// Verify ToEnvVar function is generated
	if !strings.Contains(output, "func JwkToEnvVar(v *Jwk) map[string]string") {
		t.Error("missing JwkToEnvVar function signature")
	}

	// Verify FromEnvVar function is generated
	if !strings.Contains(output, "func JwkFromEnvVar(env map[string]string) (*Jwk, error)") {
		t.Error("missing JwkFromEnvVar function signature")
	}

	// Verify env var key format
	if !strings.Contains(output, `"PROTO_SYS_JWK_JWK_KTY"`) {
		t.Error("missing PROTO_SYS_JWK_JWK_KTY key")
	}
	if !strings.Contains(output, `"PROTO_SYS_JWK_JWK_ALG"`) {
		t.Error("missing PROTO_SYS_JWK_JWK_ALG key")
	}
	if !strings.Contains(output, `"PROTO_SYS_JWK_JWK_EXP"`) {
		t.Error("missing PROTO_SYS_JWK_JWK_EXP key")
	}

	// Verify enum handling uses ToString/Parse
	if !strings.Contains(output, "key.KeyTypeToString") {
		t.Error("missing key.KeyTypeToString for enum ToEnvVar")
	}
	if !strings.Contains(output, "key.ParseKeyType") {
		t.Error("missing key.ParseKeyType for enum FromEnvVar")
	}

	// Verify int64 handling
	if !strings.Contains(output, "strconv.FormatInt") {
		t.Error("missing strconv.FormatInt for int64 ToEnvVar")
	}
	if !strings.Contains(output, "strconv.ParseInt") {
		t.Error("missing strconv.ParseInt for int64 FromEnvVar")
	}

	// Verify bool handling
	if !strings.Contains(output, "strconv.FormatBool") {
		t.Error("missing strconv.FormatBool for bool ToEnvVar")
	}

	// Verify bytes handling
	if !strings.Contains(output, "base64.StdEncoding.EncodeToString") {
		t.Error("missing base64.StdEncoding.EncodeToString for bytes ToEnvVar")
	}

	// Verify repeated string handling
	if !strings.Contains(output, `strings.Join`) {
		t.Error("missing strings.Join for repeated string ToEnvVar")
	}

	t.Log("Generated output:\n", output)
}

func TestGenerateMessageEnvVar_SubMessage(t *testing.T) {
	msg := model.Message{
		Name: "FooConfig",
		Fields: []model.Field{
			{Name: "host", Type: "HostConfig", TypePackage: "host", IsMessage: true},
			{Name: "port", Type: "int32"},
		},
	}

	var b strings.Builder
	err := generateMessageEnvVar(&b, msg, "services/foo.proto")
	if err != nil {
		t.Fatalf("generateMessageEnvVar failed: %v", err)
	}

	output := b.String()

	// Verify sub-message merging uses the sub's ToEnvVar function
	if !strings.Contains(output, "host.HostConfigToEnvVar") {
		t.Error("missing host.HostConfigToEnvVar for sub-message merge")
	}

	// Verify sub-message FromEnvVar
	if !strings.Contains(output, "host.HostConfigFromEnvVar") {
		t.Error("missing host.HostConfigFromEnvVar for sub-message parse")
	}

	// Verify port field uses its own prefix
	if !strings.Contains(output, `"PROTO_SERVICES_FOO_FOO_CONFIG_PORT"`) {
		t.Error("missing PROTO_SERVICES_FOO_FOO_CONFIG_PORT key")
	}

	t.Log("Generated output:\n", output)
}

func TestGenerateMessageEnvVar_SkipsMapAndOneof(t *testing.T) {
	msg := model.Message{
		Name: "TestMsg",
		Fields: []model.Field{
			{Name: "name", Type: "string"},
			{Name: "metadata", Type: "string", IsMap: true, MapKeyType: "string", MapValueType: "string"},
			{Name: "choice", Type: "string", IsOneOf: true, OneOfName: "decision"},
		},
	}

	var b strings.Builder
	err := generateMessageEnvVar(&b, msg, "sys/test.proto")
	if err != nil {
		t.Fatalf("generateMessageEnvVar failed: %v", err)
	}

	output := b.String()

	// Name should be present
	if !strings.Contains(output, `"PROTO_SYS_TEST_TEST_MSG_NAME"`) {
		t.Error("missing PROTO_SYS_TEST_TEST_MSG_NAME key")
	}

	// Map and oneof should be skipped
	if strings.Contains(output, "METADATA") {
		t.Error("map field METADATA should be skipped")
	}
	if strings.Contains(output, "CHOICE") {
		t.Error("oneof field CHOICE should be skipped")
	}
}

func TestGenerateMessageEnvVar_RepeatedMessages_Skipped(t *testing.T) {
	msg := model.Message{
		Name: "TestMsg",
		Fields: []model.Field{
			{Name: "name", Type: "string"},
			{Name: "items", Type: "Item", IsMessage: true, IsRepeated: true},
		},
	}

	var b strings.Builder
	err := generateMessageEnvVar(&b, msg, "sys/test.proto")
	if err != nil {
		t.Fatalf("generateMessageEnvVar failed: %v", err)
	}

	output := b.String()

	// Name should be present
	if !strings.Contains(output, `"PROTO_SYS_TEST_TEST_MSG_NAME"`) {
		t.Error("missing PROTO_SYS_TEST_TEST_MSG_NAME key")
	}

	// Repeated message should be skipped
	if strings.Contains(output, "ITEMS") {
		t.Error("repeated message field ITEMS should be skipped")
	}
}

func TestGenerateMessageEnvVar_Optional(t *testing.T) {
	msg := model.Message{
		Name: "TestMsg",
		Fields: []model.Field{
			{Name: "name", Type: "string", IsOptional: true},
			{Name: "count", Type: "int32", IsOptional: true},
		},
	}

	var b strings.Builder
	err := generateMessageEnvVar(&b, msg, "sys/test.proto")
	if err != nil {
		t.Fatalf("generateMessageEnvVar failed: %v", err)
	}

	output := b.String()

	// Verify nil check for optional fields in ToEnvVar
	if !strings.Contains(output, "v.Name != nil") {
		t.Error("missing nil check for optional string in ToEnvVar")
	}

	// Verify pointer dereference in FromEnvVar
	if !strings.Contains(output, "result.Name = &val") {
		t.Error("missing pointer assignment for optional string in FromEnvVar")
	}

	t.Log("Generated output:\n", output)
}

func TestGenerateGoHelpersIncludesErrorsEnvVarHelpers(t *testing.T) {
	out := t.TempDir()
	pf := model.ProtoFile{
		Package: "errors",
		RelPath: "sys/errors.proto",
		Messages: []model.Message{{
			Name: "Error",
			Fields: []model.Field{
				{Name: "code", Type: "int32"},
				{Name: "message", Type: "string"},
				{Name: "details", Type: "Any", TypePackage: "anypb", IsMessage: true, IsRepeated: true},
			},
		}},
	}
	cfg := &tactype.Config{
		ModuleBasePath: "example.com/current/tacplus",
	}

	if err := generateGoHelpers(pf, out, "sys/errors", cfg, nil); err != nil {
		t.Fatalf("generateGoHelpers: %v", err)
	}

	renderedBytes, err := os.ReadFile(filepath.Join(out, "golang", "sys", "errors", "helpers.go"))
	if err != nil {
		t.Fatalf("read helpers.go: %v", err)
	}
	rendered := string(renderedBytes)
	for _, want := range []string{
		"func ErrorToEnvVar(v *Error) map[string]string",
		"func ErrorFromEnvVar(env map[string]string) (*Error, error)",
		`"PROTO_SYS_ERRORS_ERROR_CODE"`,
		`"PROTO_SYS_ERRORS_ERROR_MESSAGE"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("errors helpers missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "ErrorToYAML") {
		t.Fatalf("errors helpers should not emit generic Error serialization helpers:\n%s", rendered)
	}
}
