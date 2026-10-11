// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

const publicViewSourceProto = `syntax = "proto3";

package prospectdemo;

import "sys/spec.proto";

message Prospect {
  option (spec.public_view) = true;
  string company_name = 1 [(spec.visibility) = VISIBILITY_SHOW];
  string deal_notes = 2 [(spec.visibility) = VISIBILITY_BACKGROUND];
}
`

const plainSourceProto = `syntax = "proto3";

package plainwidget;

message Widget {
  string id = 1;
}
`

// The injector appends <T>Public to the annotated file only; the trace injector
// that runs after it gives T its trace fields but leaves <T>Public holding the
// SHOW fields and nothing else — a trace_id there would be copied from T by
// <T>ToPublic and handed to the renderer.
func TestAppendPublicViewMessagesThenTrace(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := t.TempDir()
	copySpecProtoForConfigSliceTest(t, dir)
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("prospect.proto", publicViewSourceProto)
	write("plain.proto", plainSourceProto)

	if err := AppendPublicViewMessagesToProtos(dir); err != nil {
		t.Fatalf("AppendPublicViewMessagesToProtos: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "plain.proto")); string(got) != plainSourceProto {
		t.Fatalf("a file with no public_view message must be untouched, got:\n%s", got)
	}
	skipped, err := InjectTraceFields(dir, dir)
	if err != nil {
		t.Fatalf("InjectTraceFields: %v", err)
	}
	if !skipped["prospectdemo.ProspectPublic"] {
		t.Errorf("ProspectPublic must be recorded as carrying no injected trace fields, skipped=%v", skipped)
	}

	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("injected protos must compile: %v", err)
	}
	fd, err := dp.GetFileDescriptor("prospect.proto")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string][]string{}
	for _, m := range fd.Messages {
		for _, f := range m.Fields {
			fields[m.Name] = append(fields[m.Name], f.Name)
		}
	}
	if got := strings.Join(fields["ProspectPublic"], ","); got != "company_name" {
		t.Errorf("ProspectPublic fields = %q, want only the SHOW field company_name", got)
	}
	if got := strings.Join(fields["Prospect"], ","); !strings.Contains(got, "trace_id") {
		t.Errorf("Prospect itself keeps its trace fields, got %q", got)
	}
}
