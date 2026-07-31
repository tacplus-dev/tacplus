// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import "embed"

// SysProtos embeds all library proto files from sys/.
// These are the canonical copies used by the tacplus compiler at runtime.
//
//go:embed sys/*.proto
var SysProtos embed.FS
