// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"go/format"
	"strings"
	"testing"
	"text/template"
)

func TestStateWrapperHasNoLockMachineryRender(t *testing.T) {
	readOnly := renderStateWrapperForTest(t, stateClientData{
		PackageName:           "demo",
		ClientTypeName:        "StateClient",
		StateServiceName:      "DemoStateService",
		StateRootName:         "DemoState",
		TypesImportPath:       "example.com/project/tacplus/types/services/demo",
		ServiceManagerLibPath: "example.com/project/tacplus/chassis/servicemanager",
		TraceTypesPath:        "example.com/project/tacplus/types/sys/trace",
		ReadOnly:              true,
		Singletons: []singletonBoundary{{
			FieldName: "Config",
			TypeName:  "Config",
			GoType:    "Config",
		}},
	})
	// fmt has exactly one user left: the singleton Fetch's validate-retry timeout
	// error. Any second use would mean the removed locked accessors came back.
	if got := strings.Count(readOnly, "fmt."); got != 1 {
		t.Fatalf("read-only state wrapper has %d fmt uses, want exactly 1 (the singleton Fetch timeout error):\n%s", got, readOnly)
	}
	for _, expected := range []string{
		"type WatchStatus struct",
		"func (c *StateClient) SetWatchStatusListener(listener func(WatchStatus))",
		"func (c *StateClient) WatchStatuses() map[string]WatchStatus",
		"func (c *StateClient) WatchesHealthy() bool",
		"c.recordWatchHealthy(\"WatchConfig\", false)",
		"c.recordWatchRetrying(\"WatchConfig\", received, attempt+1, \"stream ended\", nil)",
	} {
		if !strings.Contains(readOnly, expected) {
			t.Fatalf("read-only state wrapper missing watch status surface %q:\n%s", expected, readOnly)
		}
	}

	// The ATOMIC / optimistic-locking machinery was removed (state is convergent
	// only), so a writable state wrapper must emit no lock surface at all, and must
	// not import fmt (only the removed locked accessors needed it).
	writable := renderStateWrapperForTest(t, stateClientData{
		PackageName:           "demo",
		ClientTypeName:        "StateClient",
		StateServiceName:      "DemoStateService",
		StateRootName:         "DemoState",
		TypesImportPath:       "example.com/project/tacplus/types/services/demo",
		ServiceManagerLibPath: "example.com/project/tacplus/chassis/servicemanager",
		TraceTypesPath:        "example.com/project/tacplus/types/sys/trace",
		Singletons: []singletonBoundary{{
			FieldName: "Config",
			TypeName:  "Config",
			GoType:    "Config",
		}},
	})
	for _, banned := range []string{"lock_pb", "lockClient", "releaseLock", "GetLock", "GetLocked", "SetLocked", "CancelLock"} {
		if strings.Contains(writable, banned) {
			t.Fatalf("writable state wrapper still contains removed lock machinery %q:\n%s", banned, writable)
		}
	}
	if got := strings.Count(writable, "fmt."); got != 1 {
		t.Fatalf("writable state wrapper has %d fmt uses, want exactly 1 (the singleton Fetch timeout error):\n%s", got, writable)
	}

	// The singleton Fetch retries while the fetched value fails its own generated
	// validation, bounded by config_fetch_timeout_seconds — see
	// decisions/state-singleton-startup-retry-2026-09-20.md.
	for _, expected := range []string{
		"func (c *StateClient) configFetchTimeout() time.Duration {",
		"c.sc.Config().GetConfigFetchTimeoutSeconds()",
		"func (c *StateClient) sleepBeforeFetchRetry(ctx context.Context, attempt int, deadline time.Time) bool {",
		"delay := c.watchRetryDelay(attempt)",
		"timeout := w.parent.configFetchTimeout()",
		// Proto defaults are applied to the fetched value BEFORE it is validated;
		// the wire carries no defaults, so an omitted (spec.default_*) field would
		// otherwise fail its own (spec.assert_*) check for the whole retry budget.
		"types.ConfigDefaults(value)\n\t\tvalidateErr := types.ValidateConfig(value)",
		"if !w.parent.sleepBeforeFetchRetry(ctx, attempt, deadline) {",
		"return nil, fmt.Errorf(\"GetConfig: state value still failed validation after %s: %w\", timeout, validateErr)",
	} {
		if !strings.Contains(writable, expected) {
			t.Fatalf("writable state wrapper missing singleton fetch retry surface %q:\n%s", expected, writable)
		}
	}
}

func renderStateWrapperForTest(t *testing.T, data stateClientData) string {
	t.Helper()
	tmplStr, err := loadTemplate("state_wrapper.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	funcMap := template.FuncMap{
		"TypeRef": func(goType string, isCross bool) string {
			if isCross {
				return goType
			}
			return "types." + goType
		},
		"ValidateRef": func(goType string, isCross bool) string {
			if isCross {
				if idx := strings.LastIndex(goType, "."); idx >= 0 {
					return goType[:idx+1] + "Validate" + goType[idx+1:]
				}
				return "Validate" + goType
			}
			return "types.Validate" + goType
		},
		"DefaultsRef": func(goType string, isCross bool) string {
			if isCross {
				return goType + "Defaults"
			}
			return "types." + goType + "Defaults"
		},
		"LowerFirst": func(s string) string {
			if len(s) == 0 {
				return s
			}
			return strings.ToLower(s[:1]) + s[1:]
		},
	}
	tmpl, err := template.New("stateclient").Funcs(funcMap).Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse state wrapper template: %v", err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute state wrapper template: %v", err)
	}
	src, err := format.Source([]byte(out.String()))
	if err != nil {
		t.Fatalf("format state wrapper template: %v\n%s", err, out.String())
	}
	return string(src)
}

// #327: each state-client helper that builds its own request has a traced form
// that puts the trace on it, and today's form calls it with no trace.
func TestStateWrapperRendersTracedForms(t *testing.T) {
	src := renderStateWrapperForTest(t, stateClientData{
		PackageName:           "demo",
		ClientTypeName:        "StateClient",
		StateServiceName:      "DemoStateService",
		StateRootName:         "DemoState",
		TypesImportPath:       "example.com/project/tacplus/types/services/demo",
		ServiceManagerLibPath: "example.com/project/tacplus/chassis/servicemanager",
		TraceTypesPath:        "example.com/project/tacplus/types/sys/trace",
		Singletons:            []singletonBoundary{{FieldName: "Config", TypeName: "Config", GoType: "Config"}},
		Collections:           []collectionBoundary{{FieldName: "Players", TypeName: "Player", GoType: "Player", KeyFieldName: "PlayerId", KeyGoType: "string", ClearTrace: true}},
		Topics:                []topicBoundary{{FieldName: "Events", TypeName: "Event", GoType: "Event"}},
		Queues:                []queueBoundary{{FieldName: "Tasks", TypeName: "Task", GoType: "Task"}},
		PriorityQueues:        []priorityQueueBoundary{{FieldName: "Alerts", TypeName: "Alert", GoType: "Alert", PriorityField: "severity"}},
		Views:                 []viewBoundary{{FieldName: "AllPlayers", TypeName: "Player", GoType: "Player", KeyFieldName: "PlayerId", KeyGoType: "string", ViewSources: []string{"Players"}}},
	})
	for _, want := range []string{
		"func (w *ConfigWrapper) FetchTraced(ctx context.Context, traceID string) (*types.Config, error)",
		"w.parent.stateStub.GetConfig(ctx, &trace_pb.TraceRequest{TraceId: traceID})",
		"func (c *StateClient) FetchPlayersTraced(ctx context.Context, key, traceID string) (*types.Player, error)",
		"&types.PlayersGetRequest{Key: key, TraceId: traceID}",
		"func (c *StateClient) QueryPlayersTraced(ctx context.Context, q *types.CollectionQuery, traceID string)",
		"func (c *StateClient) WatchQueryPlayersTraced(ctx context.Context, q *types.CollectionQuery, traceID string)",
		"&types.PlayersQueryRequest{Query: q, TraceId: traceID}",
		"func (c *StateClient) FetchAllPlayersTraced(ctx context.Context, traceID string)",
		"&types.PlayersGetAllRequest{TraceId: traceID}",
		"func (c *StateClient) PutPlayersTraced(ctx context.Context, key string, value *types.Player, traceID string)",
		"&types.PlayersPutRequest{Key: key, Value: value, TraceId: traceID}",
		"func (c *StateClient) DeletePlayersTraced(ctx context.Context, key, traceID string) error",
		"func (w *EventsWrapper) SubscribeTraced(ctx context.Context, traceID string)",
		"func (w *TasksWrapper) ConsumeTraced(ctx context.Context, traceID string)",
		"func (w *TasksWrapper) PeekTraced(ctx context.Context, traceID string)",
		"func (w *TasksWrapper) SizeTraced(ctx context.Context, traceID string)",
		"func (w *AlertsWrapper) ConsumeTraced(ctx context.Context, traceID string)",
		"func (c *StateClient) FetchAllPlayersTraced(ctx context.Context, traceID string)",
		"func (c *StateClient) ContainsKeyAllPlayersTraced(ctx context.Context, key, traceID string) (bool, error)",
		"&types.AllPlayersKeysRequest{TraceId: traceID}",
		"&types.AllPlayersSizeRequest{TraceId: traceID}",
		"func (c *StateClient) WatchAllPlayersTraced(ctx context.Context, traceID string)",
		// Today's forms are the traced ones with no trace.
		"return w.FetchTraced(ctx, \"\")",
		"return c.FetchPlayersTraced(ctx, key, \"\")",
		"return c.PutPlayersTraced(ctx, key, value, \"\")",
		"return w.ConsumeTraced(ctx, \"\")",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("state wrapper missing %q", want)
		}
	}
	// A stored value still keeps no trace: Put clears the value's own trace and
	// the trace goes on the request only.
	if !strings.Contains(src, "value.TraceId = \"\"") {
		t.Error("PutPlayersTraced must still clear the stored value's trace (#313)")
	}
	// Every request a helper builds carries the trace, but the background
	// watch loops' (no request causes them).
	if got, want := strings.Count(src, "&trace_pb.TraceRequest{}"), 2; got != want {
		t.Errorf("%d untraced TraceRequest literals, want %d (the two watch loops)", got, want)
	}
}
