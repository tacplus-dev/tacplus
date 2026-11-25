// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package service

import "testing"

// c2dPipeNameVectors pins the Windows pipe name rule (issue #314, Jody
// 2026-11-08): `\\.\pipe\tacplus-c2d-` + socket_path, every character outside
// [A-Za-z0-9._-] mapped to '-', one '-' per character. The generated Go and
// Rust chassis implement the same rule at run time; cross-test/c2d-server
// checks them against these vectors.
var c2dPipeNameVectors = []struct{ SocketPath, Pipe string }{
	{"/run/mydaemon.sock", `\\.\pipe\tacplus-c2d--run-mydaemon.sock`},
	{"/tmp/c2d_server-1.sock", `\\.\pipe\tacplus-c2d--tmp-c2d_server-1.sock`},
	{`C:\ProgramData\My App\d.sock`, `\\.\pipe\tacplus-c2d-C--ProgramData-My-App-d.sock`},
	{"/run/é/x.sock", `\\.\pipe\tacplus-c2d--run---x.sock`},
	{"", `\\.\pipe\tacplus-c2d-`},
}

func TestC2DPipeName(t *testing.T) {
	for _, v := range c2dPipeNameVectors {
		if got := C2DPipeName(v.SocketPath); got != v.Pipe {
			t.Errorf("C2DPipeName(%q) = %q, want %q", v.SocketPath, got, v.Pipe)
		}
	}
}
