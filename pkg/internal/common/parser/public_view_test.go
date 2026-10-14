// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"bytes"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// prospectProto is the sample public_view record: SHOW and BACKGROUND fields of
// every shape <T>Public carries (scalar, optional, enum, map, singular and
// repeated public_view message), plus BACKGROUND fields of shapes a SHOW field
// may not take (an unannotated message, a repeated scalar).
const prospectProto = `syntax = "proto3";

package prospectdemo;

import "sys/spec.proto";

enum Stage {
  STAGE_UNSPECIFIED = 0;
  STAGE_WARM = 1;
}

message Contact {
  option (spec.public_view) = true;
  string display_name = 1 [(spec.visibility) = VISIBILITY_SHOW];
  string private_phone = 2 [(spec.visibility) = VISIBILITY_BACKGROUND];
}

message InternalScore {
  int32 value = 1;
  string rationale = 2;
}

message Prospect {
  option (spec.public_view) = true;
  string company_name = 1 [(spec.visibility) = VISIBILITY_SHOW];
  string deal_notes = 2 [(spec.visibility) = VISIBILITY_BACKGROUND];
  int64 budget_ceiling_cents = 3 [(spec.visibility) = VISIBILITY_BACKGROUND];
  Contact champion = 4 [(spec.visibility) = VISIBILITY_SHOW];
  repeated Contact team = 5 [(spec.visibility) = VISIBILITY_SHOW, (spec.assert_max_size) = 20];
  map<string, string> highlights = 6 [(spec.visibility) = VISIBILITY_SHOW, (spec.assert_max_size) = 20];
  Stage stage = 7 [(spec.visibility) = VISIBILITY_SHOW];
  optional string tagline = 8 [(spec.visibility) = VISIBILITY_SHOW];
  InternalScore score = 9 [(spec.visibility) = VISIBILITY_BACKGROUND];
  repeated string competitor_names = 10 [(spec.visibility) = VISIBILITY_BACKGROUND, (spec.assert_max_size) = 20];
  map<string, Contact> contacts_by_role = 11 [(spec.visibility) = VISIBILITY_SHOW, (spec.assert_max_size) = 20];
}

message GetProspectRequest { string company_name = 1; }

service ProspectDesk {
  option (spec.service_c2s) = {auth_entitled_users: true};
  // GetProspect returns one prospect record, background fields included.
  rpc GetProspect(GetProspectRequest) returns (Prospect);
}
`

// prospectBackground names every BACKGROUND field (and the unannotated type only
// a BACKGROUND field reaches). None may appear in anything renderer-facing.
var prospectBackground = []string{"deal_notes", "budget_ceiling_cents", "private_phone", "score", "InternalScore", "rationale", "competitor_names"}

// The sample is valid, including an authenticated C2S read returning Prospect:
// BACKGROUND means "never leaves the company", and an entitled caller is the
// company. Positive control for every refusal below.
func TestValidatePublicViewAcceptsProspect(t *testing.T) {
	dp := writeSurfaceProtoDir(t, prospectProto)
	if err := ValidatePublicViewAnnotations(dp); err != nil {
		t.Fatalf("well-formed public_view record must generate: %v", err)
	}
}

// A proto with no public_view message yields no <T>Public at all, so its
// generated output is unchanged.
func TestPublicViewMessagesEmptyWithoutAnnotation(t *testing.T) {
	dp := writeSurfaceProtoDir(t, `syntax = "proto3";
package plain;
message Thing { string a = 1; }
`)
	if got := dp.PublicViewMessages(); len(got) != 0 {
		t.Fatalf("no public_view message must emit nothing, got %v", got)
	}
}

func TestPublicViewMessagesShape(t *testing.T) {
	dp := writeSurfaceProtoDir(t, prospectProto)
	got := dp.PublicViewMessages()["surface.proto"]
	for _, want := range []string{
		"message ContactPublic {\n  string display_name = 1;\n}",
		"message ProspectPublic {",
		"  string company_name = 1;\n",
		"  .prospectdemo.ContactPublic champion = 4;\n",
		"  repeated .prospectdemo.ContactPublic team = 5;\n",
		"  map<string, string> highlights = 6;\n",
		"  .prospectdemo.Stage stage = 7;\n",
		"  optional string tagline = 8;\n",
		"  map<string, .prospectdemo.ContactPublic> contacts_by_role = 11;\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// The leak test: no BACKGROUND field name appears anywhere in the generated
// public views, and the one ToPublic recipe every language uses (serialize T,
// parse as <T>Public discarding unknown fields) carries none of their values —
// not as fields and not as retained unknown bytes.
func TestPublicViewLeaksNoBackgroundField(t *testing.T) {
	dp := writeSurfaceProtoDir(t, prospectProto)
	views := dp.PublicViewMessages()["surface.proto"]
	for _, bg := range prospectBackground {
		if strings.Contains(views, bg) {
			t.Errorf("BACKGROUND name %q leaked into generated public views:\n%s", bg, views)
		}
	}

	// Compile the generated <T>Public alongside T, as every generator does.
	dir := t.TempDir()
	copySpecProto(t, dir)
	writeTempProtoFile(t, dir, "prospect.proto", prospectProto+views)
	full, err := NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("generated public views must compile: %v", err)
	}
	find := func(name string) protoreflect.MessageDescriptor {
		d, err := full.files.FindDescriptorByName(protoreflect.FullName("prospectdemo." + name))
		if err != nil {
			t.Fatalf("find %s: %v", name, err)
		}
		return d.(protoreflect.MessageDescriptor)
	}
	contactD, prospectD, scoreD := find("Contact"), find("Prospect"), find("InternalScore")

	contact := func(name, phone string) protoreflect.Message {
		c := dynamicpb.NewMessage(contactD)
		c.Set(contactD.Fields().ByName("display_name"), protoreflect.ValueOfString(name))
		c.Set(contactD.Fields().ByName("private_phone"), protoreflect.ValueOfString(phone))
		return c
	}
	p := dynamicpb.NewMessage(prospectD)
	f := prospectD.Fields().ByName
	p.Set(f("company_name"), protoreflect.ValueOfString("Jimerson"))
	p.Set(f("deal_notes"), protoreflect.ValueOfString("SECRET-NOTES"))
	p.Set(f("budget_ceiling_cents"), protoreflect.ValueOfInt64(987654321))
	p.Set(f("champion"), protoreflect.ValueOfMessage(contact("Dennis", "SECRET-PHONE-1")))
	team := p.Mutable(f("team")).List()
	team.Append(protoreflect.ValueOfMessage(contact("Ann", "SECRET-PHONE-2")))
	p.Mutable(f("highlights")).Map().Set(protoreflect.ValueOfString("k").MapKey(), protoreflect.ValueOfString("shown"))
	p.Set(f("stage"), protoreflect.ValueOfEnum(1))
	p.Set(f("tagline"), protoreflect.ValueOfString("tag"))
	score := dynamicpb.NewMessage(scoreD)
	score.Set(scoreD.Fields().ByName("rationale"), protoreflect.ValueOfString("SECRET-RATIONALE"))
	p.Set(f("score"), protoreflect.ValueOfMessage(score))
	p.Mutable(f("competitor_names")).List().Append(protoreflect.ValueOfString("SECRET-COMPETITOR"))
	p.Mutable(f("contacts_by_role")).Map().Set(protoreflect.ValueOfString("buyer").MapKey(), protoreflect.ValueOfMessage(contact("Bo", "SECRET-PHONE-3")))

	raw, err := proto.Marshal(p.Interface())
	if err != nil {
		t.Fatal(err)
	}
	pub := dynamicpb.NewMessage(find("ProspectPublic"))
	if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, pub.Interface()); err != nil {
		t.Fatal(err)
	}
	out, err := proto.Marshal(pub.Interface())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SECRET-NOTES", "SECRET-PHONE-1", "SECRET-PHONE-2", "SECRET-PHONE-3", "SECRET-RATIONALE", "SECRET-COMPETITOR"} {
		if bytes.Contains(out, []byte(secret)) {
			t.Errorf("BACKGROUND value %q survived the public view", secret)
		}
	}
	for _, shown := range []string{"Jimerson", "Dennis", "Ann", "shown", "tag", "Bo"} {
		if !bytes.Contains(out, []byte(shown)) {
			t.Errorf("SHOW value %q missing from the public view", shown)
		}
	}
}

// Negative cases: each is the sample with one thing broken.
func TestValidatePublicViewRefusals(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"unannotated field defaults to nothing",
			`string deal_notes = 2 [(spec.visibility) = VISIBILITY_BACKGROUND];`,
			`string deal_notes = 2;`,
			"prospectdemo.Prospect.deal_notes: every field of a (spec.public_view) message must declare (spec.visibility)"},
		{"unspecified visibility",
			`string deal_notes = 2 [(spec.visibility) = VISIBILITY_BACKGROUND];`,
			`string deal_notes = 2 [(spec.visibility) = VISIBILITY_UNSPECIFIED];`,
			"prospectdemo.Prospect.deal_notes: every field of a (spec.public_view) message must declare (spec.visibility)"},
		{"SHOW over an unannotated message",
			`InternalScore score = 9 [(spec.visibility) = VISIBILITY_BACKGROUND];`,
			`InternalScore score = 9 [(spec.visibility) = VISIBILITY_SHOW];`,
			"prospectdemo.Prospect.score: VISIBILITY_SHOW field of type prospectdemo.InternalScore would show every field of it"},
		{"visibility on a message that is not public_view",
			`  int32 value = 1;`,
			`  int32 value = 1 [(spec.visibility) = VISIBILITY_SHOW];`,
			"prospectdemo.InternalScore.value: (spec.visibility) is only valid on a field of a top-level (spec.public_view) message"},
		{"public_view on a nested declaration",
			`  string rationale = 2;
}`,
			`  string rationale = 2;
  message Inner {
    option (spec.public_view) = true;
    string x = 1 [(spec.visibility) = VISIBILITY_SHOW];
  }
}`,
			"prospectdemo.InternalScore.Inner: (spec.public_view) is declared on a NESTED message declaration"},
		{"SHOW oneof member",
			`optional string tagline = 8 [(spec.visibility) = VISIBILITY_SHOW];`,
			`oneof motto { string tagline = 8 [(spec.visibility) = VISIBILITY_SHOW]; }`,
			"prospectdemo.Prospect.tagline: a VISIBILITY_SHOW field cannot be a oneof member"},
		{"declared <T>Public collides",
			`message GetProspectRequest { string company_name = 1; }`,
			`message GetProspectRequest { string company_name = 1; }
message ProspectPublic { string company_name = 1; }`,
			"prospectdemo.Prospect: prospectdemo.ProspectPublic is generated from (spec.public_view) and must not be declared"},
		{"tokenless RPC returns the record",
			`rpc GetProspect(GetProspectRequest) returns (Prospect);`,
			`rpc GetProspect(GetProspectRequest) returns (Prospect) { option (spec.rpc) = {flags: [RPC_FLAG_PUBLIC_NO_AUTH]}; }`,
			"prospectdemo.ProspectDesk.GetProspect: callable without a caller token but carries (spec.public_view) message prospectdemo.Prospect"},
		{"tokenless RPC reaches the record through a wrapper",
			`message GetProspectRequest { string company_name = 1; }`,
			`message GetProspectRequest { string company_name = 1; }
message Page { repeated Prospect rows = 1; }`,
			"prospectdemo.ProspectDesk.GetProspect: callable without a caller token but carries (spec.public_view) message prospectdemo.Prospect"},
		{"C2D RPC returns the record",
			`option (spec.service_c2s) = {auth_entitled_users: true};`,
			`option (spec.service_c2d) = {socket_path: "/run/prospect.sock"};`,
			"prospectdemo.ProspectDesk.GetProspect: callable without a caller token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(prospectProto, c.from) {
				t.Fatalf("fixture drift: %q not in prospectProto", c.from)
			}
			src := strings.Replace(prospectProto, c.from, c.to, 1)
			if c.name == "tokenless RPC reaches the record through a wrapper" {
				src = strings.Replace(src, `rpc GetProspect(GetProspectRequest) returns (Prospect);`,
					`rpc GetProspect(GetProspectRequest) returns (Page) { option (spec.rpc) = {flags: [RPC_FLAG_PUBLIC_NO_AUTH]}; }`, 1)
			}
			dp := writeSurfaceProtoDir(t, src)
			err := ValidatePublicViewAnnotations(dp)
			if err == nil {
				t.Fatal("must fail generation")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("refusal %q does not contain %q", err.Error(), c.want)
			}
		})
	}
}

// The injected proto must not change a file with no public_view message: the
// generators append nothing, byte for byte.
func TestPublicViewMessagesOnlyForAnnotatedFiles(t *testing.T) {
	dp := writeSurfaceProtoDir(t, prospectProto, `syntax = "proto3";
package other;
message Untouched { string a = 1; }
`)
	views := dp.PublicViewMessages()
	if _, ok := views["surface2.proto"]; ok {
		t.Fatalf("file without public_view got a public view: %v", views)
	}
	if _, ok := views["surface.proto"]; !ok {
		t.Fatalf("annotated file got no public view: %v", views)
	}
}
