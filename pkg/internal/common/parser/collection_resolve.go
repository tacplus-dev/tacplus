// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// ResolveCollections computes the codegen-facing collection.Resolved for every
// method_collection-annotated method across services and attaches it to
// MethodInfo.Resolved. Methods without the annotation are left untouched
// (Resolved stays nil).
//
// This is the single place policy math runs for codegen: the per-language
// emitters branch on Resolved.Mode / Resolved.Tier and never recompute tiers or
// trust themselves, which is what keeps the five backends in parity. index is
// the message index (collection.BuildMessageIndex over the parsed files).
//
// collectionMessageIndex lazily builds and caches a fully-qualified message
// index across all parsed files, for collection tier resolution. Cached
// because ConvertToProtoInfo is called once per service file but the index is
// global.
func (p *DescriptorParser) collectionMessageIndex() map[string]*model.Message {
	if p.collIndexBuilt {
		return p.collIndex
	}
	var files []model.ProtoFile
	for path := range p.GetAllFiles() {
		pf, _ := p.ConvertToProtoFile(path)
		files = append(files, pf)
	}
	p.collIndex = collection.BuildMessageIndex(files)
	p.collIndexBuilt = true
	return p.collIndex
}

// IsTier2SnapshotCollection reports whether this method is a Tier-2 SNAPSHOT
// collection read: server-streaming on the wire, but the generated seam is
// collection-in/collection-out (handler returns a materialized collection, the
// wrapper chunks it into the stream, the client accumulates it back). Tier-1
// stays unary and Tier-3 (cursor) is WS-2, so both report false here. Requires
// ResolveCollections to have run (Resolved populated).
func (m *MethodInfo) IsTier2SnapshotCollection() bool {
	return m.ServerStreaming && m.Resolved != nil && m.Resolved.Mode == "SNAPSHOT" && m.Resolved.Tier == collection.TierStream
}

// IsTier3SnapshotCollection reports whether this method is a Tier-3 SNAPSHOT
// collection read: server-streaming on the wire, surfaced as a lazy cursor —
// the handler is a pull producer and the client is an iterator with cleanup on
// early exit (WS-2). Requires ResolveCollections to have run.
func (m *MethodInfo) IsTier3SnapshotCollection() bool {
	return m.ServerStreaming && m.Resolved != nil && m.Resolved.Mode == "SNAPSHOT" && m.Resolved.Tier == collection.TierCursor
}

// IsWatchCollection reports whether this method is a WATCH collection read:
// server-streaming a {Method}WatchEvent envelope (snapshot→delta), surfaced via
// the replay-ring runtime (WS-3). Requires ResolveCollections to have run.
func (m *MethodInfo) IsWatchCollection() bool {
	return m.ServerStreaming && m.Resolved != nil && m.Resolved.Mode == "WATCH"
}

// IsPageCollection reports whether this method is a PAGE collection read: a
// unary, client-driven, resumable window (opaque keyset page_token over
// order_by, bounded by page_size). The handler is a pull-by-cursor producer and
// the client gets an explicit page call plus an auto-pager. Requires
// ResolveCollections to have run.
func (m *MethodInfo) IsPageCollection() bool {
	return !m.ServerStreaming && m.Resolved != nil && m.Resolved.Mode == "PAGE"
}

// IsRetrySafeUnary reports whether tacplus has enough semantic information to
// apply generated unary retries. Today that is limited to Tier-1 SNAPSHOT
// collection reads: finite, read-like RPCs explicitly marked with
// method_collection and carried as one unary response.
func (m *MethodInfo) IsRetrySafeUnary() bool {
	return m != nil &&
		!m.ClientStreaming &&
		!m.ServerStreaming &&
		m.Resolved != nil &&
		m.Resolved.Mode == "SNAPSHOT" &&
		m.Resolved.Tier == collection.TierInline
}

func ResolveCollections(services []*ServiceInfo, index map[string]*model.Message) {
	for _, svc := range services {
		for _, m := range svc.Methods {
			if m.Collection == nil {
				continue
			}
			r := resolveOneCollection(svc, m, index)
			m.Resolved = &r
		}
	}
}

func resolveOneCollection(svc *ServiceInfo, m *MethodInfo, index map[string]*model.Message) collection.Resolved {
	c := m.Collection
	effAuthDisabled := m.AuthDisabled || svc.AuthDisabled
	trust := collection.TrustOf(svc.IsC2S, svc.IsS2S, svc.IsC2D, effAuthDisabled)
	itemBytes := collectionItemBytes(m, index)
	r := collection.Resolve(c.Mode, c.MaxItems, c.ReplayDepth, c.DefaultPageSize, c.OrderBy, itemBytes, trust)
	if c.Mode == "WATCH" {
		// Resolve the {Method}WatchEvent envelope shape for the emitters: the
		// oneof name, the snapshot_item message (watched item), and the delta
		// message ({Method}Change). The wrapper needs these to build envelopes.
		if env := lookupMessage(m.FullOutputType, index); env != nil {
			pkg := pkgOf(m.FullOutputType)
			if si := fieldByName(env, "snapshot_item"); si != nil {
				r.WatchItemType = watchFieldFQN(si, pkg)
				r.WatchOneOf = si.OneOfName
			}
			if delta := fieldByName(env, "delta"); delta != nil {
				r.WatchChangeType = watchFieldFQN(delta, pkg)
			}
		}
		// The delta's revision field, which the replay ring is keyed by. Always
		// populated so no emitter has to know the default, and validated (it
		// exists, it is a singular int64) before generation reaches an emitter.
		r.WatchRevisionField = c.RevisionField
		if r.WatchRevisionField == "" {
			r.WatchRevisionField = collection.DefaultWatchRevisionField
		}
		// The wrapper resumes from request.from_revision only when the request
		// declares that field (the standard {Method}Request). Handle-based watch
		// requests (RecordMap's QueryHandle) omit it.
		if in := lookupMessage(m.FullInputType, index); in != nil {
			r.WatchHasFromRevision = fieldByName(in, "from_revision") != nil
		}
	} else if c.Mode == "PAGE" {
		// Resolve the page item type: the FQN of the response's single repeated
		// items field's element. The emitters render Page[*Item] from it.
		if resp := lookupMessage(m.FullOutputType, index); resp != nil {
			pkg := pkgOf(m.FullOutputType)
			for i := range resp.Fields {
				f := &resp.Fields[i]
				if f.IsRepeated && !f.IsMap {
					r.PageItemType = watchFieldFQN(f, pkg)
					r.PageItemsField = f.Name
					break
				}
			}
			r.PageHasTotal = fieldByName(resp, "total_count") != nil
		}
	}
	return r
}

// watchFieldFQN builds the fully-qualified type ("pkg.Name") of a message field,
// preferring the field's own package and falling back to the envelope's package
// (same-package fields carry no TypePackage).
func watchFieldFQN(f *model.Field, envelopePkg string) string {
	if f.TypePackage != "" {
		return f.TypePackage + "." + f.Type
	}
	if envelopePkg != "" {
		return envelopePkg + "." + f.Type
	}
	return f.Type
}

// collectionItemBytes returns the per-item max serialized size used for SNAPSHOT
// tier routing, mirroring the item-resolution rules the validator enforces
// (see collection_validate.go): a streamed SNAPSHOT's output message IS the
// item; a unary SNAPSHOT's item is its single repeated field's element. Returns
// -1 (unresolved) for WATCH — whose tier is not derived from item bytes — and
// when the item type cannot be resolved.
func collectionItemBytes(m *MethodInfo, index map[string]*model.Message) int64 {
	if m.Collection == nil || m.Collection.Mode != "SNAPSHOT" {
		return -1
	}
	outMsg := lookupMessage(m.FullOutputType, index)
	if outMsg == nil {
		return -1
	}
	if m.ServerStreaming {
		return collection.MaxMessageBytes(outMsg, pkgOf(m.FullOutputType), index)
	}
	repeated := repeatedFields(outMsg)
	if len(repeated) == 1 {
		return collection.ItemBytes(repeated[0], index)
	}
	return -1
}
