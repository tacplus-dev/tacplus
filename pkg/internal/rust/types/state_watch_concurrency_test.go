// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

// A state client holds ONE stub behind a mutex and spawns one watch loop per
// boundary. Each loop opens a server-streaming Watch and then consumes it for
// the lifetime of the service.
//
// If the MutexGuard is still in scope while the stream is consumed, the first
// boundary to acquire the lock holds it forever and every other watch loop
// blocks in lock() -- with no error, no log, and no timeout. The caches those
// boundaries feed stay empty for the life of the process.
//
// That is not hypothetical. sentinel started exactly one of its eight Cluster
// watches (grpc_stats won the race); cluster_info, cluster_health, node_metrics,
// persistence_status, network_stats, thread_info and token_cache_stats never
// began. The dashboard then failed client-side validation with "field
// clusterInfo is required" and the webapp retried forever.
//
// The fix is to clone the stub under the lock and drop the guard before opening
// the stream. A tonic client is Clone and shares the multiplexed channel.
func TestRustStateWatchDoesNotHoldStubLockAcrossStream(t *testing.T) {
	src, err := templates.FS.ReadFile("rust/types/statewrapper.rs.tmpl")
	if err != nil {
		t.Fatalf("read statewrapper template: %v", err)
	}
	tmpl := string(src)

	// Every site that opens a watch stream must clone out of the guard.
	watchOpens := strings.Count(tmpl, "response = stub.watch_")
	if watchOpens == 0 {
		t.Fatalf("no watch stream open sites found; template shape changed")
	}
	clones := strings.Count(tmpl, "guard.clone()")
	if clones != watchOpens {
		t.Fatalf("every watch site must clone the stub and release the lock: %d watch opens but %d guard.clone()",
			watchOpens, clones)
	}

	// The direct binding is the regression: `let mut stub = ... self.stub.lock()`
	// keeps the guard alive for the whole streaming loop.
	if strings.Contains(tmpl, "let mut stub = tokio::select! {\n            _ = &mut close => return Ok(false),\n            stub = self.stub.lock() => stub,\n        };") {
		t.Fatalf("watch binds the MutexGuard directly to `stub`; the guard then lives for the " +
			"whole stream and starves every other boundary's watch loop")
	}
}

// A service's typed Config arrives from kernel's config state, which is seeded
// from the deploy bundle. The bundle carries only what the operator was given,
// so any field it omits arrives ZERO -- not defaulted.
//
// tacplus generates a config_defaults() for every message from
// [(spec.default_string)] and friends, but the ServiceManager never called it on
// the delivery path. The declared defaults therefore had no runtime effect at
// all: echo's greeting is declared `[(spec.default_string) = "hello"]` and
// arrived empty, so the sample service rendered ", hello from the browser".
//
// Both delivery points must default: the initial fetch and every hot-reload.
func TestRustServiceManagerAppliesConfigDefaultsOnDelivery(t *testing.T) {
	src, err := templates.FS.ReadFile("rust/types/service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("read service_manager template: %v", err)
	}
	tmpl := string(src)

	if !strings.Contains(tmpl, "{{.ConfigHelpersModPath}}::config_defaults(&mut initial_cfg);") {
		t.Fatalf("initial typed config must have config_defaults applied before delivery")
	}
	if !strings.Contains(tmpl, "{{.ConfigHelpersModPath}}::config_defaults(&mut c);") {
		t.Fatalf("hot-reloaded typed config must have config_defaults applied before delivery")
	}
	// The value must be mutable at both sites or defaults cannot be written.
	if !strings.Contains(tmpl, "let mut initial_cfg = accessor.config().fetch()") {
		t.Fatalf("initial config must be bound mutably so defaults can be applied")
	}
}
