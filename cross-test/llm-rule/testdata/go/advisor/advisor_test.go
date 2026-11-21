// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"context"
	"testing"

	port "gitlab.com/example/dummy/advised/model/advisor"
)

// echo is the core's stand-in: what the advisor asked, on which trace.
type echo struct{}

func (echo) Generate(_ context.Context, traceID string, req *port.CompleteRequest) (*port.CompleteResponse, error) {
	return &port.CompleteResponse{Text: traceID + ":" + req.GetPrompt()}, nil
}

func TestTheAdvisorAsksThroughThePortOnItsTrace(t *testing.T) {
	got, err := Ask(context.Background(), echo{}, "trace-1", "ping")
	if err != nil || got != "trace-1:ping" {
		t.Fatalf("Ask = %q, %v; want trace-1:ping", got, err)
	}
}
