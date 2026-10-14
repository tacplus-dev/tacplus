// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the requires-device fixture. NOT generated: run.sh copies
// this file into the generated ServiceHealth state package before `go test ./...`,
// because Fetch's retry loop and its defaults call are package-internal to the
// generated state client and nothing outside that package can reach them.
//
// What it pins: on 2026-09-23 scottyagent (Rust) and stratum-2 (Java) crash-looped
// at startup because the singleton Fetch validated the fetched value BEFORE
// applying its generated proto defaults. A field the seeded state omits on purpose
// — because it carries a (spec.default_*) — arrived zero-valued, failed its own
// (spec.assert_*), and the retry loop burned the whole config_fetch_timeout budget
// on a value that should have passed. 526feb6f / bba83f49 moved the defaults call
// ahead of validate in all five languages. Before those commits nothing but a
// template render test covered it: no fixture anywhere in cross-test/ declared a
// plain singleton boundary, so no generate-and-compile gate could ever run Fetch.
// ServiceHealth.lock_settings (cross-test/proto-requiresdevice/platform/health.proto)
// is that fixture, and this is the test that executes the path.

package service_health

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gitlab.com/example/dummy/vault/tacplus/model/chassis/servicemanager"
	types "gitlab.com/example/dummy/vault/tacplus/model/types/platform/requiresdevicehealth"
	trace_pb "gitlab.com/example/dummy/vault/tacplus/model/types/sys/trace"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// fetchStub answers GetLockSettings with a canned value. Every other method of
// the state-service client is inherited from the embedded nil interface: Fetch
// calls exactly one RPC, and embedding keeps this stub from having to be
// rewritten each time the generated state service grows a boundary.
type fetchStub struct {
	types.ServiceHealthStateServiceClient
	value *types.LockSettings
}

// A real gRPC call unmarshals a fresh message off the wire on every attempt, so
// the stub must clone. Handing back the same pointer lets a defaults call that
// runs in the WRONG order still mutate the stored value, and the next retry then
// sees the defaulted field — which makes the pre-526feb6f ordering pass this test
// one second late instead of failing it.
func (s *fetchStub) GetLockSettings(ctx context.Context, in *trace_pb.TraceRequest,
	opts ...grpc.CallOption) (*types.LockSettings, error) {
	return proto.Clone(s.value).(*types.LockSettings), nil
}

// newFetchClient builds the minimum StateClient that Fetch needs: the stub it
// calls, the client context its retry sleep selects on, and a ServiceManager so
// configFetchTimeout can read its (absent) config and fall back to 30s.
func newFetchClient(t *testing.T, value *types.LockSettings) (*LockSettingsWrapper, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := &StateClient{
		sc:            &servicemanager.ServiceManager{},
		stateStub:     &fetchStub{value: value},
		ctx:           ctx,
		cancel:        cancel,
		watchStatuses: make(map[string]WatchStatus),
	}
	c.wrapperLockSettings = &LockSettingsWrapper{parent: c}
	return c.wrapperLockSettings, cancel
}

// The regression assertion. lock_acquire_attempts carries (spec.default_int) = 5
// and (spec.assert_ge) = 1, so a state authority that seeds the singleton without
// it puts a zero on the wire. Fetch must default it to 5 and then validate, and
// return. With the pre-526feb6f ordering this call does not return a value at all:
// validate rejects the zero, and Fetch retries until the fetch budget is spent.
func TestLockSettingsFetchAppliesDefaultsBeforeValidating(t *testing.T) {
	wrapper, cancel := newFetchClient(t, &types.LockSettings{})
	defer cancel()

	ctx, timeout := context.WithTimeout(context.Background(), 5*time.Second)
	defer timeout()

	value, err := wrapper.Fetch(ctx)
	if err != nil {
		t.Fatalf("Fetch of a singleton that omits its defaulted field must succeed once "+
			"defaults are applied before validation; got: %v", err)
	}
	if value.GetLockAcquireAttempts() != int32(types.DefaultLockAcquireAttempts) {
		t.Errorf("Fetch returned lock_acquire_attempts = %d, want the generated default %d; "+
			"the value must carry the default it was validated against, not the wire zero",
			value.GetLockAcquireAttempts(), types.DefaultLockAcquireAttempts)
	}
}

// The converse half: defaults must not paper over a real violation. -1 is not the
// zero value, so LockSettingsDefaults leaves it alone and (spec.assert_ge) = 1
// still rejects it. A "fix" that defaulted unconditionally, or dropped the
// validate call to make the case above pass, fails here.
func TestLockSettingsFetchStillRejectsViolatingValue(t *testing.T) {
	wrapper, cancel := newFetchClient(t, &types.LockSettings{LockAcquireAttempts: -1})
	defer cancel()

	// Fetch retries a failing value for the whole fetch budget (30s with no
	// config). Cancelling the caller context ends the retry sleep, which is the
	// supported way to bound the loop; the returned error is the validation error.
	ctx, timeout := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer timeout()

	value, err := wrapper.Fetch(ctx)
	if err == nil {
		t.Fatalf("Fetch must not return a singleton that violates (spec.assert_ge) = 1 "+
			"after defaults; got value %v", value)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Fetch must surface the validation error, not the bare context deadline: %v", err)
	}
	if got := err.Error(); !strings.Contains(got, "must be >= 1") {
		t.Errorf("Fetch error must name the failed assertion; got: %v", got)
	}
}
