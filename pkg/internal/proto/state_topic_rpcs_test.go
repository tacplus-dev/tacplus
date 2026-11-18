// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// StateTopicRPCSurface is what a state client reads to know which topic RPCs it
// may call (#454): the Go state client called RPCs whose shape and presence it
// guessed, and did not compile. It must agree with what buildStateService
// actually emits, for every combination of declared surface and export.
func TestStateTopicRPCSurfaceMatchesEmittedStateService(t *testing.T) {
	cases := []struct {
		name     string
		entry    *model.EventBusBindingEntry // nil: the payload declares no surface
		export   bool
		wantPub  bool
		wantSubs bool
	}{
		{name: "undeclared", wantPub: true, wantSubs: true},
		{name: "undeclared exported", export: true, wantPub: true, wantSubs: true},
		{name: "s2s declared", entry: &model.EventBusBindingEntry{MethodS2S: &model.EventBusMethodS2S{AuthServices: []string{"demo"}}}, wantPub: true, wantSubs: true},
		{name: "s2s declared exported", entry: &model.EventBusBindingEntry{MethodS2S: &model.EventBusMethodS2S{AuthServices: []string{"demo"}}}, export: true, wantPub: true, wantSubs: true},
		{name: "c2s declared", entry: &model.EventBusBindingEntry{MethodC2S: &model.EventBusMethodC2S{}}, wantPub: true, wantSubs: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stateRoot := topicStateRootForTest()
			stateRoot.Fields[0].Type = "demo.DemoEvent"
			if tc.export {
				stateRoot.Annotations = map[string]model.AnnotationValue{
					"service_state_export": {Type: model.AnnotationTypeBool, BoolValue: true},
				}
			}
			event := parser.MessageDescriptor{Name: "DemoEvent"}
			if tc.entry != nil {
				event.Annotations = map[string]model.AnnotationValue{
					"event_surface": {EventPublishEntries: []model.EventBusBindingEntry{*tc.entry}},
				}
			}
			files := map[string]*parser.FileDescriptor{
				"demo.proto": {Package: "demo", Messages: []parser.MessageDescriptor{event, *stateRoot}},
			}

			got := StateTopicRPCSurface(files, stateRoot)["events"]
			if got.Publish != tc.wantPub || got.Subscribe != tc.wantSubs {
				t.Fatalf("StateTopicRPCSurface = %+v, want publish=%v subscribe=%v", got, tc.wantPub, tc.wantSubs)
			}

			emitted := buildStateService(files["demo.proto"], stateRoot, stateRoot.Name,
				nil, []string{"demo"}, collectStateEventSurfaces(files), nil).serviceProto
			if has := strings.Contains(emitted, "rpc PublishToEvents("); has != got.Publish {
				t.Fatalf("emitted PublishToEvents=%v, surface says %v:\n%s", has, got.Publish, emitted)
			}
			if has := strings.Contains(emitted, "rpc SubscribeToEvents("); has != got.Subscribe {
				t.Fatalf("emitted SubscribeToEvents=%v, surface says %v:\n%s", has, got.Subscribe, emitted)
			}
		})
	}
}

// A private topic gets no RPC, so it has no entry.
func TestStateTopicRPCSurfaceOmitsPrivateTopic(t *testing.T) {
	stateRoot := topicStateRootForTest()
	stateRoot.Fields[0].Annotations["private"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	if got := StateTopicRPCSurface(nil, stateRoot); len(got) != 0 {
		t.Fatalf("private topic must have no RPC surface, got %+v", got)
	}
}
