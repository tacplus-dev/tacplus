// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// TENANT-PINNED READS, Java serving side (issue #130). Twin of the Go wrapper's
// tenantpin_gen.go; the rule is stated once, in
// proto.ResolveTenantPinnedBoundaries. Each pinned read RPC of a hosted state
// service resolves a TenantPin right after the caller is bound (null for a
// caller that is not pinned, which is then served exactly as before), refuses a
// Get/ContainsKey of another tenant's key, and filters + projects the response
// — or, on Watch/WatchQuery, every frame — before it is validated and sent.
// The TenantPin helper is a private nested class of the wrapper.

// tenantPin looks a method up in the pinned-read index.
func (es *javaEgressSanitizer) tenantPin(servicePackage, serviceName, method string) (proto.TenantPinnedRPC, bool) {
	if es == nil {
		return proto.TenantPinnedRPC{}, false
	}
	pin, ok := es.tenantPins[servicePackage+"."+serviceName+"/"+method]
	return pin, ok
}

// javaTenantPinShapeError refuses a pinned RPC whose generated shape is not the
// one this file renders a filter for (unreachable for the RPCs
// AppendStateServiceToProtos emits; a change there fails generation instead of
// serving the RPC unfiltered).
func javaTenantPinShapeError(method *parser.MethodInfo, pin proto.TenantPinnedRPC) error {
	stream := pin.Kind == proto.TenantPinWatch || pin.Kind == proto.TenantPinWatchQuery
	if method.ClientStreaming || method.ServerStreaming != stream || method.IsWatchCollection() || method.IsPageCollection() ||
		method.IsTier2SnapshotCollection() || method.IsTier3SnapshotCollection() {
		return fmt.Errorf("%s is a tenant-pinned %s read, but its generated shape is not the one the Java wrapper filters; refusing to serve it unfiltered", method.Name, pin.Kind)
	}
	return nil
}

// javaTenantPinPre renders the resolve and the key gate, spliced right after the
// caller is bound.
func javaTenantPinPre(pin proto.TenantPinnedRPC, indent string) string {
	out := fmt.Sprintf(`
%[1]s// Tenant-pinned read (%[2]s): a pinned caller is served its own tenant's
%[1]s// entry only, projected to its own config slice.
%[1]sfinal TenantPin __pin = TenantPin.resolve%[3]s(Auth.verifiedTokenFromContext());`, indent, pin.Boundary.Field, pin.Boundary.BoundaryPascal())
	if pin.Kind == proto.TenantPinGet || pin.Kind == proto.TenantPinContainsKey {
		out += fmt.Sprintf(`
%[1]sif (!TenantPin.admits(__pin, request.getKey())) {
%[1]s    throw ProtoException.newErrPermissionDenied(null, "%[2]s: the key is another tenant's entry");
%[1]s}`, indent, pin.Name)
	}
	return out
}

// javaTenantPinPost renders the response filter over `response`, spliced after
// the handler returns and before response validation.
func javaTenantPinPost(pin proto.TenantPinnedRPC, outputType, indent string) string {
	switch pin.Kind {
	case proto.TenantPinGet, proto.TenantPinContainsKey:
		return fmt.Sprintf(`
%[1]sif (__pin != null && response.hasValue()) {
%[1]s    response = response.toBuilder().setValue(TenantPin.project(__pin, response.getValue())).build();
%[1]s}`, indent)
	case proto.TenantPinGetAll:
		return fmt.Sprintf(`
%[1]sresponse = TenantPin.filterEntries(__pin, response);`, indent)
	case proto.TenantPinKeySet:
		return fmt.Sprintf(`
%[1]sif (__pin != null) {
%[1]s    response = response.toBuilder().clearKeys().addAllKeys(TenantPin.filterKeys(__pin, response.getKeysList())).build();
%[1]s}`, indent)
	case proto.TenantPinQuery:
		return fmt.Sprintf(`
%[1]sresponse = TenantPin.filterQuery(__pin, response);`, indent)
	case proto.TenantPinSize:
		// The handler's size counts every tenant; a pinned caller is told how
		// many entries it can see, counted over the keys it is admitted to.
		outer := outputType[:strings.LastIndex(outputType, ".")+1]
		return fmt.Sprintf(`
%[1]sif (__pin != null) {
%[1]s    %[2]s%[3]sKeysResponse __keys = handler.keySet%[3]s(caller, %[2]s%[3]sKeysRequest.getDefaultInstance());
%[1]s    response = response.toBuilder().setSize(TenantPin.filterKeys(__pin, __keys.getKeysList()).size()).build();
%[1]s}`, indent, outer, pin.Boundary.BoundaryPascal())
	}
	return ""
}

// javaTenantPinStream wraps a stream observer in the per-frame filter.
func javaTenantPinStream(pin proto.TenantPinnedRPC, observer string) string {
	filter := "filterEntries"
	if pin.Kind == proto.TenantPinWatchQuery {
		filter = "filterQuery"
	}
	return fmt.Sprintf("TenantPin.filtering(%s, value -> TenantPin.%s(__pin, value))", observer, filter)
}

// javaTenantPinClass renders the wrapper's private TenantPin helper. Empty when
// none of the wrapper's RPCs is pinned.
func javaTenantPinClass(pins []proto.TenantPinnedRPC) string {
	if len(pins) == 0 {
		return ""
	}
	boundaries := make(map[string]proto.TenantPinnedBoundary)
	for _, p := range pins {
		boundaries[p.Boundary.Field] = p.Boundary
	}
	fields := make([]string, 0, len(boundaries))
	for f := range boundaries {
		fields = append(fields, f)
	}
	sort.Strings(fields)

	var resolvers strings.Builder
	for _, f := range fields {
		bd := boundaries[f]
		var puts []string
		for _, s := range bd.Slices {
			puts = append(puts, fmt.Sprintf("java.util.Map.entry(%q, %q)", s.Caller, s.SliceField))
		}
		fmt.Fprintf(&resolvers, `
        /** Pins the callers holding a tenant-pinned read of %[1]s.%[2]s, each to its own config slice. */
        static TenantPin resolve%[3]s(TokenWrapper token) throws ProtoException {
            return resolve(token, java.util.Map.ofEntries(%[4]s), %[5]q, %[6]q);
        }
`, bd.StateRoot, bd.Field, bd.BoundaryPascal(), strings.Join(puts, ", "), bd.KeyField, bd.ConfigField)
	}

	return fmt.Sprintf(`
    /**
     * How one pinned caller reads a tenant-pinned collection (spec.per_service_config
     * held by an exported state root): every tenant's entry when its namespace is
     * /system or below, only the entry keyed by &lt;t&gt; when it is exactly
     * /tenants/&lt;t&gt;, and in both cases each entry projected to the key and the
     * caller's own config slice. A null pin is a caller that is not pinned, and
     * every helper below is the identity for it.
     */
    private static final class TenantPin {
        private final boolean all;
        private final String tenant;
        private final String keyField;
        private final String configField;
        private final String sliceField;

        private TenantPin(boolean all, String tenant, String keyField, String configField, String sliceField) {
            this.all = all;
            this.tenant = tenant;
            this.keyField = keyField;
            this.configField = configField;
            this.sliceField = sliceField;
        }
%[1]s
        /**
         * Pins token when its service path is one of slices' keys; null when it
         * is not. Fail closed: no verified service identity, or a pinned caller in
         * any other namespace, is PERMISSION_DENIED.
         */
        private static TenantPin resolve(TokenWrapper token, java.util.Map<String, String> slices,
                String keyField, String configField) throws ProtoException {
            if (token == null) {
                throw ProtoException.newErrPermissionDenied(null, "a tenant-pinned read needs a verified caller");
            }
            String path;
            try {
                path = token.servicePath();
            } catch (IllegalArgumentException e) {
                throw ProtoException.newErrPermissionDenied(e, "a tenant-pinned read needs the caller's service identity: %%s", e.getMessage());
            }
            String slice = slices.get(path);
            if (slice == null) {
                return null;
            }
            String ns = token.getNamespace() == null ? "" : token.getNamespace();
            if (ns.equals(%[2]q) || ns.startsWith(%[2]q + "/")) {
                return new TenantPin(true, "", keyField, configField, slice);
            }
            if (ns.startsWith(%[3]q)) {
                String t = ns.substring(%[3]q.length());
                if (!t.isEmpty() && t.indexOf('/') < 0) {
                    return new TenantPin(false, t, keyField, configField, slice);
                }
            }
            throw ProtoException.newErrPermissionDenied(null,
                "a tenant-pinned read is served to %[2]s and %[3]s<t> callers only, not namespace %%s", ns);
        }

        /** Whether the entry keyed by key is the pinned caller's. */
        static boolean admits(TenantPin p, String key) {
            return p == null || p.all || p.tenant.equals(key);
        }

        private static void keepOnly(com.google.protobuf.Message.Builder b, String... keep) {
            java.util.List<String> kept = java.util.Arrays.asList(keep);
            for (com.google.protobuf.Descriptors.FieldDescriptor fd : b.getDescriptorForType().getFields()) {
                if (!kept.contains(fd.getName())) {
                    b.clearField(fd);
                }
            }
            b.setUnknownFields(com.google.protobuf.UnknownFieldSet.getDefaultInstance());
        }

        /** A copy of entry holding only its key and, inside its config, the caller's own slice. */
        @SuppressWarnings("unchecked")
        static <T extends com.google.protobuf.Message> T project(TenantPin p, T entry) {
            if (p == null || entry == null) {
                return entry;
            }
            com.google.protobuf.Message.Builder b = entry.toBuilder();
            keepOnly(b, p.keyField, p.configField);
            com.google.protobuf.Descriptors.FieldDescriptor cfd = b.getDescriptorForType().findFieldByName(p.configField);
            if (cfd != null && b.hasField(cfd)) {
                com.google.protobuf.Message.Builder cb = ((com.google.protobuf.Message) b.getField(cfd)).toBuilder();
                keepOnly(cb, p.sliceField);
                b.setField(cfd, cb.build());
            }
            return (T) b.build();
        }

        private static String keyOf(TenantPin p, com.google.protobuf.Message m) {
            return (String) m.getField(m.getDescriptorForType().findFieldByName(p.keyField));
        }

        static java.util.List<String> filterKeys(TenantPin p, java.util.List<String> keys) {
            java.util.List<String> kept = new java.util.ArrayList<>();
            for (String k : keys) {
                if (admits(p, k)) {
                    kept.add(k);
                }
            }
            return kept;
        }

        /** Keeps the admitted entries of an entries response's map, projected. */
        @SuppressWarnings("unchecked")
        static <R extends com.google.protobuf.Message> R filterEntries(TenantPin p, R response) {
            if (p == null || response == null) {
                return response;
            }
            com.google.protobuf.Message.Builder b = response.toBuilder();
            com.google.protobuf.Descriptors.FieldDescriptor entries = b.getDescriptorForType().findFieldByName("entries");
            java.util.List<Object> kept = new java.util.ArrayList<>();
            for (Object o : (java.util.List<Object>) b.getField(entries)) {
                com.google.protobuf.MapEntry<String, com.google.protobuf.Message> e = (com.google.protobuf.MapEntry<String, com.google.protobuf.Message>) o;
                if (admits(p, e.getKey())) {
                    kept.add(e.toBuilder().setValue(project(p, e.getValue())).build());
                }
            }
            b.clearField(entries);
            for (Object e : kept) {
                b.addRepeatedField(entries, e);
            }
            return (R) b.build();
        }

        /** Keeps the admitted results of a query response, projected, and recounts them. */
        @SuppressWarnings("unchecked")
        static <R extends com.google.protobuf.Message> R filterQuery(TenantPin p, R response) {
            if (p == null || response == null) {
                return response;
            }
            com.google.protobuf.Message.Builder b = response.toBuilder();
            com.google.protobuf.Descriptors.FieldDescriptor results = b.getDescriptorForType().findFieldByName("results");
            java.util.List<com.google.protobuf.Message> kept = new java.util.ArrayList<>();
            for (Object o : (java.util.List<Object>) b.getField(results)) {
                com.google.protobuf.Message m = (com.google.protobuf.Message) o;
                if (admits(p, keyOf(p, m))) {
                    kept.add(project(p, m));
                }
            }
            b.clearField(results);
            for (com.google.protobuf.Message m : kept) {
                b.addRepeatedField(results, m);
            }
            b.setField(b.getDescriptorForType().findFieldByName("total_count"), kept.size());
            return (R) b.build();
        }

        /** An observer that filters every frame before passing it on. */
        static <T> StreamObserver<T> filtering(StreamObserver<T> delegate, java.util.function.UnaryOperator<T> filter) {
            return new StreamObserver<T>() {
                @Override
                public void onNext(T value) {
                    delegate.onNext(filter.apply(value));
                }

                @Override
                public void onError(Throwable t) {
                    delegate.onError(t);
                }

                @Override
                public void onCompleted() {
                    delegate.onCompleted();
                }
            };
        }
    }
`, resolvers.String(), proto.SystemNamespace, proto.TenantNamespacePrefix)
}
