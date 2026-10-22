// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Package docs embeds all tacplus documentation files.
// These are compiled into the tacplus binary and made available
// to generated registries so that LLMs, tools, and humans can
// discover documentation without external file access.
package docs

import "embed"

//go:embed *.md *.tmpl
var FS embed.FS
