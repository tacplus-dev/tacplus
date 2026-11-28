// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// cross-test/config-push (tacplus#927) copies this test into the generated
// s2sm package of the fixture's tenancy, the owner of the pushed slice
// TenancyTenantSettings. It runs the owner's generated receiver (its slice
// table, decoder, validator and typed accessor) on a TCP listener, and
// against it the generated push of stratum, the tenant's record store, a
// separate generation that never held tenancy's proto: its owners table from
// the registry and its feed from its configpush.TenantSliceRecord entries,
// through configpush_wire.go. The two meet only on the wire. The fleet's
// token is the test's: the owner takes a stream's bearer as its subject.

package sm

import (
	"context"
	"iter"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	storecollection "gitlab.com/example/dummy/stratum/tacplus/model/chassis/collection"
	storecp "gitlab.com/example/dummy/stratum/tacplus/model/chassis/configpush"
	storeservicemanager "gitlab.com/example/dummy/stratum/tacplus/model/chassis/servicemanager"
	storesm "gitlab.com/example/dummy/stratum/tacplus/model/s2sm"
	store_fe "gitlab.com/example/dummy/stratum/tacplus/model/s2sm/grpc/servers/vservices/query"
	store_pb "gitlab.com/example/dummy/stratum/tacplus/model/types/sys/configpush"
	store_query "gitlab.com/example/dummy/stratum/tacplus/model/types/vservices/query"
	wrapperToken "gitlab.com/example/dummy/tenancy/tacplus/model/chassis/token"
	tenancy_pb "gitlab.com/example/dummy/tenancy/tacplus/model/types/services/tenancy"
	grpcconfig_pb "gitlab.com/example/dummy/tenancy/tacplus/model/types/sys/grpcconfig"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	wireTrustDomain = "test.local"
	// wireField is tenancy's registered tenant_slice_field
	// (cross-test/proto-configpush/registry.yaml).
	wireField = 1
	wireWait  = 20 * time.Second
)

func wireSubject(tid, service string) string {
	return "spiffe://" + wireTrustDomain + "/tenants/" + tid + "/services/" + service
}

// ---- the owner --------------------------------------------------------------

type wireRegistry map[string]bool

func (r wireRegistry) HoldsTenant(_ context.Context, tid string) (bool, error) { return r[tid], nil }

// bearerSubject is the test's authenticator: the stream's bearer is its subject.
func bearerSubject(ctx context.Context) (*wrapperToken.TokenWrapper, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	for _, v := range md.Get("authorization") {
		if sub, ok := strings.CutPrefix(v, "Bearer "); ok {
			return wrapperToken.NewImmutableToken(nil).SetSubject(sub), nil
		}
	}
	return nil, status.Error(codes.Unauthenticated, "no bearer")
}

type wireOwner struct {
	sm   *ServiceManager
	srv  *grpc.Server
	addr string
}

// startOwner serves the owner's generated receiver at addr ("" for any port),
// its registry holding tenants.
func startOwner(t *testing.T, addr string, tenants ...string) *wireOwner {
	t.Helper()
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	var lis net.Listener
	var err error
	for i := 0; i < 50; i++ {
		if lis, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	sm := &ServiceManager{cfg: &grpcconfig_pb.Config{SpiffeTrustDomain: wireTrustDomain}}
	sm.authenticateFunc = bearerSubject
	sm.configPush = newConfigPushReceiver(sm)
	reg := wireRegistry{}
	for _, tid := range tenants {
		reg[tid] = true
	}
	sm.configPush.SetTenantRegistry(reg)
	srv := grpc.NewServer()
	if err := sm.configPush.RegisterServices(srv); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()
	o := &wireOwner{sm: sm, srv: srv, addr: lis.Addr().String()}
	t.Cleanup(o.srv.Stop)
	return o
}

// waitFor polls the owner's accessor for tid until want holds.
func (o *wireOwner) waitFor(t *testing.T, tid, what string, want func(*tenancy_pb.TenancyTenantSettings, int64, bool) bool) {
	t.Helper()
	deadline := time.Now().Add(wireWait)
	for {
		v, version, ok := o.sm.ConfigPush().TenancyTenantSettings(tid)
		if want(v, version, ok) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the owner never held %s for %s: holds %v at %d (%v)", what, tid, v, version, ok)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (o *wireOwner) waitHolds(t *testing.T, tid, joinMode string, version int64) {
	t.Helper()
	o.waitFor(t, tid, joinMode, func(v *tenancy_pb.TenancyTenantSettings, ver int64, ok bool) bool {
		return ok && v.GetJoinMode() == joinMode && ver == version
	})
}

func (o *wireOwner) holdsNone(t *testing.T, tid string) {
	t.Helper()
	if v, version, ok := o.sm.ConfigPush().TenancyTenantSettings(tid); ok {
		t.Fatalf("the owner holds %v at %d for %s, want none", v, version, tid)
	}
}

// ---- the store --------------------------------------------------------------

type wireChange = store_query.TenantSliceRecordWatchChange

// wirePlane is the store's record plane: an in-memory configpush.TenantSliceRecord
// map behind the generated handler's watch, the only method the feed calls.
type wirePlane struct {
	store_fe.RecordMapHandler

	mu       sync.Mutex
	rev      int64
	entries  map[int32][]byte
	watchers []chan *wireChange
	closed   bool
}

func newWirePlane(t *testing.T) *wirePlane {
	p := &wirePlane{entries: make(map[int32][]byte)}
	t.Cleanup(p.close)
	return p
}

func (p *wirePlane) WatchTenantSliceRecordChanges(_ *storeservicemanager.Caller, _ *store_query.WatchTenantSliceRecordChangesRequest) (*storecollection.WatchSource[store_pb.TenantSliceRecord, wireChange], error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, status.Error(codes.Unavailable, "closed")
	}
	var items []*store_pb.TenantSliceRecord
	for f, v := range p.entries {
		items = append(items, &store_pb.TenantSliceRecord{SliceField: f, Value: v})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].GetSliceField() < items[j].GetSliceField() })
	ch := make(chan *wireChange, 64)
	p.watchers = append(p.watchers, ch)
	return &storecollection.WatchSource[store_pb.TenantSliceRecord, wireChange]{
		Snapshot: func(yield func(*store_pb.TenantSliceRecord, error) bool) {
			for _, it := range items {
				if !yield(it, nil) {
					return
				}
			}
		},
		Revision: p.rev,
		Changes: func() iter.Seq2[*wireChange, error] {
			return func(yield func(*wireChange, error) bool) {
				for c := range ch {
					if !yield(c, nil) {
						return
					}
				}
			}
		},
	}, nil
}

func (p *wirePlane) write(c *wireChange) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rev++
	c.Revision = p.rev
	if c.GetType() == store_query.RecordChangeType_RECORD_CHANGE_TYPE_DELETE {
		delete(p.entries, c.GetItem().GetSliceField())
	} else {
		p.entries[c.GetItem().GetSliceField()] = c.GetItem().GetValue()
	}
	for _, w := range p.watchers {
		select {
		case w <- c:
		default:
		}
	}
	return p.rev
}

// put writes the slice as the tenant's admin writes it: the owner's message,
// encoded, which the store holds as bytes.
func (p *wirePlane) put(t *testing.T, field int32, m *tenancy_pb.TenancyTenantSettings) int64 {
	t.Helper()
	value, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return p.write(&wireChange{Type: store_query.RecordChangeType_RECORD_CHANGE_TYPE_MODIFY,
		Item: &store_pb.TenantSliceRecord{SliceField: field, Value: value}})
}

func (p *wirePlane) remove(field int32) int64 {
	return p.write(&wireChange{Type: store_query.RecordChangeType_RECORD_CHANGE_TYPE_DELETE,
		Item: &store_pb.TenantSliceRecord{SliceField: field}})
}

func (p *wirePlane) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	for _, w := range p.watchers {
		close(w)
	}
}

// wireStream records how each stream ended and closes its connection then.
type wireStream struct {
	store_pb.ConfigPush_PushClient
	conn *grpc.ClientConn
	ends chan<- codes.Code
	once sync.Once
}

func (s *wireStream) Recv() (*store_pb.ConfigPushAck, error) {
	ack, err := s.ConfigPush_PushClient.Recv()
	if err != nil {
		s.once.Do(func() {
			_ = s.conn.Close()
			select {
			case s.ends <- status.Code(err):
			default:
			}
		})
	}
	return ack, err
}

// startStore runs the store's generated publisher and feed over plane. Its
// opener dials the owner at target() as subject, the store's identity.
func startStore(t *testing.T, plane *wirePlane, target func() string, subject string) <-chan codes.Code {
	t.Helper()
	ends := make(chan codes.Code, 64)
	open := func(ctx context.Context, owner string) (store_pb.ConfigPush_PushClient, error) {
		if owner != "tenancy" {
			return nil, status.Errorf(codes.Internal, "no owner %q in this fleet", owner)
		}
		conn, err := grpc.NewClient(target(), grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, err
		}
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+subject)
		// trace: unrelated: the store's own background push, caused by no inbound request
		stream, err := store_pb.NewConfigPushClient(conn).Push(ctx)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return &wireStream{ConfigPush_PushClient: stream, conn: conn, ends: ends}, nil
	}
	p := storesm.WirePublisher(plane, storecp.Opener(open))
	t.Cleanup(p.Close)
	return ends
}

func waitEnd(t *testing.T, ends <-chan codes.Code, want codes.Code) {
	t.Helper()
	timeout := time.After(wireWait)
	for {
		select {
		case got := <-ends:
			if got == want {
				return
			}
			t.Logf("a stream ended %s, waiting for %s", got, want)
		case <-timeout:
			t.Fatalf("no stream ended %s", want)
		}
	}
}

// ---- the tests --------------------------------------------------------------

// The store's generation and the owner's agree on the slice's identity: the
// registry's tenant_slice_field, which the store's run knows only from the
// registry.
func TestConfigPushWireOwnersTable(t *testing.T) {
	owners := storesm.WireOwners()
	if len(owners) != 1 || owners[wireField] != "tenancy" {
		t.Fatalf("the store's owners table is %v, want {%d: tenancy}", owners, wireField)
	}
}

// A tenant's store pushes its slice to the owner: a snapshot, then each new
// version at the record's revision, a delete as absent, and after the owner
// restarts (holding nothing, memory only) the snapshot again, with no new
// write.
func TestConfigPushWireStoreToOwner(t *testing.T) {
	owner := startOwner(t, "", "tnt00001")
	owner.holdsNone(t, "tnt00001") // fails closed before the first snapshot
	var addr sync.Map
	addr.Store("owner", owner.addr)
	target := func() string { v, _ := addr.Load("owner"); return v.(string) }

	plane := newWirePlane(t)
	rev := plane.put(t, wireField, &tenancy_pb.TenancyTenantSettings{JoinMode: "invite"})
	watched := make(chan string, 16)
	owner.sm.ConfigPush().WatchTenancyTenantSettings(func(tid string) { watched <- tid })
	startStore(t, plane, target, wireSubject("tnt00001", "stratum"))

	owner.waitHolds(t, "tnt00001", "invite", rev)
	select {
	case tid := <-watched:
		if tid != "tnt00001" {
			t.Fatalf("the owner's watch reported %q, want tnt00001", tid)
		}
	case <-time.After(wireWait):
		t.Fatal("the owner's watch never reported tnt00001")
	}
	owner.holdsNone(t, "tnt00002")

	rev = plane.put(t, wireField, &tenancy_pb.TenancyTenantSettings{JoinMode: "open"})
	owner.waitHolds(t, "tnt00001", "open", rev)

	// The owner restarts at the same address: it holds nothing until the
	// store's publisher reconnects and sends its snapshot again.
	owner.srv.Stop()
	restarted := startOwner(t, owner.addr, "tnt00001")
	restarted.waitHolds(t, "tnt00001", "open", rev)

	rev = plane.remove(wireField)
	restarted.waitFor(t, "tnt00001", "no slice", func(_ *tenancy_pb.TenancyTenantSettings, _ int64, ok bool) bool { return !ok })
	if rev <= 2 {
		t.Fatalf("the delete's revision is %d", rev)
	}
}

// The owner admits only the tenant's own stratum, for a tenant its registry
// holds: another tenant service, or the stratum of a tenant it does not hold,
// is refused PERMISSION_DENIED and the owner holds nothing for it.
func TestConfigPushWireRefusesAnotherCaller(t *testing.T) {
	for _, tc := range []struct{ name, tid, subject string }{
		{"another service of the tenant", "tnt00001", wireSubject("tnt00001", "ledger")},
		{"a tenant the registry does not hold", "tnt00009", wireSubject("tnt00009", "stratum")},
		{"another trust domain", "tnt00001", "spiffe://other.local/tenants/tnt00001/services/stratum"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := startOwner(t, "", "tnt00001")
			plane := newWirePlane(t)
			plane.put(t, wireField, &tenancy_pb.TenancyTenantSettings{JoinMode: "invite"})
			ends := startStore(t, plane, func() string { return owner.addr }, tc.subject)
			waitEnd(t, ends, codes.PermissionDenied)
			owner.holdsNone(t, tc.tid)
		})
	}
}

// A value the owner's generated validation refuses ends the stream
// INVALID_ARGUMENT and is not held; the store's next valid write is.
func TestConfigPushWireRefusesAnInvalidValue(t *testing.T) {
	owner := startOwner(t, "", "tnt00001")
	plane := newWirePlane(t)
	plane.put(t, wireField, &tenancy_pb.TenancyTenantSettings{JoinMode: strings.Repeat("x", 65)})
	ends := startStore(t, plane, func() string { return owner.addr }, wireSubject("tnt00001", "stratum"))
	waitEnd(t, ends, codes.InvalidArgument)
	owner.holdsNone(t, "tnt00001")

	rev := plane.put(t, wireField, &tenancy_pb.TenancyTenantSettings{JoinMode: "invite"})
	owner.waitHolds(t, "tnt00001", "invite", rev)
}
