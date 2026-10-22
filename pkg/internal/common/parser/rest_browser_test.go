// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"
)

// (spec.rest_browser) makes a REST route a browser form target. Its form body
// and redirect field are resolved against the rpc's request and response at
// parse time, so a declaration the route could not honour is a refused
// generation rather than a browser stranded on a 502.

func restBrowserParser(t *testing.T, protoBody string) (*DescriptorParser, error) {
	t.Helper()
	dir := t.TempDir()
	copySpecProto(t, dir)
	content := `syntax = "proto3";

package rbtest;

import "sys/spec.proto";

message CallbackRequest {
  string code = 1;
  optional string state = 2;
}

message CallbackResponse {
  string redirect_url = 1;
  repeated string urls = 2;
  int64 status = 3;
}
` + protoBody
	writeTempProtoFile(t, dir, "rbtest.proto", content)
	return NewDescriptorParser(dir)
}

// rbService wraps one rpc carrying the given rest_browser option in a service
// of the given kind ("service_c2s" gets REST routes, "service_s2s" does not).
func rbService(kind, rpc, option string) string {
	return `
service Signin {
  option (spec.` + kind + `) = {};
  ` + rpc + ` {
    option (spec.rest_browser) = ` + option + `;
  }
}
`
}

const rbCallback = "rpc BrokerCallback(CallbackRequest) returns (CallbackResponse)"

func TestRestBrowserRefusals(t *testing.T) {
	skipIfNoProtoc(t)

	cases := []struct {
		name string
		body string
		want string
	}{{
		name: "redirect field not in the response",
		body: rbService("service_c2s", rbCallback, `{redirect_field: "next"}`),
		want: `no top-level field of that name`,
	}, {
		name: "redirect field repeated",
		body: rbService("service_c2s", rbCallback, `{redirect_field: "urls"}`),
		want: "is not a singular string",
	}, {
		name: "redirect field not a string",
		body: rbService("service_c2s", rbCallback, `{redirect_field: "status"}`),
		want: "is not a singular string",
	}, {
		name: "redirect field named by JSON name",
		body: rbService("service_c2s", rbCallback, `{redirect_field: "redirectUrl"}`),
		want: `no top-level field of that name`,
	}, {
		name: "streaming rpc",
		body: rbService("service_c2s", "rpc WatchCallback(CallbackRequest) returns (stream CallbackResponse)", `{form_body: true}`),
		want: "on a streaming rpc",
	}, {
		name: "no REST route",
		body: rbService("service_s2s", rbCallback, `{form_body: true, redirect_field: "redirect_url"}`),
		want: "it has no REST route",
	}, {
		name: "declares nothing",
		body: rbService("service_c2s", rbCallback, `{}`),
		want: "neither form_body nor redirect_field",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := restBrowserParser(t, tc.body)
			if err == nil {
				t.Fatalf("%s: parse succeeded; the route could not honour the annotation", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: refusal does not say %q: %v", tc.name, tc.want, err)
			}
			if !strings.Contains(err.Error(), "rbtest.Signin.") {
				t.Fatalf("%s: refusal does not name the rpc: %v", tc.name, err)
			}
		})
	}
}

// A form body redirecting to a string response field: the descriptor carries the field and the JSON key the
// REST response spells it with, and the rpc keeps its REST route.
func TestRestBrowserParses(t *testing.T) {
	skipIfNoProtoc(t)

	p, err := restBrowserParser(t, rbService("service_c2s", rbCallback, `{form_body: true, redirect_field: "redirect_url"}`))
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	fd, err := p.GetFileDescriptor("rbtest.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}
	m := fd.Services[0].Methods[0]
	want := RestBrowserDescriptor{FormBody: true, RedirectField: "redirect_url", RedirectJSONName: "redirectUrl"}
	if m.RestBrowser == nil || *m.RestBrowser != want {
		t.Fatalf("RestBrowser = %+v, want %+v", m.RestBrowser, want)
	}
	if m.HttpPath == "" {
		t.Fatal("a rest_browser rpc must keep its REST route")
	}

	info, err := p.ConvertToProtoInfo("rbtest.proto")
	if err != nil {
		t.Fatalf("ConvertToProtoInfo: %v", err)
	}
	if got := info.Services[0].Methods[0].RestBrowser; got == nil || *got != want {
		t.Fatalf("MethodInfo.RestBrowser = %+v, want %+v", got, want)
	}
}

// An rpc without the annotation carries no descriptor, so its route generates
// exactly as before.
func TestRestBrowserAbsent(t *testing.T) {
	skipIfNoProtoc(t)

	p, err := restBrowserParser(t, `
service Signin {
  option (spec.service_c2s) = {};
  `+rbCallback+`;
}
`)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	fd, err := p.GetFileDescriptor("rbtest.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}
	if rb := fd.Services[0].Methods[0].RestBrowser; rb != nil {
		t.Fatalf("RestBrowser = %+v, want nil", *rb)
	}
}
