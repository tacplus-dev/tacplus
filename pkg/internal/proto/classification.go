// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"regexp"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// Match any same-line prefix with no slash (so inline `{ option` matches but `// option` does not) so a commented-out
// annotation is not false-matched as active (MS-10 class).
var serviceStateSourceOptionRe = regexp.MustCompile(`(?m)^[^\n/]*?option\s*\(\s*spec\.(service_state|service_event)\s*\)`)

func DeclaresServiceState(fileDesc *parser.FileDescriptor) bool {
	if fileDesc == nil {
		return false
	}
	for i := range fileDesc.Messages {
		ann, ok := fileDesc.Messages[i].Annotations["service_state"]
		if ok && ann.StringValue != "" {
			return true
		}
	}
	return false
}

func DeclaresServiceStateModel(file model.ProtoFile) bool {
	for i := range file.Messages {
		ann, ok := file.Messages[i].Annotations["service_state"]
		if ok && ann.StringValue != "" {
			return true
		}
	}
	return false
}

func DeclaresServiceStateSource(source string) bool {
	return serviceStateSourceOptionRe.MatchString(source)
}

func HasSynthesizedServices(fileDesc *parser.FileDescriptor) bool {
	if fileDesc == nil {
		return false
	}
	for i := range fileDesc.Services {
		if fileDesc.Services[i].Synthesized {
			return true
		}
	}
	return false
}

func IsSyntheticServiceOnlyFile(fileDesc *parser.FileDescriptor) bool {
	return HasSynthesizedServices(fileDesc) && !DeclaresServiceState(fileDesc)
}
