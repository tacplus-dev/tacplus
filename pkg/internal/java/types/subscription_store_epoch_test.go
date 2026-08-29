// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// The paired control for BIG GOAL 4 P4a's stamping seam. A record has no write
// RPC — it reaches a store on the store's EventBus subscription — so the
// subscription dispatch is where the store's clock is stamped, and a record type
// declaring (spec.store_epoch) must be stamped there before the handler sees it.
// A type declaring none must render byte-identically to before.
func TestSubscriptionStampsTheDeclaredStoreEpoch(t *testing.T) {
	stamped := javaSubscriptionEntry{
		FieldPascal: "EventStream", FieldCamel: "eventStream",
		HandlerMethodName: "onEventStreamAgentsensorAgentEventBusEventStream",
		SourcePackage:     "agentsensor", SourceField: "event_stream",
		TypeName: "AgentEvent", TypeImport: "com.example.types.agentsensor.Agentsensor",
		TypeOuterClass: "Agentsensor", ClassName: "AgentsensorAgentEventBusEventStreamSubscriber",
		TargetService:         "eventbus",
		StoreEpochField:       "store_epoch_ms",
		StoreEpochFieldPascal: "StoreEpochMs",
	}
	plain := javaSubscriptionEntry{
		FieldPascal: "Stream", FieldCamel: "stream",
		HandlerMethodName: "onStreamAccountingrecordAccountingRecordBusStream",
		SourcePackage:     "accountingrecord", SourceField: "stream",
		TypeName: "AccountingRecord", TypeImport: "com.example.types.accountingrecord.Accountingrecord",
		TypeOuterClass: "Accountingrecord", ClassName: "AccountingrecordAccountingRecordBusStreamSubscriber",
		TargetService: "eventbus",
	}

	rendered := renderJavaSm(t, []javaSubscriptionEntry{stamped, plain})

	want := "event = event.toBuilder().setStoreEpochMs(System.currentTimeMillis()).build();"
	if got := strings.Count(rendered, want); got != 1 {
		t.Fatalf("exactly the epoch-bearing subscription must stamp, got %d occurrences of %q", got, want)
	}
	// The stamp must precede the dispatch: a handler that has already stored the
	// row cannot be given its epoch afterwards.
	stamp := strings.Index(rendered, want)
	dispatch := strings.Index(rendered, "handler.onEventStreamAgentsensorAgentEventBusEventStream(event,")
	if stamp < 0 || dispatch < 0 || stamp > dispatch {
		t.Fatalf("the store-epoch stamp must be rendered before the handler dispatch (stamp=%d dispatch=%d)", stamp, dispatch)
	}
}

// The other half: a subscription over a type declaring no (spec.store_epoch)
// renders no stamp at all, and its dispatch block is spelled exactly as it was —
// the determinism and current-generation gates compare whole trees.
func TestSubscriptionWithoutStoreEpochRendersNoStamp(t *testing.T) {
	rendered := renderJavaSm(t, []javaSubscriptionEntry{{
		FieldPascal: "Stream", FieldCamel: "stream",
		HandlerMethodName: "onStreamAccountingrecordAccountingRecordBusStream",
		SourcePackage:     "accountingrecord", SourceField: "stream",
		TypeName: "AccountingRecord", TypeImport: "com.example.types.accountingrecord.Accountingrecord",
		TypeOuterClass: "Accountingrecord", ClassName: "AccountingrecordAccountingRecordBusStreamSubscriber",
		TargetService: "eventbus",
	}})
	if strings.Contains(rendered, "store_epoch") || strings.Contains(rendered, "toBuilder().set") {
		t.Fatal("a subscription over a type declaring no (spec.store_epoch) must render no stamp")
	}
	want := "                        subscriber.subscribe((event, meta) -> {\n" +
		"                            handler.onStreamAccountingrecordAccountingRecordBusStream(event,\n"
	if !strings.Contains(rendered, want) {
		t.Fatalf("the un-stamped dispatch must be unchanged byte for byte; want:\n%s", want)
	}
}
