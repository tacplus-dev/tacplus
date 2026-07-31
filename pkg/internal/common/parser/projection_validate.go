// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"sort"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// projectionAnnotations are the message-level markers that make a record a graph
// node, a timeseries table, or an extraction-projection source — each of which
// requires graph/timeseries/extraction codegen (Open/Refine/Sample/Pull over the
// projected record) in a full-service target.
var projectionAnnotations = []string{
	"graph_type",
	"timeseries_type",
	"graph_type_source",
	"timeseries_table_source",
}

// projectionSupportedLanguages lists the FULL-SERVICE targets whose graph /
// timeseries / extraction projection codegen tacplus generates. A record that
// declares one of these projections must NOT build for a full-service language
// absent here: silently emitting nothing would ship a store that never
// materializes the projection the annotation asks for (contrast redact, which is
// a leak; this is a silent missing-surface). Add a full-service language only once
// its graph/timeseries/extraction emitters and tests land.
var projectionSupportedLanguages = map[string]bool{
	"go":   true,
	"rust": true,
}

// projectionClientOnlyLanguages lists the client-only targets. They reject
// record-store / service generation, so they never host a projection — they only
// query already-materialized records via the RecordMap client surface. A
// projection annotation is therefore a no-op for them, not a missing surface, so
// they build without projection codegen.
var projectionClientOnlyLanguages = map[string]bool{
	"python":     true,
	"typescript": true,
	"swift":      true,
	"csharp":     true,
}

// ValidateProjectionLanguageSupport fails the build LOUDLY when a record in the
// SELF service declares a graph / timeseries / extraction projection but the
// target is a full-service language with no projection generator (java today).
// Fail-loud, never silently emit nothing: a store-hosting language that can't
// materialize the projection refuses rather than shipping a record surface the
// annotation was meant to produce. Client-only targets pass — they cannot host a
// store, so there is no projection surface to materialize.
//
// Scoped to selfPackage: a projection is hosted only by the service that OWNS the
// record, so a projection record in an imported/ambient dependency (which this
// build merely references as a client) must not fail this build. Contrast
// ValidateRedactLanguageSupport, which scans all files because a redact field
// leaks wherever its egress surface is emitted; a projection that isn't emitted
// here is simply not this service's concern.
func ValidateProjectionLanguageSupport(allFiles []model.ProtoFile, selfPackage, language string) error {
	if projectionSupportedLanguages[language] || projectionClientOnlyLanguages[language] {
		return nil
	}
	offenders := make(map[string]bool)
	for _, pf := range allFiles {
		if pf.Package != selfPackage {
			continue
		}
		for _, msg := range pf.Messages {
			for _, ann := range projectionAnnotations {
				if _, ok := msg.Annotations[ann]; ok {
					offenders[msg.Name] = true
					break
				}
			}
		}
	}
	if len(offenders) == 0 {
		return nil
	}
	names := make([]string, 0, len(offenders))
	for n := range offenders {
		names = append(names, n)
	}
	sort.Strings(names)
	return fmt.Errorf(
		"graph/timeseries/extraction projection is declared on %s but projection codegen is not implemented for target language %q; "+
			"tacplus refuses to generate a store that silently omits the projected record surface — implement projection codegen for %q or remove the annotation",
		strings.Join(names, ", "), language, language)
}
