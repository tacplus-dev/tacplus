// Copyright (c) 2026 Second Star Cavalier LLC

package boundaries

import (
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

func TestIsTraceEventPublisherDataRequiresCanonicalFQN(t *testing.T) {
	if !IsTraceEventPublisherData(EventPublisherData{EventTypeFQN: TraceEventTypeFQN, EventTypeName: "TraceEvent"}) {
		t.Fatal("canonical trace.TraceEvent publisher should be recognized")
	}
	if IsTraceEventPublisherData(EventPublisherData{EventTypeFQN: "audit.TraceEvent", EventTypeName: "TraceEvent"}) {
		t.Fatal("bare TraceEvent simple name must not be enough to select the trace publisher")
	}
}

func TestActorOwnsFileByDeclaredServiceName(t *testing.T) {
	fd := &parser.FileDescriptor{
		Package:     "contractclient",
		ServiceName: "contract",
	}
	if !actorOwnsFile(fd, "contract") {
		t.Fatal("file-level (spec.service).name must establish actor ownership when proto package differs")
	}
}

// PC-C-6: actorOwnsFile is the ownership filter that keeps an IMPORTED topic
// proto from turning the importer into a producer. An event_publish/subscribe
// annotation only binds when the file is owned — same proto package, declared
// (spec.service).name, or a hosting vservice tag — never merely because the
// actor imports the file.
func TestActorOwnsFileRejectsImportedForeignProto(t *testing.T) {
	// A foreign topic proto: different package, its own unrelated service, no
	// vservice host tag matching the importer.
	imported := &parser.FileDescriptor{
		Package:     "eventbus",
		ServiceName: "eventbus",
		Services:    []parser.ServiceDescriptor{{Name: "EventBusService", HostTag: ""}},
	}
	if actorOwnsFile(imported, "contract") {
		t.Fatal("importing a foreign topic proto must NOT make the importer its producer/owner")
	}
}

// The selfPackage=="" fallback is owns-all: when the caller does not scope to a
// package (whole-build passes), every file is in scope. Pinned so a change to
// the fallback is caught.
func TestActorOwnsFileEmptySelfPackageOwnsAll(t *testing.T) {
	foreign := &parser.FileDescriptor{Package: "somethingelse", ServiceName: "other"}
	if !actorOwnsFile(foreign, "") {
		t.Fatal("an empty selfPackage is the owns-all whole-build fallback")
	}
}

// A vservice hosted BY the actor is owned via the host tag, even though the
// proto package differs — the complement of the imported-foreign case.
func TestActorOwnsFileByVserviceHostTag(t *testing.T) {
	hosted := &parser.FileDescriptor{
		Package:  "topics",
		Services: []parser.ServiceDescriptor{{Name: "MyTopics", HostTag: "contract"}},
	}
	if !actorOwnsFile(hosted, "contract") {
		t.Fatal("a vservice whose host tag is the actor must be owned")
	}
}
