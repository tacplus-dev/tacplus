// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// defaultHostProtoFile is a proto file whose message carries a singular
// host.Host field annotated with default_host — the shape that both
// defaults.py.tmpl and undefaults.py.tmpl special-case.
func defaultHostProtoFile() model.ProtoFile {
	return model.ProtoFile{
		Package: "cfg",
		RelPath: "cfg/cfg.proto",
		Messages: []model.Message{
			{
				Name: "Cfg",
				Fields: []model.Field{
					{
						Name: "listen_address", Type: "Host", TypePackage: "host",
						Number: 1, IsMessage: true,
						Annotations: map[string]model.AnnotationValue{
							"assert_required": {Type: model.AnnotationTypeBool, BoolValue: true},
							"default_host":    {Type: model.AnnotationTypeHost, HostValue: &model.HostValue{Host: "0.0.0.0", Port: 8443}},
						},
					},
				},
			},
		},
	}
}

// TestPythonDefaultHostDoesNotAssignCompositeField pins the default_host
// branches of the Python defaults/undefaults helpers. protobuf-python forbids
// assignment to a singular message field — both `msg.f = None` and
// `msg.f = Host(...)` raise AttributeError("Assignment not allowed to composite
// field") — so presence must be read with HasField(), cleared with ClearField()
// and populated by mutating the submessage in place.
func TestPythonDefaultHostDoesNotAssignCompositeField(t *testing.T) {
	pf := defaultHostProtoFile()

	for _, tc := range []struct {
		name    string
		write   func(*strings.Builder, model.ProtoFile) error
		want    []string
		notWant []string
	}{
		{
			name:  "defaults",
			write: writeDefaults,
			want: []string{
				`if not msg.HasField("listen_address"):`,
				`msg.listen_address.host = "0.0.0.0"`,
				`msg.listen_address.port = 8443`,
			},
			notWant: []string{
				"msg.listen_address = ",
				"Host(host=",
			},
		},
		{
			name:  "undefaults",
			write: writeUndefaults,
			want: []string{
				`if msg.HasField("listen_address") and msg.listen_address.host == "0.0.0.0" and msg.listen_address.port == 8443:`,
				`msg.ClearField("listen_address")`,
			},
			notWant: []string{
				"msg.listen_address = ",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			if err := tc.write(&b, pf); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			src := b.String()
			for _, want := range tc.want {
				if !strings.Contains(src, want) {
					t.Errorf("%s missing %q:\n%s", tc.name, want, src)
				}
			}
			for _, bad := range tc.notWant {
				if strings.Contains(src, bad) {
					t.Errorf("%s assigns to composite field (%q):\n%s", tc.name, bad, src)
				}
			}
			assertPythonCompiles(t, tc.name, src)
		})
	}
}
