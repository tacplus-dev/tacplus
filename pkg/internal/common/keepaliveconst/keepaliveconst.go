// Copyright (c) 2026 Second Star Cavalier LLC

// Package keepaliveconst defines the platform-wide gRPC keepalive constants.
// These are the single source of truth that all language generators inject
// into their templates, ensuring client and server sides are always consistent
// BY CONSTRUCTION: change here, regenerate, both sides update atomically.
//
// Invariant: PermitMinTimeSecs ≤ TimeSecs. The server must permit pings at
// intervals at least as long as the client sends them; violating this causes
// the server to GOAWAY with "too_many_pings" and tear down a working connection.
package keepaliveconst

const (
	// TimeSecs is the interval at which generated clients send HTTP/2 PING
	// frames to detect dead-but-open connections. Chosen to catch 80-second
	// partitions (as observed in drill-7) within one ping cycle.
	TimeSecs = 30

	// TimeoutSecs is how long a generated client waits for a PING ACK before
	// declaring the connection dead and triggering the reconnect loop.
	TimeoutSecs = 10

	// PermitMinTimeSecs is the minimum interval the generated server enforces
	// between client pings. Must be ≤ TimeSecs; set to 10s so any client
	// pinging every 30s is well within the allowed range.
	PermitMinTimeSecs = 10
)
