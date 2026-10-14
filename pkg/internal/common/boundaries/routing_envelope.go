// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package boundaries

import (
	"fmt"
	"sort"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// The (spec.record)/(spec.event) s2s_subscribe.routing_only_services resolution.
//
// A topic OWNER names, on its own record/event type, the subset of the already
// authorized auth_services whose generated subscriber Handler takes the
// synthesized <T>RoutingEnvelope instead of the full <T>. The envelope carries
// exactly three strings — delivery_id, recipient, namespace — projected from
// the source type's own annotations:
//
//	(spec.record).key         -> delivery_id
//	(spec.store_principal)    -> recipient
//	(spec.store_namespace)    -> namespace
//
// Resolution is a lookup, not a second opinion — the same shape as
// recordStoreEpochField above, and for the same reason.
//
// THE REFUSAL. A type flagged routing_only_services that cannot fill all three
// slots is a generation error, not a half-empty envelope: an envelope with no
// recipient is a routing table nobody can route with, and a subscriber handed
// one would be narrower AND broken. This is the watch_egress_gen.go posture —
// refuse rather than under-deliver silently.
//
// WHAT THIS IS NOT. A typed boundary, not a memory boundary. The full body
// still arrives on the wire and is still fully decoded in the subscriber's
// process; the generated dispatch projects three fields out of it and drops the
// decoded message. See spec.proto's routing_only_services comment.

// RoutingEnvelopeSuffix is appended to the source type name to name the
// synthesized envelope message (<T>RoutingEnvelope). Spelled once here so the
// proto synthesis pass and the five subscriber generators cannot disagree.
const RoutingEnvelopeSuffix = "RoutingEnvelope"

// RoutingEnvelope is one record/event type's resolved routing-only declaration:
// who gets the narrow view, and which of the type's own fields fill it.
type RoutingEnvelope struct {
	// Services is s2s_subscribe.routing_only_services as declared, sorted.
	Services []string
	// KeyField, PrincipalField and NamespaceField name the SOURCE fields
	// projected into delivery_id, recipient and namespace respectively.
	KeyField       string
	PrincipalField string
	NamespaceField string
}

// Covers reports whether selfPackage is one of the services declared
// routing-only for this type.
func (r RoutingEnvelope) Covers(selfPackage string) bool {
	for _, s := range r.Services {
		if s == selfPackage {
			return true
		}
	}
	return false
}

// ResolveRoutingEnvelope reads msg's s2s_subscribe.routing_only_services and the
// three source fields the envelope projects.
//
// ok is false when msg declares no routing_only_services — the overwhelmingly
// common case, and a pure no-op. err is non-nil only when msg DOES declare one
// and cannot fill the envelope; see the refusal note above.
func ResolveRoutingEnvelope(pkgName string, msg *parser.MessageDescriptor) (RoutingEnvelope, bool, error) {
	services := routingOnlyServices(msg)
	if len(services) == 0 {
		return RoutingEnvelope{}, false, nil
	}

	env := RoutingEnvelope{
		Services:       services,
		KeyField:       msg.Annotations["record_key"].StringValue,
		PrincipalField: fieldWithBoolAnnotation(msg, "store_principal"),
		NamespaceField: fieldWithBoolAnnotation(msg, "store_namespace"),
	}

	var missing []string
	if env.KeyField == "" {
		missing = append(missing, "no (spec.record).key (delivery_id)")
	}
	if env.PrincipalField == "" {
		missing = append(missing, "no (spec.store_principal) field (recipient)")
	}
	if env.NamespaceField == "" {
		missing = append(missing, "no (spec.store_namespace) field (namespace)")
	}
	if len(missing) > 0 {
		return RoutingEnvelope{}, false, fmt.Errorf(
			"%s.%s declares s2s_subscribe.routing_only_services %v but %s: "+
				"the %s%s envelope cannot be filled, and an envelope with an empty "+
				"slot is a routing table nobody can route with — declare the missing "+
				"annotation(s) or drop routing_only_services",
			pkgName, msg.Name, services, strings.Join(missing, "; "),
			msg.Name, RoutingEnvelopeSuffix)
	}

	// The envelope's three slots are proto3 strings. A source field of any other
	// type has no slot to project into, so it is refused for the same reason a
	// missing one is rather than silently widening the envelope.
	for _, pair := range [][2]string{
		{env.KeyField, "delivery_id"},
		{env.PrincipalField, "recipient"},
		{env.NamespaceField, "namespace"},
	} {
		f := findField(msg, pair[0])
		if f == nil {
			return RoutingEnvelope{}, false, fmt.Errorf(
				"%s.%s declares s2s_subscribe.routing_only_services %v but names field %q "+
					"for the envelope's %s slot and no such field exists",
				pkgName, msg.Name, services, pair[0], pair[1])
		}
		if f.Type != "string" || f.IsRepeated || f.IsMap {
			return RoutingEnvelope{}, false, fmt.Errorf(
				"%s.%s declares s2s_subscribe.routing_only_services %v but field %q "+
					"(the envelope's %s slot) is %q, not a singular string: the %s%s "+
					"slots are strings and there is nothing to project a %s into",
				pkgName, msg.Name, services, pair[0], pair[1], f.Type,
				msg.Name, RoutingEnvelopeSuffix, f.Type)
		}
	}

	return env, true, nil
}

// routingOnlyServices returns the routing_only_services declared on msg's
// s2s_subscribe half, sorted and de-duplicated. The list lives on the
// "event_surface" annotation, which parseMessageAnnotations populates from
// whichever of (spec.event)/(spec.record) carries the surface.
func routingOnlyServices(msg *parser.MessageDescriptor) []string {
	surface, ok := msg.Annotations["event_surface"]
	if !ok {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, entry := range surface.EventPublishEntries {
		if entry.MethodS2SSubscribe == nil {
			continue
		}
		for _, svc := range entry.MethodS2SSubscribe.RoutingOnlyServices {
			if svc == "" || seen[svc] {
				continue
			}
			seen[svc] = true
			out = append(out, svc)
		}
	}
	sort.Strings(out)
	return out
}

func fieldWithBoolAnnotation(msg *parser.MessageDescriptor, key string) string {
	for i := range msg.Fields {
		if msg.Fields[i].Annotations[key].BoolValue {
			return msg.Fields[i].Name
		}
	}
	return ""
}

func findField(msg *parser.MessageDescriptor, name string) *parser.FieldDescriptor {
	for i := range msg.Fields {
		if msg.Fields[i].Name == name {
			return &msg.Fields[i]
		}
	}
	return nil
}

// LookupRoutingEnvelope resolves the routing envelope for the message named by
// typeFqn (e.g. "messaging.Message") across every parsed file. Same shape as
// recordStoreEpochField: a lookup by fully-qualified name, "" / not-ok when the
// type is not found or declares no routing_only_services.
func LookupRoutingEnvelope(p *parser.DescriptorParser, typeFqn string) (RoutingEnvelope, bool, error) {
	if typeFqn == "" {
		return RoutingEnvelope{}, false, nil
	}
	for _, fd := range p.GetAllFiles() {
		if fd.Package == "" || !strings.HasPrefix(typeFqn, fd.Package+".") {
			continue
		}
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			if fd.Package+"."+msg.Name != typeFqn {
				continue
			}
			return ResolveRoutingEnvelope(fd.Package, msg)
		}
	}
	return RoutingEnvelope{}, false, nil
}

// ApplyRoutingEnvelope fills entry's RoutingOnly / SourceKeyField /
// SourceStorePrincipalField / SourceStoreNamespaceField from the source type's
// s2s_subscribe.routing_only_services declaration.
//
// One implementation for all five subscriber generators: the narrowing is a
// property of the OWNER's declaration and of who is being generated, and five
// backends resolving that separately is five ways to disagree about one
// boundary. Returns the generation refusal unchanged when the flagged type
// cannot fill its envelope.
func ApplyRoutingEnvelope(p *parser.DescriptorParser, selfPackage string, entry *tactype.SubscriptionEntry) error {
	env, ok, err := LookupRoutingEnvelope(p, entry.SourceTypeFqn)
	if err != nil {
		return err
	}
	if !ok || !env.Covers(selfPackage) {
		return nil
	}
	entry.RoutingOnly = true
	entry.SourceKeyField = env.KeyField
	entry.SourceStorePrincipalField = env.PrincipalField
	entry.SourceStoreNamespaceField = env.NamespaceField
	return nil
}
