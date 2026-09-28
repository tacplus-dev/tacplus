package parser

import (
	"sort"

	"google.golang.org/protobuf/reflect/protoreflect"

	spec "gitlab.com/seabeam/schematic/tacplus/target/proto/spec/sys"
)

// spec.proto declares a family of statedb tuning annotations — replica counts and
// eviction policy/TTL/idle-timeout — complete with worked examples in their own
// doc comments and, for eviction_ttl_seconds, in docs/USER.md. Exactly one of
// them, (spec.eviction_max_size), is consumed by a generator. The rest reach no
// collector, no template and no emitted line in any of the seven target
// languages: an author who writes
//
//	SessionData sessions = 2 [(spec.eviction_ttl_seconds) = 3600];
//
// gets a state boundary that never expires anything, with no diagnostic. That is
// the fail-silent annotation class (NEW-23 / MS-3 / NEW-13) applied to durability
// and retention, where the silent outcome is unbounded growth or stale reads
// rather than a missing surface.
//
// tacplus refuses to generate for them rather than accept an intent it does not
// implement. Reversing that (implementing them instead) is the maintainer fork
// recorded in todo/QUESTIONS.md.
//
// unimplementedStatedbFieldExtensions returns the field-level members keyed by
// their proto option name.
func unimplementedStatedbFieldExtensions() map[string]protoreflect.ExtensionType {
	return map[string]protoreflect.ExtensionType{
		"sync_backup_count":         spec.E_SyncBackupCount,
		"async_backup_count":        spec.E_AsyncBackupCount,
		"eviction_policy":           spec.E_EvictionPolicy,
		"eviction_max_size_policy":  spec.E_EvictionMaxSizePolicy,
		"eviction_ttl_seconds":      spec.E_EvictionTtlSeconds,
		"eviction_max_idle_seconds": spec.E_EvictionMaxIdleSeconds,
	}
}

// unimplementedStatedbMessageExtensions returns the message-level members (the
// state-root defaults for the same three concerns) keyed by proto option name.
func unimplementedStatedbMessageExtensions() map[string]protoreflect.ExtensionType {
	return map[string]protoreflect.ExtensionType{
		"state_eviction_policy":    spec.E_StateEvictionPolicy,
		"state_sync_backup_count":  spec.E_StateSyncBackupCount,
		"state_async_backup_count": spec.E_StateAsyncBackupCount,
	}
}

// UnimplementedStatedbAnnotations lists every annotation name the two tables
// above record, sorted. Exported because the rejection lives in
// pkg/internal/proto while the parsing lives here, and the set must have exactly
// one definition: a name parsed here but missing there is silently accepted
// again, which is the very failure this closes.
func UnimplementedStatedbAnnotations() []string {
	fields := unimplementedStatedbFieldExtensions()
	messages := unimplementedStatedbMessageExtensions()
	names := make([]string, 0, len(fields)+len(messages))
	for name := range fields {
		names = append(names, name)
	}
	for name := range messages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
