// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package templates

import "embed"

//go:embed all:go all:java all:typescript all:python all:rust all:swift all:csharp
var FS embed.FS
