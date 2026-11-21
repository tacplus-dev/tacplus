package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/templates"
)

// #327: every generated helper that builds its own request has a traced form
// beside today's, taking a trace id and putting it on the request(s) it sends.
// Today's form keeps its signature and delegates with "". No ambient carrier.

func TestRustStateClientTracedForms(t *testing.T) {
	out := renderRustStateWrapperForTest(t, rustStateWrapperData{
		Header:                 "// generated for test",
		StateRootName:          "DemoState",
		TypesCratePath:         "services::demo",
		PackageName:            "demo",
		StateServiceName:       "DemoStateService",
		StateServiceModule:     "demo_state_service_client",
		StateServiceClientType: "DemoStateServiceClient",
		Singletons: []rustSingletonBoundary{{
			FieldName: "config", FieldPascal: "Config", TypeName: "Config", DefaultsFn: "config_defaults", ClearTrace: true,
		}},
		Collections: []rustCollectionBoundary{{
			FieldName: "players", FieldPascal: "Players", TypeName: "Player", KeyFieldName: "player_id", MaxEntries: 10, ClearTrace: true,
		}},
	})
	for _, want := range []string{
		// Singleton fetch: today's form delegates, the traced one sends a
		// TraceRequest carrying the trace.
		"pub async fn fetch(&self) -> Result<Config, tonic::Status> {\n        self.fetch_traced(\"\").await\n    }",
		"pub async fn fetch_traced(&self, trace_id: &str) -> Result<Config, tonic::Status> {",
		".authorized_request(crate::types::sys::trace::TraceRequest {\n                    trace_id: trace_id.to_string(),\n                    ..Default::default()\n                })",
		// Collection helpers, public surface.
		"pub async fn fetch_all_players(&self) -> Result<HashMap<String, Player>, tonic::Status> {\n        self.inner.fetch_all_players_traced(\"\").await\n    }",
		"pub async fn fetch_all_players_traced(&self, trace_id: &str) -> Result<HashMap<String, Player>, tonic::Status> {\n        self.inner.fetch_all_players_traced(trace_id).await\n    }",
		"pub async fn put_players(&self, key: String, value: Player) -> Result<(), tonic::Status> {\n        self.inner.put_players_traced(key, value, \"\").await\n    }",
		"pub async fn put_players_traced(&self, key: String, value: Player, trace_id: &str) -> Result<(), tonic::Status> {\n        self.inner.put_players_traced(key, value, trace_id).await\n    }",
		"pub async fn delete_players(&self, key: String) -> Result<(), tonic::Status> {\n        self.inner.delete_players_traced(key, \"\").await\n    }",
		"pub async fn delete_players_traced(&self, key: String, trace_id: &str) -> Result<(), tonic::Status> {\n        self.inner.delete_players_traced(key, trace_id).await\n    }",
		// The requests carry the trace; the stored value's is still cleared.
		"PlayersGetAllRequest { trace_id: trace_id.to_string(), ..Default::default() }",
		"PlayersPutRequest { key, value: Some(value), lock_counter: 0, trace_id: trace_id.to_string(), ..Default::default() }",
		"PlayersGetRequest { key, trace_id: trace_id.to_string(), ..Default::default() }",
		"value.trace_id = String::new();\n        let req = self.authorized_request(PlayersPutRequest {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("state client missing traced form %q:\n%s", want, out)
		}
	}
	// set sends the caller's value, which is stored: no traced form.
	if strings.Contains(out, "set_traced") {
		t.Error("set must not grow a traced form: its value is stored and its trace cleared")
	}
}

func TestRustRecordReaderTracedForms(t *testing.T) {
	e := messageReaderEntry()
	e.HasDeletedFlag = true
	e.Routed, e.RouteField = true, "target_tenant"
	out := renderRecordReaders(t, messageReaderFile(e))
	for _, want := range []string{
		"self.get_traced(tenant, delivery_id, include_deleted, \"\").await",
		"    pub async fn get_traced(\n        &self,\n        tenant: &str,\n        delivery_id: &str,\n        include_deleted: bool,\n        trace_id: &str,\n    ) -> Result<Option<Message>, tonic::Status> {",
		"self.list_traced(tenant, filter, page_size, page_token, include_deleted, \"\").await",
		"    pub async fn list_traced(\n        &self,\n        tenant: &str,\n        filter: Option<MessageFilter>,\n        page_size: i32,\n        page_token: impl Into<String>,\n        include_deleted: bool,\n        trace_id: &str,\n    ) -> Result<(Vec<Message>, String, i64), tonic::Status> {",
		"self.query_page(tenant, filter, page_size, page_token.into(), include_deleted, trace_id).await",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("record reader missing %q:\n%s", want, out)
		}
	}
	// The direct read, and a query's open, pull and release: every request
	// carries the trace.
	if n := strings.Count(out, "trace_id: trace_id.to_string(),"); n != 4 {
		t.Errorf("want trace_id on 4 requests (rows, open, pull, release), got %d:\n%s", n, out)
	}
}

func TestRustRecordC2SPropagatesInboundTrace(t *testing.T) {
	out := renderRecordC2S(t, vaultRecordC2S())
	for _, want := range []string{
		".list_traced(Some(filter), 1, String::new(), &req.trace_id)",
		".list_traced(Some(filter), req.page_size, req.page_token, &req.trace_id)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("record_c2s.rs must read on the inbound request's trace; missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, ".list(") {
		t.Errorf("record_c2s.rs must not call the untraced reader:\n%s", out)
	}
}

func TestRustTokenForExternalTracedForm(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"rust/chassis/service_im/src/manager.rs.tmpl", "pub async fn token_for_external(&self, audience: &str) -> Result<String, Box<dyn std::error::Error>> {\n        self.token_for_external_traced(audience, \"\").await\n    }"},
		{"rust/chassis/service_im/src/manager.rs.tmpl", "pub async fn token_for_external_traced(&self, audience: &str, trace_id: &str) -> Result<String, Box<dyn std::error::Error>> {"},
		{"rust/chassis/service_im/src/manager.rs.tmpl", "exp: now + EXTERNAL_TOKEN_LIFETIME_SECS,\n                    trace_id: trace_id.to_string(),"},
		{"rust/types/service_manager.rs.tmpl", "pub async fn token_for_external(&self, audience: &str) -> Result<String, Box<dyn std::error::Error>> {\n        self.token_for_external_traced(audience, \"\").await\n    }"},
		{"rust/types/service_manager.rs.tmpl", "pub async fn token_for_external_traced(&self, audience: &str, trace_id: &str) -> Result<String, Box<dyn std::error::Error>> {\n        self.identity.token_for_external_traced(audience, trace_id).await\n    }"},
	} {
		src, err := templates.FS.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		if !strings.Contains(string(src), tc.want) {
			t.Errorf("%s missing %q", tc.file, tc.want)
		}
	}
}
