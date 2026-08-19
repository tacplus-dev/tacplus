// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"bytes"
	"strings"
	"testing"
	"text/template"
)

func TestRustRestDispatchValidatesUnaryResponseBeforeJson(t *testing.T) {
	out := renderRustRestDispatchForTest(t, restDispatchData{
		UnaryArms: []dispatchArm{
			{
				GrpcPath:       "/contract.ContractApi/GetAudit",
				ServiceID:      "contract",
				MethodSnake:    "get_audit",
				ClientPath:     "contract::contract_api_client::ContractApiClient",
				FromJsonPath:   "contract::get_audit_request_from_json",
				ToJsonPath:     "contract::get_audit_response_to_json",
				ValidateOutput: true,
			},
		},
	})

	resp := strings.Index(out, "let resp = resp.into_inner();")
	validate := strings.Index(out, "resp.validate().map_err")
	toJSON := strings.Index(out, "contract::get_audit_response_to_json(&resp)")
	encode := strings.Index(out, "String::from_utf8(bytes)")
	for label, idx := range map[string]int{
		"response binding":    resp,
		"response validation": validate,
		"JSON serialization":  toJSON,
		"UTF-8 encoding":      encode,
	} {
		if idx < 0 {
			t.Fatalf("rendered REST dispatch missing %s:\n%s", label, out)
		}
	}
	if !(resp < validate && validate < toJSON && toJSON < encode) {
		t.Fatalf("REST unary response must validate before JSON serialization:\n%s", out)
	}
	if strings.Contains(out, "String::from_utf8(bytes).unwrap_or_default()") {
		t.Fatalf("REST unary response must not hide UTF-8 failures:\n%s", out)
	}
}

func TestRustRestDispatchValidatesSSEItemBeforeJson(t *testing.T) {
	out := renderRustRestDispatchForTest(t, restDispatchData{
		HasStreaming: true,
		StreamArms: []dispatchArm{
			{
				GrpcPath:       "/contract.ContractApi/WatchAudit",
				ServiceID:      "contract",
				MethodSnake:    "watch_audit",
				ClientPath:     "contract::contract_api_client::ContractApiClient",
				FromJsonPath:   "contract::watch_audit_request_from_json",
				ToJsonPath:     "contract::watch_audit_event_to_json",
				ValidateOutput: true,
			},
		},
	})

	validate := strings.Index(out, "msg.validate()")
	toJSON := strings.Index(out, "contract::watch_audit_event_to_json(&msg)")
	event := strings.Index(out, "Event::default().data(json)")
	for label, idx := range map[string]int{
		"SSE item validation": validate,
		"JSON serialization":  toJSON,
		"SSE data event":      event,
	} {
		if idx < 0 {
			t.Fatalf("rendered REST dispatch missing %s:\n%s", label, out)
		}
	}
	if !(validate < toJSON && toJSON < event) {
		t.Fatalf("SSE item response must validate before JSON serialization/write:\n%s", out)
	}
	for _, want := range []string{
		`sse_error(format!("response validation failed: {}", e))`,
		`sse_error(format!("failed to serialize response: {}", e))`,
		`sse_error(format!("failed to encode response: {}", e))`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered REST dispatch missing SSE error handling %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "unwrap_or_default()") {
		t.Fatalf("SSE dispatch must not hide serialization/encoding failures:\n%s", out)
	}
}

func TestRustRestDispatchValidatesRequestBeforeForwarding(t *testing.T) {
	out := renderRustRestDispatchForTest(t, restDispatchData{
		HasStreaming: true,
		UnaryArms: []dispatchArm{
			{
				GrpcPath:      "/contract.ContractApi/GetAudit",
				ServiceID:     "contract",
				MethodSnake:   "get_audit",
				ClientPath:    "contract::contract_api_client::ContractApiClient",
				FromJsonPath:  "contract::get_audit_request_from_json",
				ToJsonPath:    "contract::get_audit_response_to_json",
				ValidateInput: true,
			},
		},
		StreamArms: []dispatchArm{
			{
				GrpcPath:      "/contract.ContractApi/WatchAudit",
				ServiceID:     "contract",
				MethodSnake:   "watch_audit",
				ClientPath:    "contract::contract_api_client::ContractApiClient",
				FromJsonPath:  "contract::watch_audit_request_from_json",
				ToJsonPath:    "contract::watch_audit_event_to_json",
				ValidateInput: true,
			},
		},
	})

	decode := strings.Index(out, "contract::get_audit_request_from_json(body)")
	validate := strings.Index(out, "req.validate().map_err")
	forward := strings.Index(out, ".get_audit(with_metadata(req")
	streamDecode := strings.Index(out, "contract::watch_audit_request_from_json(body)")
	streamValidate := strings.Index(out, "if let Err(e) = req.validate()")
	streamForward := strings.Index(out, ".watch_audit(with_metadata(req")
	for label, idx := range map[string]int{
		"unary request decode":     decode,
		"unary request validation": validate,
		"unary forward":            forward,
		"SSE request decode":       streamDecode,
		"SSE request validation":   streamValidate,
		"SSE forward":              streamForward,
	} {
		if idx < 0 {
			t.Fatalf("rendered REST dispatch missing %s:\n%s", label, out)
		}
	}
	if !(decode < validate && validate < forward) {
		t.Fatalf("REST unary request must validate before forwarding:\n%s", out)
	}
	if !(streamDecode < streamValidate && streamValidate < streamForward) {
		t.Fatalf("SSE request must validate before forwarding:\n%s", out)
	}
}

func renderRustRestDispatchForTest(t *testing.T, data restDispatchData) string {
	t.Helper()
	tmplStr, err := loadTemplate("rest_dispatch.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("rest_dispatch").Delims("<<", ">>").Parse(tmplStr)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return buf.String()
}
