// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// #327: every generated helper that builds its request itself has a traced
// overload (a trailing String traceId) that puts the trace on the request(s)
// it sends, and today's form delegates to it with "".

func TestJavaStateClientTracedOverloads(t *testing.T) {
	out := renderJavaStateWrapperForTest(t, javaStateWrapperData{
		Header:           "// generated for test",
		Package:          "com.example.tacplus.s2sm.internal.state.services.demo.demostate",
		OuterClassName:   "Demo",
		StateRootName:    "DemoState",
		StateServiceName: "DemoStateService",
		GrpcServiceFQN:   "com.example.tacplus.types.services.demo.DemoStateServiceGrpc",
		TraceFQN:         "com.example.tacplus.types.sys.trace.Trace",
		BackoffFQN:       "com.example.tacplus.chassis.backoff.Backoff",
		Singletons: []javaSingletonBoundary{{
			FieldName: "config", FieldPascal: "Config", TypeName: "Config", JavaType: "Demo.Config",
			ValidateRef: "com.example.tacplus.types.services.demo.Helpers.validateConfig",
		}},
		Collections: []javaCollectionBoundary{{
			FieldName: "players", FieldPascal: "Players", TypeName: "Player", JavaType: "Demo.Player",
			KeyFieldName: "playerId", KeyFieldPascal: "PlayerId", MaxEntries: 10, ClearTrace: true,
		}},
	})
	for _, want := range []string{
		// singleton
		"public Demo.Config fetchConfig() {\n        return fetchConfig(\"\");\n    }",
		"public Demo.Config fetchConfig(String traceId) {",
		"stateStub.getConfig(Trace.TraceRequest.newBuilder().setTraceId(traceId).build())",
		"public Demo.Config fetch() {\n            return parent.fetchConfig();\n        }",
		"public Demo.Config fetch(String traceId) {\n            return parent.fetchConfig(traceId);\n        }",
		// collection
		"public Map<String, Demo.Player> fetchAllPlayers() {\n        return fetchAllPlayers(\"\");\n    }",
		"public Map<String, Demo.Player> fetchAllPlayers(String traceId) {",
		"Demo.PlayersGetAllRequest.newBuilder()\n                        .setTraceId(traceId)\n                        .build();",
		"public void putPlayers(String key, Demo.Player value) {\n        putPlayers(key, value, \"\");\n    }",
		"public void putPlayers(String key, Demo.Player value, String traceId) {",
		"public void deletePlayers(String key) {\n        deletePlayers(key, \"\");\n    }",
		"public void deletePlayers(String key, String traceId) {",
		"Demo.PlayersGetRequest.newBuilder()\n                        .setKey(key)\n                        .setTraceId(traceId)\n                        .build();",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("state client missing %q:\n%s", want, out)
		}
	}
	// The trace goes on the PutRequest; the stored value still keeps none (#313).
	put := out[strings.Index(out, "public void putPlayers(String key, Demo.Player value, String traceId) {"):]
	put = put[:strings.Index(put, "stateStub.putPlayers(req);")]
	if !strings.Contains(put, ".setTraceId(traceId)") || !strings.Contains(put, ".setValue(value.toBuilder().clearTraceId().build())") {
		t.Errorf("traced putPlayers must trace the request and clear the value:\n%s", put)
	}
	if strings.Contains(out, "getConfig(Trace.TraceRequest.getDefaultInstance())") {
		t.Errorf("the singleton fetch must not send an untraced default TraceRequest")
	}
}

func TestJavaRecordReaderTracedOverloads(t *testing.T) {
	for _, tc := range []struct {
		name           string
		routed, delete bool
		key            string
		getUntraced    string
		getTraced      string
		listTail       string
	}{
		{"plain", false, false, "String", "get(String key)", "get(String key, String traceId)", "String pageToken"},
		{"routed", true, false, "String", "get(String tenant, String key)", "get(String tenant, String key, String traceId)", "String pageToken"},
		{"deleted", false, true, "long", "get(long key, boolean includeDeleted)", "get(long key, boolean includeDeleted, String traceId)", "String pageToken, boolean includeDeleted"},
		{"routed+deleted", true, true, "String", "get(String tenant, String key, boolean includeDeleted)", "get(String tenant, String key, boolean includeDeleted, String traceId)", "String pageToken, boolean includeDeleted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := javaMessageReaderEntry()
			e.Routed, e.HasDeletedFlag, e.KeyJavaType = tc.routed, tc.delete, tc.key
			if tc.routed {
				e.RouteSetter = "setTargetTenant"
			}
			out := renderJavaReader(t, e)
			for _, want := range []string{
				"> " + tc.getUntraced + " {",
				"> " + tc.getTraced + " {",
				tc.listTail + ") {",
				tc.listTail + ", String traceId) {",
				".setLimit(1)\n                .setTraceId(traceId);",
				"newBuilder().setStanding(false).setTraceId(traceId);",
				".setPageToken(pageToken)\n                    .setTraceId(traceId)",
				".setTraceId(traceId).build());\n            } catch (RuntimeException ignored) {",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("reader missing %q:\n%s", want, out)
				}
			}
			// Each untraced form only delegates with "": exactly one Rows read,
			// one Create, one List and one Delete in the class.
			for _, call := range []string{"client.getMessageRows(", "client.createMessageQuery(", "client.listMessagePage(", "client.deleteMessageQuery("} {
				if n := strings.Count(out, call); n != 1 {
					t.Errorf("%s appears %d times; the untraced form must delegate", call, n)
				}
			}
			if !strings.Contains(out, `, "");`+"\n    }") {
				t.Errorf("untraced forms must delegate with \"\":\n%s", out)
			}
		})
	}
}

func TestJavaRecordC2SHandlerPropagatesInboundTrace(t *testing.T) {
	out, err := renderJavaRecordC2SPlane(javaRecordC2SFixture())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if n := strings.Count(string(out), "request.getTraceId())"); n != 2 {
		t.Fatalf("both reads must call the traced reader with the inbound trace, got %d:\n%s", n, out)
	}
}

func TestJavaTokenForExternalTracedOverload(t *testing.T) {
	im := readJavaTemplate(t, "java/chassis/service_im/IdentityManager.java.tmpl")
	for _, want := range []string{
		"public String tokenForExternal(String audience) throws Exception {\n        return tokenForExternal(audience, \"\");\n    }",
		"public String tokenForExternal(String audience, String traceId) throws Exception {",
		".setExp(java.time.Instant.now().getEpochSecond() + EXTERNAL_TOKEN_LIFETIME_SECS)\n                        .setTraceId(traceId)\n                        .build());",
	} {
		if !strings.Contains(im, want) {
			t.Errorf("IdentityManager missing %q", want)
		}
	}
	sm := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")
	if !strings.Contains(sm, "public String tokenForExternal(String audience, String traceId) throws Exception {\n        return identity.tokenForExternal(audience, traceId);\n    }") {
		t.Errorf("ServiceManager must expose the traced tokenForExternal")
	}
}
