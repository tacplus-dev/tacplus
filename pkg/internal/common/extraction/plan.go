// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

// Package extraction resolves (spec.timeseries_type).source projections into
// the language-neutral facts every extraction helper is generated from: which
// projection is drawn from which source message, which source field feeds each
// projection field, and which of those sit behind a oneof variant.
//
// A projection field names its source with (spec.projection_source): a bare
// field name reads the source message directly; "variant.field" reads a field
// of the message held by the source's oneof member "variant", and makes the
// extraction conditional on that member being set. The per-language generators
// map these facts to their own names and accessors and nothing else.
package extraction

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// Field is one projection field and the source field that feeds it.
type Field struct {
	Projection model.Field // the field on the projection message
	SourceName string      // field name on the receiver: the source message, or the variant's message
	// Source is SourceName resolved on its receiver; Resolved is false when the
	// receiver does not declare it (the generated accessor then fails to compile
	// in every language, naming the field).
	Source   model.Field
	Resolved bool
}

// Variant groups the fields read from one oneof member of the source.
type Variant struct {
	Name   string // the oneof member's field name, e.g. "tool_use"
	Fields []Field
}

// Projection is one message extracted from a source message.
type Projection struct {
	Message model.Message
	File    model.ProtoFile
	// Fields are the projection fields read from the source message itself,
	// in declaration order.
	Fields []Field
	// Variants are the oneof members read, in first-use order. A projection
	// with any is conditional: it is extracted only when one of them is set.
	Variants []Variant
}

// Group is every projection one package draws from one source message.
type Group struct {
	Source      model.Message
	SourceFile  model.ProtoFile
	Projections []Projection // in proto file and declaration order
}

// File is the proto file of the group's first projection, which places the
// group's generated output.
func (g Group) File() model.ProtoFile { return g.Projections[0].File }

// SourceFQN returns the source message FQN a projection declares through
// (spec.timeseries_type).source, or "" when it declares none.
func SourceFQN(msg model.Message) string {
	if ann, ok := msg.Annotations["timeseries_table_source"]; ok && ann.StringValue != "" {
		return ann.StringValue
	}
	return ""
}

// FindMessage locates a top-level message by its FQN ("pkg.Name"; the package
// is the first segment).
func FindMessage(allFiles []model.ProtoFile, fqn string) (model.Message, model.ProtoFile, bool) {
	pkg, name, ok := strings.Cut(fqn, ".")
	if !ok {
		return model.Message{}, model.ProtoFile{}, false
	}
	for _, pf := range allFiles {
		if pf.Package != pkg {
			continue
		}
		for _, msg := range pf.Messages {
			if msg.Name == name {
				return msg, pf, true
			}
		}
	}
	return model.Message{}, model.ProtoFile{}, false
}

// Plan resolves every projection in allFiles, grouped by (source, projection
// package) in first-seen order. A projection whose source message is not in
// allFiles is an error: there is nothing to extract it from.
func Plan(allFiles []model.ProtoFile) ([]Group, error) {
	type key struct{ sourceFQN, projPkg string }
	index := make(map[key]int)
	var groups []Group

	for _, pf := range allFiles {
		if strings.HasPrefix(pf.RelPath, "google/") {
			continue
		}
		for _, msg := range pf.Messages {
			sourceFQN := SourceFQN(msg)
			if sourceFQN == "" {
				continue
			}
			sourceMsg, sourcePf, ok := FindMessage(allFiles, sourceFQN)
			if !ok {
				return nil, fmt.Errorf("extraction source %q for projection %s.%s not found", sourceFQN, pf.Package, msg.Name)
			}
			proj := planProjection(allFiles, msg, pf, sourceMsg, sourcePf)

			k := key{sourceFQN: sourceFQN, projPkg: pf.Package}
			if i, ok := index[k]; ok {
				groups[i].Projections = append(groups[i].Projections, proj)
				continue
			}
			index[k] = len(groups)
			groups = append(groups, Group{Source: sourceMsg, SourceFile: sourcePf, Projections: []Projection{proj}})
		}
	}
	return groups, nil
}

func planProjection(allFiles []model.ProtoFile, msg model.Message, pf model.ProtoFile, sourceMsg model.Message, sourcePf model.ProtoFile) Projection {
	proj := Projection{Message: msg, File: pf}
	variantIdx := make(map[string]int)
	for _, f := range msg.Fields {
		ann, ok := f.Annotations["projection_source"]
		if !ok || ann.StringValue == "" {
			continue
		}
		variant, inner, isVariant := strings.Cut(ann.StringValue, ".")
		if !isVariant {
			src, resolved := findField(sourceMsg, ann.StringValue)
			proj.Fields = append(proj.Fields, Field{Projection: f, SourceName: ann.StringValue, Source: src, Resolved: resolved})
			continue
		}
		field := Field{Projection: f, SourceName: inner}
		if member, ok := findField(sourceMsg, variant); ok && member.IsMessage {
			pkg := member.TypePackage
			if pkg == "" {
				pkg = sourcePf.Package
			}
			if variantMsg, _, ok := FindMessage(allFiles, pkg+"."+member.Type); ok {
				field.Source, field.Resolved = findField(variantMsg, inner)
			}
		}
		i, ok := variantIdx[variant]
		if !ok {
			i = len(proj.Variants)
			variantIdx[variant] = i
			proj.Variants = append(proj.Variants, Variant{Name: variant})
		}
		proj.Variants[i].Fields = append(proj.Variants[i].Fields, field)
	}
	return proj
}

func findField(msg model.Message, name string) (model.Field, bool) {
	for _, f := range msg.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return model.Field{}, false
}
