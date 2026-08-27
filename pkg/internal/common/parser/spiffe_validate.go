// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"regexp"
	"strings"

	commonspiffe "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/spiffe"
)

// spiffeSegmentRe uses the same grammar data rendered into generated sys/spiffe.
var spiffeSegmentRe = regexp.MustCompile(commonspiffe.SegmentPattern)

// spiffeReservedTags uses the same structural tags rendered into generated sys/spiffe.
var spiffeReservedTags = commonspiffe.ReservedTagSet()

// ValidateSpiffeIdentityAnnotations enforces the SPIFFE identity annotation
// contract (plan D5) at generation time: an annotated SPIFFE field must be
// constructible from declared proto fields and proto-defined constants.
// Generation FAILS on violation — identity is a declared, validated property of
// the model, never inference or hand-formatting.
func ValidateSpiffeIdentityAnnotations(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	var errs []string
	for _, fd := range dp.GetAllFiles() {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			validateSpiffeMessage(fd.Package, msg, &errs)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("spiffe identity annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

func validateSpiffeMessage(pkg string, msg *MessageDescriptor, errs *[]string) {
	where := func(field string) string { return fmt.Sprintf("%s.%s.%s", pkg, msg.Name, field) }

	fieldsByName := make(map[string]*FieldDescriptor, len(msg.Fields))
	for fi := range msg.Fields {
		fieldsByName[msg.Fields[fi].Name] = &msg.Fields[fi]
	}

	for fi := range msg.Fields {
		field := &msg.Fields[fi]

		for _, name := range []string{"identity_namespace", "identity_subject", "identity_spiffe_subject", "identity_spiffe_actor"} {
			if _, ok := field.Annotations[name]; !ok {
				continue
			}
			if field.Type != "string" || field.IsMap {
				*errs = append(*errs, fmt.Sprintf("%s: (spec.%s) requires a string field; got %s",
					where(field.Name), name, field.Type))
			}
		}

		ann, ok := field.Annotations["identity_spiffe_resource"]
		if !ok {
			continue
		}
		res := ann.IdentitySpiffeResource
		if res == nil {
			*errs = append(*errs, fmt.Sprintf("%s: (spec.identity_spiffe_resource) annotation is empty", where(field.Name)))
			continue
		}
		if field.Type != "string" || field.IsMap || field.IsRepeated {
			*errs = append(*errs, fmt.Sprintf("%s: (spec.identity_spiffe_resource) requires a singular string field; got %s",
				where(field.Name), field.Type))
		}

		// kind is a constant single segment, validated against the shared grammar.
		switch {
		case res.Kind == "":
			*errs = append(*errs, fmt.Sprintf("%s: identity_spiffe_resource.kind is required", where(field.Name)))
		case spiffeReservedTags[res.Kind]:
			*errs = append(*errs, fmt.Sprintf("%s: identity_spiffe_resource.kind %q is a reserved SPIFFE tag",
				where(field.Name), res.Kind))
		case !spiffeSegmentRe.MatchString(res.Kind):
			*errs = append(*errs, fmt.Sprintf("%s: identity_spiffe_resource.kind %q violates the segment grammar %s",
				where(field.Name), res.Kind, commonspiffe.SegmentGrammar))
		}

		// namespace_field and id_field must name sibling singular string fields,
		// so the resource identity is always constructible from declared fields.
		for _, ref := range []struct{ label, value string }{
			{"namespace_field", res.NamespaceField},
			{"id_field", res.IdField},
		} {
			if ref.value == "" {
				*errs = append(*errs, fmt.Sprintf("%s: identity_spiffe_resource.%s is required", where(field.Name), ref.label))
				continue
			}
			sibling, ok := fieldsByName[ref.value]
			if !ok {
				*errs = append(*errs, fmt.Sprintf("%s: identity_spiffe_resource.%s %q does not name a field on message %q",
					where(field.Name), ref.label, ref.value, msg.Name))
				continue
			}
			if sibling.Type != "string" || sibling.IsMap || sibling.IsRepeated {
				*errs = append(*errs, fmt.Sprintf("%s: identity_spiffe_resource.%s %q must name a singular string field; got %s",
					where(field.Name), ref.label, ref.value, sibling.Type))
			}
			if sibling.Name == field.Name {
				*errs = append(*errs, fmt.Sprintf("%s: identity_spiffe_resource.%s must not reference the annotated field itself",
					where(field.Name), ref.label))
			}
		}
	}
}
