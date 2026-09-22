// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// javaRequiresDeviceGate renders the (spec.rpc).requires_device check for a
// unary method, or "" when the method declares none. It sits between caller
// extraction and the handler call — the same seam the auth token is extracted
// at — because it reads two more metadata keys the same way and needs the
// caller the auth check just verified.
//
// The approval id and the device assertion are metadata, so they are physically
// outside the request message and therefore outside the hashed bytes, which is
// kernel's own constraint. The hash comes from the shared chassis
// canonicalization, the same function the caller-side generated helper calls —
// not a second implementation that could drift from it.
func javaRequiresDeviceGate(method *parser.MethodInfo, servicePackage, serviceName string) string {
	if method == nil || !method.RequiresDevice {
		return ""
	}
	return fmt.Sprintf(`
            // (spec.rpc).requires_device: the device-only approval gate. Any
            // failure refuses the rpc — the handler below is never reached.
            Approval.Presented __approval = Approval.fromIncomingContext();
            byte[] __requestHash = Approval.requestHash(request);
            brokerApproval("%s", __approval, __requestHash, caller);`, javaRequiresDeviceRpcMethod(servicePackage, serviceName, method.Name))
}

// javaRequiresDeviceRpcMethod is the rpc_method string the check reports to the
// broker: the PACKAGE-QUALIFIED "pkg.Service/Method". It is deliberately NOT
// javaMethodAuthKey, which leaves a non-vservice unqualified — that key is read
// only by this process's own Authorizer, while rpc_method is a WIRE value kernel
// compares against what the approval was created with. Go's check reports
// qualifiedMethodAuthKey (always package-qualified), so an unqualified Java
// spelling would mean one person's approval is spendable on a Go service and not
// on the Java service serving the same rpc.
func javaRequiresDeviceRpcMethod(servicePackage, serviceName, methodName string) string {
	if servicePackage == "" {
		return serviceName + "/" + methodName
	}
	return servicePackage + "." + serviceName + "/" + methodName
}

// javaRequiresDeviceMapField names the map field reachable from a
// requires_device RPC's request message, as "Message.field", or "" when the
// request contains none. The walk recurses because the canonicalization
// recurses: a map nested three messages down is the same cross-language
// iteration-order nondeterminism as one at the top level.
func javaRequiresDeviceMapField(fullType string, san *javaEgressSanitizer, servicePackage string) string {
	if san == nil || fullType == "" {
		return ""
	}
	seen := make(map[string]bool)
	var walk func(t string) string
	walk = func(t string) string {
		pkg, name := splitJavaEgressFQN(t, servicePackage)
		key := pkg + "\x00" + name
		if seen[key] {
			return ""
		}
		seen[key] = true
		msg, ok := san.msgs[key]
		if !ok {
			return ""
		}
		for i := range msg.Fields {
			if msg.Fields[i].IsMap {
				return name + "." + msg.Fields[i].Name
			}
		}
		for i := range msg.Fields {
			f := &msg.Fields[i]
			if !f.IsMessage {
				continue
			}
			sub := f.TypePackage
			if sub == "" {
				sub = pkg
			}
			if hit := walk(sub + "." + f.Type); hit != "" {
				return hit
			}
		}
		return ""
	}
	return walk(fullType)
}

// javaGrpcClientAccessorName is the accessor a generated group aggregator gives
// one service's typed client: the service name minus a "Service" suffix, first
// letter lowered. Shared with the (spec.rpc).requires_device broker seam, which
// must spell the accessor the aggregator actually emitted — a second spelling
// here would compile in the generator and fail in javac.
func javaGrpcClientAccessorName(serviceName string) string {
	name := strings.TrimSuffix(serviceName, "Service")
	if name == "" {
		return name
	}
	return strings.ToLower(name[:1]) + name[1:]
}

// javaRequiresDeviceClientHelpers renders the caller-side half of
// (spec.rpc).requires_device for one unary rpc, or "" when the rpc declares
// none. Two seams, because the caller owns two distinct steps and tacplus owns
// neither of them wholly:
//
//   - <method>ApprovalHash gives the caller the request hash it must propose the
//     pending approval with. It is the SAME shared canonicalization the
//     server-side check recomputes from the live request, so the two ends cannot
//     drift; every product hand-rolling this is exactly what it prevents.
//   - <method>WithApproval attaches the approval id and the device assertion as
//     CALL METADATA and makes the ordinary call. Metadata is outside the request
//     message, which is what keeps both values outside the hashed bytes.
//
// Proposing the approval itself (kernel PersonalIdentity.CreatePendingApproval)
// and obtaining the device assertion are the caller's own business — not
// generated here, and deliberately so: an approval is proposed by a person, not
// by a service's client stub.
func javaRequiresDeviceClientHelpers(svc *parser.ServiceInfo, method *parser.MethodInfo, inputType, outputType string) string {
	if method == nil || !method.RequiresDevice {
		return ""
	}
	name := methodNameLowerCamel(method.Name)
	emptyOutput := method.FullOutputType == "google.protobuf.Empty"
	// An Empty-request rpc's generated client method takes no argument, so the
	// guarded call must not pass one either. The hash seam still takes the
	// message: the canonicalization of an Empty is the empty byte string, and
	// the caller's approval is still proposed with it.
	call := fmt.Sprintf("%s(request)", name)
	if method.FullInputType == "google.protobuf.Empty" {
		call = name + "()"
	}
	body := fmt.Sprintf("        return Approval.callWithApproval(approvalId, deviceAssertion, () -> %s);", call)
	ret := outputType
	if emptyOutput {
		ret = "void"
		body = fmt.Sprintf("        Approval.runWithApproval(approvalId, deviceAssertion, () -> %s);", call)
	}
	return fmt.Sprintf(`    /**
     * %sApprovalHash is the request_hash a pending approval for this call must
     * be created with. It is computed by the same shared canonicalization the
     * generated server-side (spec.rpc).requires_device check recomputes from the
     * live request, so the proposed approval and the served call cannot disagree
     * about what was approved.
     */
    public byte[] %sApprovalHash(%s request) {
        return Approval.requestHash(request);
    }

    /**
     * %sWithApproval calls %s.%s carrying a device-only approval: approvalId
     * names the pending approval the person proposed, and deviceAssertion is the
     * serialized WebauthnAssertion of the approving tap. Both ride as call
     * metadata — never as request fields — which is what keeps them outside the
     * hashed request.
     */
    public %s %sWithApproval(%s request, String approvalId, byte[] deviceAssertion) {
%s
    }

`, name, name, inputType, name, svc.Name, method.Name, ret, name, inputType, body)
}
