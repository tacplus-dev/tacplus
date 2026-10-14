// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/verbs"
)

// ValidateArtifactOnWrite is the build-time guard on PLAN.md's knowledge/
// artifact invariant: a write path that reaches "the living side" — a
// user-owned/user-facing state root, marked with
// (spec.service_state).requires_artifact_on_write — must leave an artifact
// (a (spec.record)-annotated message) somewhere in its package, or the build
// fails.
//
// State mutation is not declared per-RPC: a service declares a state root and
// tacplus generates a state client, but which handler writes through it is
// hand-written application code the generator never sees. So this rule
// correlates at the coarsest level tacplus CAN see at generation time: the
// package. Within a package that declares an opted-in state root, every
// non-synthesized service's RPC that the closed verb registry
// (internal/common/verbs) classifies Access=WRITE is a candidate write path
// to that root. If the package declares at least one (spec.record) message,
// the artifact requirement is satisfied (tasks.TaskActivity alongside
// tasks.TasksState is the reference shape). If it declares zero, every write
// RPC in the package is reported as reaching the living side with no
// artifact.
//
// This is deliberately coarser than "this specific RPC's mutation has no
// artifact" — tacplus cannot see the handler body that decides whether a
// given RPC path actually calls the record's publish method. The coarse form
// still catches the real violation the acceptance sentence describes: a
// service that owns a living-side root, has WRITE RPCs, and emits no record
// at all.
//
// Only messages classified as (spec.record) count as artifacts; declaring a
// service_event without a record (S2S-only, no durable row in stratum) does
// not satisfy this rule, because (spec.record) is what lands the row in
// stratum (spec.proto's E_Record doc).
func ValidateArtifactOnWrite(inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for artifact-on-write validation: %w", err)
	}

	type pkgFacts struct {
		hasRecord  bool
		stateRoots []string // opted-in root names, for the error message
		writeRPCs  []string // "Service.Method" for every candidate write path
	}
	pkgs := make(map[string]*pkgFacts)

	factsFor := func(pkg string) *pkgFacts {
		f, ok := pkgs[pkg]
		if !ok {
			f = &pkgFacts{}
			pkgs[pkg] = f
		}
		return f
	}

	for _, fileDesc := range dp.GetAllFiles() {
		f := factsFor(fileDesc.Package)

		for _, msg := range fileDesc.Messages {
			if _, isState := msg.Annotations["service_state"]; isState {
				if ann, ok := msg.Annotations["service_state_requires_artifact_on_write"]; ok && ann.BoolValue {
					f.stateRoots = append(f.stateRoots, msg.Name)
				}
			}
			if rec, ok := msg.Annotations["record"]; ok && rec.BoolValue {
				f.hasRecord = true
			}
		}

		for _, svc := range fileDesc.Services {
			// Synthesized services (state/store/recordmap/opaeval) are the
			// generated plumbing to the root itself, not the hand-written
			// business surface the acceptance sentence is about.
			if svc.Synthesized {
				continue
			}
			for _, m := range svc.Methods {
				mode := ""
				if m.Collection != nil {
					mode = m.Collection.Mode
				}
				class, err := verbs.Classify(verbs.MethodShape{
					Name:            m.Name,
					ClientStreaming: m.ClientStreaming,
					ServerStreaming: m.ServerStreaming,
					CollectionMode:  mode,
				})
				if err != nil {
					// Unclassifiable names are somebody else's problem (the
					// verb-registry gate); this validator only cares about
					// RPCs it can confidently call WRITE.
					continue
				}
				if class.Access == verbs.AccessWrite {
					f.writeRPCs = append(f.writeRPCs, svc.Name+"."+m.Name)
				}
			}
		}
	}

	var errs []string
	for pkg, f := range pkgs {
		if len(f.stateRoots) == 0 || len(f.writeRPCs) == 0 || f.hasRecord {
			continue
		}
		sort.Strings(f.stateRoots)
		sort.Strings(f.writeRPCs)
		errs = append(errs, fmt.Sprintf(
			"package %s: state root(s) %s require an artifact on write "+
				"((spec.service_state).requires_artifact_on_write), but WRITE RPC(s) %s "+
				"reach it with no (spec.record) message declared anywhere in package %s — "+
				"every write path reaching the living side must emit an artifact",
			pkg, strings.Join(f.stateRoots, ", "), strings.Join(f.writeRPCs, ", "), pkg))
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("artifact-on-write validation failed:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
