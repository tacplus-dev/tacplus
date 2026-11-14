// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// javaServesRotatingSibling reports whether a generated Java server wrapper
// also serves the service's <Svc>Rotating sibling (tacplus#322), and whether
// a generated Java S2S client of it rotates its streams: every
// service-to-service service with a streaming rpc whose calls carry tokens.
// The same rule as Go's goServesRotatingSibling: a c2d service has no tokens
// (its socket is the boundary), and an auth-disabled service has none to
// expire, so neither needs rotation.
func javaServesRotatingSibling(service *parser.ServiceInfo) bool {
	if service == nil || !service.IsS2S || service.IsC2D || service.AuthDisabled {
		return false
	}
	for _, m := range service.Methods {
		if !m.IsMcp && (m.ClientStreaming || m.ServerStreaming) {
			return true
		}
	}
	return false
}

// javaRotationAdmit renders the wrapper's rotatingService and rotationAdmit:
// the rotation chassis' Admit for this service (StreamRotation.Admit). It
// applies, per streaming method, exactly the check the method's own wrapper
// applies at open (generateJavaGRPCWrapperSurface), so a rotation passes the
// same (spec.rpc) policy the opening leg's call did. The Java twin of Go's
// goRotationAdmit:
//
//   - an opening leg only authenticates (signature, audience, exp, the tenant
//     door); the wrapper then runs the method's full check on the handler's
//     call, as it does on the plain method;
//   - a rotation runs the full check: authorize with the stream's request
//     (server streaming) or none (a request stream), authenticate only where
//     the wrapper authorizes message by message, the auth_any_user check;
//   - an auth-disabled method carries no token, so it has nothing to rotate
//     onto: a rotation is refused.
//
// RPC_FLAG_ANY_TENANT methods authenticate under their method key, as the
// wrapper's checkAuthenticateAnyTenant does, so the tenant door opens for
// them on a leg exactly as on the plain method.
func javaRotationAdmit(service *parser.ServiceInfo, servicePackage, parentDir, authorizerFQN, rotationFQN, tokenWrapperFQN string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `    // ==================== Rotating sibling (tacplus#322) ====================

    /**
     * The rotating sibling of %[1]s, %[1]sRotating (tacplus#322): the same
     * streaming methods, whose streams the generated clients carry across token
     * lifetimes (make-before-break, sys/rotation.proto). It runs this wrapper's
     * own methods on a logical stream, so a handler sees one stream, one
     * StreamObserver pair and one Context for its whole life.
     */
    public io.grpc.ServerServiceDefinition rotatingService() {
        return %[2]s.serve(bindService(), this::rotationAdmit);
    }

    /**
     * Admits a leg of %[1]sRotating: an opening leg is authenticated, and a
     * rotation must pass the method's own open-time policy.
     */
    private %[3]s rotationAdmit(String method, Object first, boolean rotating) throws Exception {
        switch (method) {`, service.Name, rotationFQN, tokenWrapperFQN)
	for _, m := range service.Methods {
		if m.IsMcp || !(m.ClientStreaming || m.ServerStreaming) {
			continue
		}
		authMethod := javaMethodAuthKey(servicePackage, service.Name, m.Name, parentDir)
		anyTenant := "null"
		if m.AnyTenant {
			anyTenant = fmt.Sprintf("%q", authMethod)
		}
		fmt.Fprintf(&b, "\n            case %q:", m.Name)
		if m.AuthDisabled {
			b.WriteString(`
                // Auth is disabled: no token, no exp, nothing to rotate onto.
                if (rotating) {
                    throw io.grpc.Status.PERMISSION_DENIED.asRuntimeException();
                }
                return null;`)
			continue
		}
		fmt.Fprintf(&b, `
                if (!rotating) {
                    return rotationAuthenticate(%s, null);
                }`, anyTenant)
		switch {
		case m.AuthAnyUser:
			target := "null"
			if t := strings.TrimSpace(m.TargetNamespaceField); t != "" {
				target = fmt.Sprintf("%q", t)
			}
			fmt.Fprintf(&b, "\n                return rotationAuthenticate(%s, %s);", anyTenant, target)
		case m.ClientStreaming && m.TargetNamespaceField != "":
			// The wrapper authorizes every message, with the token of the leg
			// that carries it.
			fmt.Fprintf(&b, "\n                return rotationAuthenticate(%s, null);", anyTenant)
		case m.ClientStreaming:
			fmt.Fprintf(&b, "\n                return authorizer.authorize(%q, null);", authMethod)
		default:
			fmt.Fprintf(&b, `
                if (first == null) {
                    // The stream's request has not reached the handler yet: retryable.
                    throw io.grpc.Status.UNAVAILABLE.asRuntimeException();
                }
                return authorizer.authorize(%q, first);`, authMethod)
		}
	}
	fmt.Fprintf(&b, `
            default:
                throw io.grpc.Status.UNIMPLEMENTED.asRuntimeException();
        }
    }

    /**
     * The token-only check of a leg, under an RPC_FLAG_ANY_TENANT method's key
     * when it has one: signature, audience, exp, the tenant door, and the fixed
     * namespace target when there is one.
     */
    private %[2]s rotationAuthenticate(String anyTenantMethod, String targetNamespace) throws Exception {
        io.grpc.Context ctx = anyTenantMethod == null ? io.grpc.Context.current()
            : io.grpc.Context.current().withValue(%[1]s.ANY_TENANT_METHOD_CTX_KEY, anyTenantMethod);
        io.grpc.Context prev = ctx.attach();
        try {
            String authHeader = %[1]s.AUTH_HEADER_CTX_KEY.get();
            return targetNamespace == null
                ? authorizer.authenticateWithToken(authHeader)
                : authorizer.authenticateAtOrBelowWithToken(authHeader, targetNamespace);
        } finally {
            ctx.detach(prev);
        }
    }
`, authorizerFQN, tokenWrapperFQN)
	return b.String()
}
