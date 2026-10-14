// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import "embed"

// SysProtos embeds all library proto files from sys/.
// These are the canonical copies used by the tacplus compiler at runtime.
//
//go:embed sys/*.proto
var SysProtos embed.FS
