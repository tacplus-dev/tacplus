package parser

import (
	"fmt"
	"strings"
)

// ValidateAuthAnnotations enforces cross-language auth annotation semantics.
func ValidateAuthAnnotations(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	messages := make(map[string]*MessageDescriptor)
	for _, fd := range dp.GetAllFiles() {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			messages[fd.Package+"."+msg.Name] = msg
			messages[msg.Name] = msg
		}
	}
	var errs []string
	for _, fd := range dp.GetAllFiles() {
		for si := range fd.Services {
			svc := &fd.Services[si]
			for mi := range svc.Methods {
				m := &svc.Methods[mi]
				target := strings.TrimSpace(m.TargetNamespaceField)
				if target == "" {
					continue
				}
				if m.AuthAnyUser {
					if target != m.TargetNamespaceField || !strings.HasPrefix(target, "/") {
						errs = append(errs, fmt.Sprintf("%s.%s: auth_any_user target_namespace_field must be empty or an absolute namespace literal such as \"/\" or \"/tenant\"; got %q",
							svc.Name, m.Name, m.TargetNamespaceField))
					}
					continue
				}
				if strings.HasPrefix(target, "/") {
					errs = append(errs, fmt.Sprintf("%s.%s: target_namespace_field on entitled methods must name a request string field, not a namespace literal; got %q",
						svc.Name, m.Name, m.TargetNamespaceField))
					continue
				}
				msg := messages[strings.TrimPrefix(m.InputType, ".")]
				if msg == nil {
					errs = append(errs, fmt.Sprintf("%s.%s: target_namespace_field %q cannot be validated because input message %q was not found",
						svc.Name, m.Name, m.TargetNamespaceField, m.InputType))
					continue
				}
				var field *FieldDescriptor
				for fi := range msg.Fields {
					if msg.Fields[fi].Name == target {
						field = &msg.Fields[fi]
						break
					}
				}
				if field == nil {
					errs = append(errs, fmt.Sprintf("%s.%s: target_namespace_field %q not found on input message %q",
						svc.Name, m.Name, m.TargetNamespaceField, m.InputType))
					continue
				}
				if field.Type != "string" {
					errs = append(errs, fmt.Sprintf("%s.%s: target_namespace_field %q must name a string field on input message %q; got %s",
						svc.Name, m.Name, m.TargetNamespaceField, m.InputType, field.Type))
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("auth annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
