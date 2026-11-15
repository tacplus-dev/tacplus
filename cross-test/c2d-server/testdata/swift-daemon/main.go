// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// The daemon the Swift leg of cross-test/c2d-server dials (issue #494): a
// cluster-less daemon main, outside the generated packages, serving the
// generated standalone C2D host on sm.C2DSocketPath, the socket_path its
// service declares, until SIGTERM. The Swift C2DClient's default socket must
// be that path, or its no-argument dial reaches no daemon.

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"google.golang.org/grpc"

	"example.com/c2dserver/model/chassis/servicemanager"
	sm "example.com/c2dserver/model/s2sm"
	c2dserver_fe "example.com/c2dserver/model/s2sm/grpc/servers/svc/c2dserver"
	types "example.com/c2dserver/model/types/svc/c2dserver"
)

type daemon struct{}

var _ sm.Handler = daemon{}

func (daemon) LocalDaemonGrpcImpl() c2dserver_fe.LocalDaemonHandler { return daemon{} }

func (daemon) Shutdown(context.Context) {}

func (daemon) GetLocal(_ context.Context, _ *servicemanager.Caller, req *types.LocalRequest) (*types.LocalResponse, error) {
	return &types.LocalResponse{ResultId: "result-" + req.GetItemId()}, nil
}

func (daemon) GenerateLocalEvents(_ *servicemanager.Caller, req *types.LocalRequest, stream grpc.ServerStreamingServer[types.LocalEvent]) error {
	for i := int32(0); i < req.GetCount(); i++ {
		if err := stream.Send(&types.LocalEvent{ItemId: req.GetItemId(), Seq: i}); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := os.MkdirAll(filepath.Dir(sm.C2DSocketPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := sm.ServeC2DStandalone(ctx, sm.C2DSocketPath, daemon{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
