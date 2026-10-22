// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// findMessageInParser searches for a message by package and type name across all parsed files.
func findMessageInParser(dp *parser.DescriptorParser, pkg, typeName string) *parser.MessageDescriptor {
	for _, fileDesc := range dp.GetAllFiles() {
		if fileDesc.Package == pkg {
			for i := range fileDesc.Messages {
				if fileDesc.Messages[i].Name == typeName {
					return &fileDesc.Messages[i]
				}
			}
		}
	}
	return nil
}

// extractSimpleTypeName gets the simple type name from a potentially dotted type.
func extractSimpleTypeName(fullType string) string {
	parts := strings.Split(fullType, ".")
	return parts[len(parts)-1]
}
