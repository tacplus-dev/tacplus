// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Package coverage is the kitchensink coverage check (tacplus#196). A tacplus
// release is formally verified and fuzzed once, on the kitchensink
// (cross-test/kitchensink); the coverage manifest records which generator
// features its members use, in which languages. `tacplus build` compares what
// a service uses with it and refuses a feature the kitchensink does not cover.
//
// A feature is a spec option a proto sets, at the level it sets it, with the
// enum values it picks ("method/rpc.principal=RPC_PRINCIPAL_USER"), the
// combination of the enum values set together in one option
// ("method/rpc{exposure=...,principal=...}", the call surface of an RPC), and
// the shapes the generator emits differently: field kinds and cardinalities,
// oneofs, and streaming RPCs.
package coverage

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"gopkg.in/yaml.v3"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// ManifestPath is the coverage manifest, relative to the repository root.
// `just kitchensink-manifest` (cmd/kitchensinkmanifest) rewrites it.
const ManifestPath = "pkg/coverage/kitchensink.json"

// MembersPath lists the kitchensink's members, relative to the repository root.
const MembersPath = "cross-test/kitchensink/members.yaml"

//go:embed kitchensink.json
var releaseManifest []byte

// Manifest maps each covered feature to the languages a kitchensink member
// generates it in.
type Manifest struct {
	Features map[string][]string `json:"features"`
}

// Release returns the manifest this tacplus was built with.
func Release() (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(releaseManifest, &m); err != nil {
		return nil, fmt.Errorf("coverage manifest: %w", err)
	}
	return &m, nil
}

// Marshal renders the manifest as the checked-in file: sorted, indented, with
// a trailing newline.
func (m *Manifest) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Uncovered returns, sorted, the features in used that no member covers in lang.
func (m *Manifest) Uncovered(lang string, used []string) []string {
	var out []string
	for _, f := range used {
		if !slices.Contains(m.Features[f], lang) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// Member is one entry of members.yaml.
type Member struct {
	Path      string   `yaml:"path"`
	Languages []string `yaml:"languages"`
	Gate      string   `yaml:"gate"`
}

// Compute builds the manifest from the members listed in members.yaml under root.
func Compute(root string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, MembersPath))
	if err != nil {
		return nil, err
	}
	var doc struct {
		Members []Member `yaml:"members"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", MembersPath, err)
	}
	langs := map[string]map[string]bool{}
	for _, mem := range doc.Members {
		if len(mem.Languages) == 0 || mem.Gate == "" {
			return nil, fmt.Errorf("%s: member %s needs languages and a gate", MembersPath, mem.Path)
		}
		features, err := memberFeatures(filepath.Join(root, mem.Path))
		if err != nil {
			return nil, fmt.Errorf("member %s: %w", mem.Path, err)
		}
		for _, f := range features {
			if langs[f] == nil {
				langs[f] = map[string]bool{}
			}
			for _, l := range mem.Languages {
				langs[f][l] = true
			}
		}
	}
	m := &Manifest{Features: map[string][]string{}}
	for f, set := range langs {
		for l := range set {
			m.Features[f] = append(m.Features[f], l)
		}
		sort.Strings(m.Features[f])
	}
	return m, nil
}

// memberFeatures prepares a member's protos as `tacplus build` does and
// returns the features they use.
func memberFeatures(dir string) ([]string, error) {
	tmp, err := os.MkdirTemp("", "tacplus-coverage-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	flat, err := proto.CopyAndPrepareProtoDir(dir, tmp)
	if err != nil {
		return nil, err
	}
	return ProtoDirFeatures(flat)
}

// ProtoDirFeatures returns, sorted, the features the protos under a prepared
// (flat) proto dir use. The sys/ and generated/ protos tacplus adds to every
// build are not the service's and are not counted.
func ProtoDirFeatures(flatProtoDir string) ([]string, error) {
	dp, err := parser.NewDescriptorParser(flatProtoDir)
	if err != nil {
		return nil, err
	}
	raw := dp.RawFileDescriptors()
	set := &descriptorpb.FileDescriptorSet{}
	for _, name := range slices.Sorted(maps.Keys(raw)) {
		set.File = append(set.File, raw[name])
	}
	files, err := protodesc.NewFiles(set)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		path := fd.Path()
		if proto.IsSysProto(path) || proto.IsGeneratedProto(path) || strings.HasPrefix(path, "google/") {
			return true
		}
		walkFile(fd, seen)
		return true
	})
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out, nil
}

func walkFile(fd protoreflect.FileDescriptor, seen map[string]bool) {
	addOptions("file", fd.Options(), seen)
	walkMessages(fd.Messages(), seen)
	walkEnums(fd.Enums(), seen)
	for i := 0; i < fd.Services().Len(); i++ {
		sd := fd.Services().Get(i)
		addOptions("service", sd.Options(), seen)
		for j := 0; j < sd.Methods().Len(); j++ {
			md := sd.Methods().Get(j)
			addOptions("method", md.Options(), seen)
			switch {
			case md.IsStreamingClient() && md.IsStreamingServer():
				seen["method/streaming=bidi"] = true
			case md.IsStreamingClient():
				seen["method/streaming=client"] = true
			case md.IsStreamingServer():
				seen["method/streaming=server"] = true
			}
		}
	}
}

func walkMessages(msgs protoreflect.MessageDescriptors, seen map[string]bool) {
	for i := 0; i < msgs.Len(); i++ {
		md := msgs.Get(i)
		if md.IsMapEntry() {
			continue
		}
		addOptions("message", md.Options(), seen)
		for j := 0; j < md.Fields().Len(); j++ {
			addField(md.Fields().Get(j), seen)
		}
		for j := 0; j < md.Oneofs().Len(); j++ {
			od := md.Oneofs().Get(j)
			if od.IsSynthetic() {
				continue
			}
			seen["field/oneof"] = true
			addOptions("oneof", od.Options(), seen)
		}
		walkMessages(md.Messages(), seen)
		walkEnums(md.Enums(), seen)
	}
}

func walkEnums(enums protoreflect.EnumDescriptors, seen map[string]bool) {
	for i := 0; i < enums.Len(); i++ {
		ed := enums.Get(i)
		addOptions("enum", ed.Options(), seen)
		for j := 0; j < ed.Values().Len(); j++ {
			addOptions("enum_value", ed.Values().Get(j).Options(), seen)
		}
	}
}

func addField(fd protoreflect.FieldDescriptor, seen map[string]bool) {
	addOptions("field", fd.Options(), seen)
	switch {
	case fd.IsMap():
		seen["field/map<"+fd.MapKey().Kind().String()+","+fd.MapValue().Kind().String()+">"] = true
		return
	case fd.IsList():
		seen["field/repeated "+fd.Kind().String()] = true
		return
	case fd.HasOptionalKeyword():
		seen["field/optional "+fd.Kind().String()] = true
		return
	}
	seen["field/"+fd.Kind().String()] = true
}

// addOptions records each spec option set on opts, at level.
func addOptions(level string, opts protoreflect.ProtoMessage, seen map[string]bool) {
	if opts == nil {
		return
	}
	opts.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if !fd.IsExtension() || fd.ParentFile().Package() != "spec" {
			return true
		}
		addValue(level+"/"+string(fd.Name()), fd, v, seen)
		return true
	})
}

// addValue records the option at path and, for a message, every field it sets
// and the combination of its singular enum fields.
func addValue(path string, fd protoreflect.FieldDescriptor, v protoreflect.Value, seen map[string]bool) {
	seen[path] = true
	switch {
	case fd.Kind() == protoreflect.EnumKind:
		for _, n := range enumNames(fd, v) {
			seen[path+"="+n] = true
		}
	case fd.Kind() == protoreflect.MessageKind && !fd.IsMap():
		var msgs []protoreflect.Message
		if fd.IsList() {
			for i := 0; i < v.List().Len(); i++ {
				msgs = append(msgs, v.List().Get(i).Message())
			}
		} else {
			msgs = append(msgs, v.Message())
		}
		for _, m := range msgs {
			var combo []string
			m.Range(func(sub protoreflect.FieldDescriptor, sv protoreflect.Value) bool {
				addValue(path+"."+string(sub.Name()), sub, sv, seen)
				if sub.Kind() == protoreflect.EnumKind && !sub.IsList() {
					combo = append(combo, string(sub.Name())+"="+enumNames(sub, sv)[0])
				}
				return true
			})
			if len(combo) > 1 {
				sort.Strings(combo)
				seen[path+"{"+strings.Join(combo, ",")+"}"] = true
			}
		}
	}
}

func enumNames(fd protoreflect.FieldDescriptor, v protoreflect.Value) []string {
	name := func(n protoreflect.EnumNumber) string {
		if ev := fd.Enum().Values().ByNumber(n); ev != nil {
			return string(ev.Name())
		}
		return fmt.Sprint(n)
	}
	if fd.IsList() {
		var out []string
		for i := 0; i < v.List().Len(); i++ {
			out = append(out, name(v.List().Get(i).Enum()))
		}
		return out
	}
	return []string{name(v.Enum())}
}
