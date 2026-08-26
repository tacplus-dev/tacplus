// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// CollectServingLabels returns every routing label served by the loaded proto
// set: a service's own identity (GrpcName, the proto package), the physical host
// a vservice declares through (spec.service_s2s).label, and the file-level
// identity of a service that declares itself a backing store through
// (spec.service).store — the way a store service says which label it serves. A
// routing label is late-bound — it names a service by label, not by import — so
// this set is the only thing that can tell a declared destination from a typo.
func CollectServingLabels(fds []*FileDescriptor) map[string]bool {
	labels := make(map[string]bool)
	for _, fd := range fds {
		if fd.ServiceStore {
			if fd.ServiceName != "" {
				labels[fd.ServiceName] = true
			}
			if fd.Package != "" {
				labels[fd.Package] = true
			}
		}
		for _, svc := range fd.Services {
			if svc.GrpcName != "" {
				labels[svc.GrpcName] = true
			}
			if svc.HostLabel != "" {
				labels[svc.HostLabel] = true
			}
		}
	}
	return labels
}

// ValidateRoutingLabels enforces that every routing label declared by a state
// root or a durable store resolves to a service in the loaded proto set. A
// record that declares a label nothing serves routes nowhere; without this check
// that is silent at build time and invisible at runtime.
//
// (spec.service_state).label and (spec.service_store).label are repeated: a
// record may land in more than one destination. ALL declared labels must be
// served — a two-label record with one good label and one typo is still a record
// half of whose destinations do not exist, which is exactly the failure this
// check exists to make impossible.
//
// The equivalent check for (spec.service_event).label lives in
// eventbus.BuildTopicRegistry, which needs the same label set to resolve topics
// and validates it there against CollectServingLabels.
func ValidateRoutingLabels(dp *DescriptorParser) error {
	all := dp.GetAllFiles()
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	fds := make([]*FileDescriptor, 0, len(all))
	for _, path := range paths {
		fds = append(fds, all[path])
	}
	return validateRoutingLabels(fds)
}

// validateRoutingLabels is the file-list form of ValidateRoutingLabels; fds must
// already be in a deterministic order so the error text is stable.
func validateRoutingLabels(fds []*FileDescriptor) error {
	served := CollectServingLabels(fds)

	var errs []string
	for _, fd := range fds {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			for _, ann := range []struct {
				key    string
				option string
			}{
				{"service_state_label", "spec.service_state"},
				{"service_store_label", "spec.service_store"},
			} {
				val, ok := msg.Annotations[ann.key]
				if !ok {
					continue
				}
				for _, label := range val.StringSliceValue {
					if served[label] {
						continue
					}
					errs = append(errs, fmt.Sprintf(
						"%s.%s declares (%s).label %q but no loaded service serves that label",
						fd.Package, msg.Name, ann.option, label))
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("routing label errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
