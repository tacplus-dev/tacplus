// Copyright (c) 2026 Second Star Cavalier LLC

package llmdesc

import (
	"encoding/json"
	"sort"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// RegistryTypeInfo holds pre-computed data for a single message type
// exposed through the always-on top-level registry.
type RegistryTypeInfo struct {
	Package    string
	TypeName   string
	FQN        string
	TypeDir    string
	SchemaJSON string
	Descriptor TypeDescriptor
	// EventDB
	RecordKey   string // event key field from (spec.record).key
	RecordOwner string // owner field from (spec.record).owner
	// Transformations declared on this message, from (spec.transformation).
	Transformations []model.TransformationEntry
	// timeseries_type is the generator-internal compile target a transformation
	// emits. Store-neutral: dedup is semantic intent, not backend DDL.
	TimeseriesType        bool                    // true if message has (spec.timeseries_type)
	TimeseriesTableName   string                  // table name (or snake_case of message name)
	TimeseriesTableSource string                  // source message FQN
	TimeseriesDedup       *model.DedupPolicyEntry // store-neutral dedup policy (nil = none)
	// Durable service_store metadata
	ServiceStore       bool
	ServiceStoreName   string
	ServiceStoreLabels []string
}

// CollectRegistryTypes scans allFiles for all message types and returns their
// pre-built descriptors sorted deterministically.
func CollectRegistryTypes(allFiles []model.ProtoFile) []RegistryTypeInfo {
	msgIndex := BuildMessageIndex(allFiles)
	enumIndex := BuildEnumIndex(allFiles)

	seen := make(map[string]bool)
	var types []RegistryTypeInfo

	for _, pf := range allFiles {
		for _, msg := range pf.Messages {
			fqn := pf.Package + "." + msg.Name
			if seen[fqn] {
				continue
			}
			seen[fqn] = true

			desc := BuildTypeDescriptor(msg, pf.Package, msgIndex, enumIndex, make(map[string]bool))
			jsonBytes, err := json.MarshalIndent(desc, "", "  ")
			if err != nil {
				continue
			}

			typeDir := "services"
			relNorm := strings.ReplaceAll(pf.RelPath, "\\", "/")
			if strings.HasPrefix(relNorm, "sys/") {
				typeDir = "sys"
			}

			rti := RegistryTypeInfo{
				Package:    pf.Package,
				TypeName:   msg.Name,
				FQN:        fqn,
				TypeDir:    typeDir,
				SchemaJSON: string(jsonBytes),
				Descriptor: desc,
			}

			// Extract record/projection annotations from message-level annotations.
			if msg.Annotations != nil {
				// EventDB annotations. A record's key is its declared (spec.record).key
				// or model.DefaultRecordKeyField — the ONE canonical default shared with
				// the RecordMap query plane and projection generators. The registry emits
				// the resolved key, never an empty string, so consumers never have to
				// hardcode the generator-internal default.
				if ann, ok := msg.Annotations["record"]; ok && ann.BoolValue {
					rti.RecordKey = model.DefaultRecordKeyField
					if k, ok := msg.Annotations["record_key"]; ok && k.StringValue != "" {
						rti.RecordKey = k.StringValue
					}
				}
				if ann, ok := msg.Annotations["record_owner"]; ok && ann.StringValue != "" {
					rti.RecordOwner = ann.StringValue
				}
				// Transformation annotations
				if ann, ok := msg.Annotations["transformation"]; ok {
					rti.Transformations = ann.TransformationEntries
				}
				// Timeseries table annotations (store-neutral)
				if _, ok := msg.Annotations["timeseries_type"]; ok {
					rti.TimeseriesType = true
					if ann, ok := msg.Annotations["timeseries_table_name"]; ok && ann.StringValue != "" {
						rti.TimeseriesTableName = ann.StringValue
					}
					if ann, ok := msg.Annotations["timeseries_table_source"]; ok && ann.StringValue != "" {
						rti.TimeseriesTableSource = ann.StringValue
					}
					if ann, ok := msg.Annotations["timeseries_dedup"]; ok && ann.TimeseriesDedup != nil {
						rti.TimeseriesDedup = ann.TimeseriesDedup
					}
				}
				if ann, ok := msg.Annotations["service_store"]; ok {
					rti.ServiceStore = true
					rti.ServiceStoreName = ann.StringValue
				}
				if ann, ok := msg.Annotations["service_store_label"]; ok && len(ann.StringSliceValue) > 0 {
					rti.ServiceStoreLabels = ann.StringSliceValue
				}
			}

			types = append(types, rti)
		}
	}

	sort.Slice(types, func(i, j int) bool {
		return types[i].FQN < types[j].FQN
	})

	return types
}
