// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The stream-expiry and rotation fixtures copy this file into their generated
// chassis/grpcserver; it is not part of the generated output. It lets a test,
// in another package, serve on the port-0 listener it bound instead of
// releasing the port for Listen to bind again, which another process may take
// in between (#713).

package grpcserver

import (
	"context"
	"net"
)

// ServeOn serves on lis instead of binding the configured address.
func (ps *GrpcServer) ServeOn(ctx context.Context, lis net.Listener) error {
	ps.listener = lis
	return ps.Listen(ctx)
}
