// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The trace-helpers gate's Go state-client leg (tacplus#327).
package helper_state

import (
	"context"
	"testing"

	"gitlab.com/example/tacplus/trace/tracehelpers/model/chassis/servicemanager"
	types "gitlab.com/example/tacplus/trace/tracehelpers/model/types/services/tracehelpers"
)

func TestTraceHelpersState(t *testing.T) {
	conn := &recordingConn{}
	c := &StateClient{
		sc:            &servicemanager.ServiceManager{},
		stateStub:     types.NewHelperStateServiceClient(recordingT{recordingConn: conn, t: t}),
		cachedPlayers: map[string]*types.Player{},
		cachedBench:   map[string]*types.Player{},
		ctx:           context.Background(),
		watchStatuses: map[string]WatchStatus{},
	}
	c.wrapperSettings = &SettingsWrapper{parent: c}
	q := &types.CollectionQuery{}
	player := &types.Player{PlayerId: "p-1", TraceId: "stored-value-trace"}

	runHelperCases(t, conn, "state", []helperCase{
		{"Settings.Fetch", func(ctx context.Context, tr string) error { _, err := c.Settings().FetchTraced(ctx, tr); return err },
			func(ctx context.Context) error { _, err := c.Settings().Fetch(ctx); return err }, []string{"GetSettings"}},
		{"FetchPlayers", func(ctx context.Context, tr string) error { _, err := c.FetchPlayersTraced(ctx, "p-1", tr); return err },
			func(ctx context.Context) error { _, err := c.FetchPlayers(ctx, "p-1"); return err }, []string{"GetPlayers"}},
		{"QueryPlayers", func(ctx context.Context, tr string) error { _, _, err := c.QueryPlayersTraced(ctx, q, tr); return err },
			func(ctx context.Context) error { _, _, err := c.QueryPlayers(ctx, q); return err }, []string{"QueryPlayers"}},
		{"WatchQueryPlayers", func(ctx context.Context, tr string) error {
			_, err := c.WatchQueryPlayersTraced(ctx, q, tr)
			return err
		},
			func(ctx context.Context) error { _, err := c.WatchQueryPlayers(ctx, q); return err }, []string{"WatchQueryPlayers"}},
		{"FetchAllPlayers", func(ctx context.Context, tr string) error { _, err := c.FetchAllPlayersTraced(ctx, tr); return err },
			func(ctx context.Context) error { _, err := c.FetchAllPlayers(ctx); return err }, []string{"GetAllPlayers"}},
		{"PutPlayers", func(ctx context.Context, tr string) error {
			_, err := c.PutPlayersTraced(ctx, "p-1", player, tr)
			return err
		},
			func(ctx context.Context) error { _, err := c.PutPlayers(ctx, "p-1", player); return err }, []string{"PutPlayers"}},
		{"DeletePlayers", func(ctx context.Context, tr string) error { return c.DeletePlayersTraced(ctx, "p-1", tr) },
			func(ctx context.Context) error { return c.DeletePlayers(ctx, "p-1") }, []string{"DeletePlayers"}},
		{"FetchRoster", func(ctx context.Context, tr string) error { _, err := c.FetchRosterTraced(ctx, "p-1", tr); return err },
			func(ctx context.Context) error { _, err := c.FetchRoster(ctx, "p-1"); return err }, []string{"GetRoster"}},
		{"FetchAllRoster", func(ctx context.Context, tr string) error { _, err := c.FetchAllRosterTraced(ctx, tr); return err },
			func(ctx context.Context) error { _, err := c.FetchAllRoster(ctx); return err }, []string{"GetAllRoster"}},
		{"ContainsKeyRoster", func(ctx context.Context, tr string) error {
			_, err := c.ContainsKeyRosterTraced(ctx, "p-1", tr)
			return err
		},
			func(ctx context.Context) error { _, err := c.ContainsKeyRoster(ctx, "p-1"); return err }, []string{"ContainsKeyRoster"}},
		{"KeySetRoster", func(ctx context.Context, tr string) error { _, err := c.KeySetRosterTraced(ctx, tr); return err },
			func(ctx context.Context) error { _, err := c.KeySetRoster(ctx); return err }, []string{"KeySetRoster"}},
		{"SizeRoster", func(ctx context.Context, tr string) error { _, err := c.SizeRosterTraced(ctx, tr); return err },
			func(ctx context.Context) error { _, err := c.SizeRoster(ctx); return err }, []string{"SizeRoster"}},
		{"WatchRoster", func(ctx context.Context, tr string) error { _, err := c.WatchRosterTraced(ctx, tr); return err },
			func(ctx context.Context) error { _, err := c.WatchRoster(ctx); return err }, []string{"WatchRoster"}},
		{"QueryRoster", func(ctx context.Context, tr string) error { _, _, err := c.QueryRosterTraced(ctx, q, tr); return err },
			func(ctx context.Context) error { _, _, err := c.QueryRoster(ctx, q); return err }, []string{"QueryRoster"}},
		{"WatchQueryRoster", func(ctx context.Context, tr string) error { _, err := c.WatchQueryRosterTraced(ctx, q, tr); return err },
			func(ctx context.Context) error { _, err := c.WatchQueryRoster(ctx, q); return err }, []string{"WatchQueryRoster"}},
	})
	// The stored value keeps no trace (#313), and the caller's own is left as it was.
	if player.TraceId != "stored-value-trace" {
		t.Errorf("PutPlayers changed the caller's value: trace_id %q", player.TraceId)
	}
}
