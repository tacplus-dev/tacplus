// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
	"github.com/tacplus-dev/tacplus/templates"
)

// pySerializationMessageData holds pre-computed data for a single message's serialization.
type pySerializationMessageData struct {
	TypeName      string // e.g., "TeamRoster"
	FuncPrefix    string // e.g., "team_roster"
	QualifiedType string // e.g., "baseball_pb2.TeamRoster"
}

// pySerializationData holds the template data for all serialization functions.
type pySerializationData struct {
	Messages []pySerializationMessageData
}

// writeSerialization writes serialization functions to the combined helpers.py builder
func writeSerialization(b *strings.Builder, pf model.ProtoFile, protoFilename string) error {
	if len(pf.Messages) == 0 {
		return nil
	}

	var messages []pySerializationMessageData
	for _, msg := range pf.Messages {
		messages = append(messages, pySerializationMessageData{
			TypeName:      msg.Name,
			FuncPrefix:    strutil.ToSnakeCase(msg.Name),
			QualifiedType: fmt.Sprintf("%s_pb2.%s", protoFilename, msg.Name),
		})
	}

	data := pySerializationData{
		Messages: messages,
	}

	typeFS, err := fs.Sub(templates.FS, "python/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	result, err := tmplutil.ExecuteTemplate(typeFS, "serialization.py.tmpl", tmplutil.BaseFuncMap(), data)
	if err != nil {
		return fmt.Errorf("failed to execute serialization template: %w", err)
	}

	b.Write(result)
	return nil
}
