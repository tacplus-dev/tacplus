// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/llmdesc"
)

// NEW-20 residue: pin the timeseries table name at its CALL SITE.
//
// A timeseries table name is derived from the message name whenever
// (spec.timeseries_type) declares no explicit name. CON-3 replaced a local naive
// converter with strutil.ToSnakeCase, which changed the derived name for any
// type with an acronym: HTTPMetric went from h_t_t_p_metric to http_metric.
//
// That name is PERSISTED -- it is the table the store actually writes to. A
// change to it does not fail anything at build time: generation succeeds, the
// service starts, and it quietly begins writing to a NEW table while the existing
// one stops receiving rows and is left orphaned. There is no compile error and no
// runtime error to notice.
//
// strutil.ToSnakeCase has its own unit tests, but those do not pin this call
// site: swapping this derivation back to a local converter, or to any other
// casing helper, would leave every strutil test green. Assert the derived name
// that is actually emitted.
func TestTimeseriesTableNameDerivationIsPinned(t *testing.T) {
	cases := []struct {
		typeName string
		want     string
	}{
		// The acronym cases are the ones CON-3 changed, and the reason this
		// pin exists.
		{"HTTPMetric", "http_metric"},
		{"OpenAIUsage", "open_ai_usage"},
		{"LLMEvent", "llm_event"},
		// Ordinary names, unchanged by CON-3, included so the pin also catches a
		// converter swap that only breaks the simple cases.
		{"RequestLatency", "request_latency"},
		{"Metric", "metric"},
	}

	for _, c := range cases {
		info := llmdesc.RegistryTypeInfo{
			TimeseriesType: true,
			Descriptor:     llmdesc.TypeDescriptor{Name: c.typeName},
		}
		got := BuildTypeInfoProto(info).GetTimeseriesTableName()
		if got != c.want {
			t.Errorf("timeseries table name for %q = %q, want %q\n"+
				"This name is the PERSISTED table. Changing it orphans the existing "+
				"table and silently starts writing to a new one, with nothing failing "+
				"at build or run time. If this change is intended, it needs a migration, "+
				"not just a passing test.", c.typeName, got, c.want)
		}
	}
}

// An explicit (spec.timeseries_type).name must win over the derived one --
// otherwise the escape hatch for pinning a legacy table name does not work, and
// there would be no way to keep an existing table after a converter change.
func TestExplicitTimeseriesTableNameOverridesDerivation(t *testing.T) {
	info := llmdesc.RegistryTypeInfo{
		TimeseriesType:      true,
		TimeseriesTableName: "h_t_t_p_metric",
		Descriptor:          llmdesc.TypeDescriptor{Name: "HTTPMetric"},
	}
	if got := BuildTypeInfoProto(info).GetTimeseriesTableName(); got != "h_t_t_p_metric" {
		t.Errorf("explicit table name = %q, want it to override the derived name; "+
			"without this there is no way to keep a pre-CON-3 table name", got)
	}
}
