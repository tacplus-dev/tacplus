// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// AppendPublicViewMessagesToProtos appends, to each proto file that declares a
// (spec.public_view) message T, the generated "<T>Public" message holding only
// T's VISIBILITY_SHOW fields (see parser.PublicViewMessages). Emitted as PROTO,
// before native compilation, so the public view lands in every language as an
// ordinary message and the per-language <T>ToPublic is a parse through it.
func AppendPublicViewMessagesToProtos(tempProtoDir string) error {
	dp, err := parser.NewDescriptorParser(tempProtoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for public view generation: %w", err)
	}
	views := dp.PublicViewMessages()
	relPaths := make([]string, 0, len(views))
	for relPath := range views {
		relPaths = append(relPaths, relPath)
	}
	sort.Strings(relPaths)
	for _, relPath := range relPaths {
		if IsInfraProto(relPath) {
			continue
		}
		filePath := filepath.Join(tempProtoDir, relPath)
		content, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read %s for public view generation: %w", relPath, err)
		}
		out := string(content) + "\n// Auto-generated public views from spec.public_view (tacplus)\n" + views[relPath]
		if err := os.WriteFile(filePath, []byte(out), 0644); err != nil {
			return fmt.Errorf("write %s after public view generation: %w", relPath, err)
		}
	}
	return nil
}
