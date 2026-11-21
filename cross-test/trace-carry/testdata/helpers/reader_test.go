// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The trace-helpers gate's Go record-reader leg (tacplus#327).
package sm

import (
	"context"
	"testing"

	helperquery_gc "gitlab.com/example/tacplus/trace/tracehelpers/model/s2sm/grpc/clients/vservices/helperquery"
	helperquery_pb "gitlab.com/example/tacplus/trace/tracehelpers/model/types/vservices/helperquery"
)

func TestTraceHelpersRecordReader(t *testing.T) {
	conn := &recordingConn{}
	client := helperquery_gc.NewHelperRecordMapClient(recordingT{recordingConn: conn, t: t}, func() (string, error) { return "token", nil })
	r := NewHelperRowRecordReader(client)
	filter := &helperquery_pb.HelperRowFilter{}
	list := []string{"CreateHelperRowQuery", "ListHelperRow", "DeleteHelperRowQuery"}

	runHelperCases(t, conn, "reader", []helperCase{
		{"Get", func(ctx context.Context, tr string) error { _, err := r.GetTraced(ctx, "r-1", tr); return err },
			func(ctx context.Context) error { _, err := r.Get(ctx, "r-1"); return err }, []string{"GetHelperRowRows"}},
		{"List", func(ctx context.Context, tr string) error {
			_, _, _, err := r.ListTraced(ctx, filter, 10, "", tr)
			return err
		},
			func(ctx context.Context) error { _, _, _, err := r.List(ctx, filter, 10, ""); return err }, list},
	})
}
