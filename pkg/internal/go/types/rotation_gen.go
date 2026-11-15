// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// goServesRotatingSibling reports whether a generated Go server wrapper also
// serves the service's <Svc>Rotating sibling (tacplus#322): every
// service-to-service service with a streaming rpc whose calls carry tokens.
// A c2d service has no tokens (its socket is the boundary), and an
// auth-disabled service has none to expire, so neither needs rotation.
func goServesRotatingSibling(service *parser.ServiceInfo) bool {
	if !service.IsS2S || service.IsC2D || service.AuthDisabled {
		return false
	}
	for _, m := range service.Methods {
		if !m.IsMcp && (m.ClientStreaming || m.ServerStreaming) {
			return true
		}
	}
	return false
}

// goRotationAdmit renders the wrapper's rotationAdmit: the rotation chassis'
// Admit for this service. It applies, per streaming method, exactly the check
// the method's own wrapper applies at open (generateGRPCWrapper), so a
// rotation passes the same (spec.rpc) policy the opening leg's call did:
//
//   - an opening leg only authenticates (signature, audience, exp, the tenant
//     door); the wrapper then runs the method's full check on the handler's
//     call, as it does on the plain method;
//   - a rotation runs the full check: authorize with the stream's request
//     (server streaming) or none (a request stream), authenticate only where
//     the wrapper authorizes message by message, the auth_any_user check;
//   - an auth-disabled method carries no token, so it has nothing to rotate
//     onto: a rotation is refused.
func goRotationAdmit(service *parser.ServiceInfo, parentDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `
// rotationAdmit admits a leg of %sRotating, the rotating sibling of %s
// (tacplus#322): an opening leg is authenticated, and a rotation must pass the
// method's own open-time policy.
func (p *{{.ServerName}}) rotationAdmit(ctx context.Context, method string, first any, rotating bool) (*wrapperToken.TokenWrapper, error) {
	switch method {`, service.Name, service.Name)
	for _, m := range service.Methods {
		if m.IsMcp || !(m.ClientStreaming || m.ServerStreaming) {
			continue
		}
		fullMethod := fmt.Sprintf(`"/%s" + types.%s_%s_FullMethodName`, parentDir, service.Name, m.Name)
		fmt.Fprintf(&b, "\n\tcase %q:", m.Name)
		if m.AuthDisabled {
			b.WriteString(`
		// Auth is disabled: no token, no exp, nothing to rotate onto.
		if rotating {
			return nil, status.Error(codes.PermissionDenied, "")
		}
		return nil, nil`)
			continue
		}
		b.WriteString(`
		if !rotating {
			return p.authenticate(ctx)
		}`)
		switch {
		case m.AuthAnyUser:
			if target := strings.TrimSpace(m.TargetNamespaceField); target != "" {
				fmt.Fprintf(&b, "\n\t\treturn p.authenticateAtOrBelow(ctx, %q)", target)
			} else {
				b.WriteString("\n\t\treturn p.authenticate(ctx)")
			}
		case m.ClientStreaming && m.TargetNamespaceField != "":
			// The wrapper authorizes every message, with the token of the leg
			// that carries it.
			b.WriteString("\n\t\treturn p.authenticate(ctx)")
		case m.ClientStreaming:
			fmt.Fprintf(&b, "\n\t\treturn p.authorize(ctx, %s, nil)", fullMethod)
		default:
			fmt.Fprintf(&b, `
		if first == nil {
			// The stream's request has not reached the handler yet: retryable.
			return nil, status.Error(codes.Unavailable, "")
		}
		return p.authorize(ctx, %s, first)`, fullMethod)
		}
	}
	b.WriteString(`
	}
	return nil, status.Error(codes.Unimplemented, "")
}
`)
	return b.String()
}
