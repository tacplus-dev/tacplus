// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// Issue #170: a routed rpc ((spec.rpc).route_field) names its target tenant in
// a request field. The gateway routes a TypeScript client's call by that
// field, so the client refuses an empty or malformed tid before anything is
// sent, with an Error coded INVALID_ARGUMENT (3: grpc.status.INVALID_ARGUMENT
// and the generated StatusCode.INVALID_ARGUMENT) naming the field. It never
// fills the field in, from the token or anywhere else.

// tsServiceHasRouteField reports whether any rpc of svc is routed.
func tsServiceHasRouteField(svc *parser.ServiceInfo) bool {
	if svc == nil {
		return false
	}
	for _, m := range svc.Methods {
		if m.RouteField != nil {
			return true
		}
	}
	return false
}

// tsRouteHelpers is the module-level route helper block a client file with a
// routed rpc carries; "" for a platform service so its file is byte-for-byte
// what it was. Function and class declarations only, so the block may sit
// anywhere in the module.
func tsRouteHelpers(svc *parser.ServiceInfo) string {
	if !tsServiceHasRouteField(svc) {
		return ""
	}
	return `
/** tacplusIsTenantId: a tid is exactly 8 characters of [0-9a-z]; no trimming, no case folding. */
function tacplusIsTenantId(value: string): boolean {
    if (value.length !== 8) return false;
    for (let i = 0; i < value.length; i++) {
        const c = value.charCodeAt(i);
        if (!((c >= 48 && c <= 57) || (c >= 97 && c <= 122))) return false;
    }
    return true;
}

/** tacplusRouteInvalid builds the INVALID_ARGUMENT (3) error a refused route field raises. */
function tacplusRouteInvalid(message: string): Error & { code?: number } {
    const err = new Error(message) as Error & { code?: number };
    err.code = 3;
    return err;
}

/**
 * tacplusRouteTenant returns the target tenant a routed rpc's request names in
 * its route field, or throws INVALID_ARGUMENT when it is empty or not a tid,
 * before anything is sent: the gateway routes the call by this field alone,
 * and it is never defaulted from the caller's token.
 */
function tacplusRouteTenant(value: unknown, field: string): string {
    if (typeof value !== 'string' || !tacplusIsTenantId(value)) {
        throw tacplusRouteInvalid(` + "`" + `route field "${field}" must be a tenant id (8 characters of [0-9a-z]), got ${JSON.stringify(value)}` + "`" + `);
    }
    return value;
}

/**
 * TacplusRoutePin is a routed stream's tenant, fixed by its first message:
 * every message must name a tid, and the same one, or it is refused before it
 * is sent.
 */
class TacplusRoutePin {
    private readonly field: string;
    private tenant: string | undefined;

    constructor(field: string) {
        this.field = field;
    }

    check(value: unknown): string {
        const tid = tacplusRouteTenant(value, this.field);
        if (this.tenant === undefined) {
            this.tenant = tid;
        } else if (tid !== this.tenant) {
            throw tacplusRouteInvalid(` + "`" + `route field "${this.field}": the stream is routed to tenant "${this.tenant}", this message names "${tid}"` + "`" + `);
        }
        return tid;
    }
}
`
}

// tsRouteFieldAccess is the generated message property of a method's route
// field on varName (protobuf-es local name, as the validators read it).
func tsRouteFieldAccess(method *parser.MethodInfo, varName string) string {
	return varName + "." + tsProtoFieldName(method.RouteField.Field)
}

// tsRouteCheck is the statement a routed rpc's client method runs on its
// request before sending ("" for a platform rpc).
func tsRouteCheck(method *parser.MethodInfo, varName, indent string) string {
	if method.RouteField == nil {
		return ""
	}
	return fmt.Sprintf("%stacplusRouteTenant(%s, '%s');\n", indent, tsRouteFieldAccess(method, varName), method.RouteField.Field)
}

// tsRoutePinOpen declares a routed stream's tenant pin ("" for a platform rpc).
func tsRoutePinOpen(method *parser.MethodInfo, indent string) string {
	if method.RouteField == nil {
		return ""
	}
	return fmt.Sprintf("%sconst routePin = new TacplusRoutePin('%s');\n", indent, method.RouteField.Field)
}

// tsRoutePinCheck checks one stream message against the pin ("" for a
// platform rpc).
func tsRoutePinCheck(method *parser.MethodInfo, varName, indent string) string {
	if method.RouteField == nil {
		return ""
	}
	return fmt.Sprintf("%sroutePin.check(%s);\n", indent, tsRouteFieldAccess(method, varName))
}

// tsInjectAtMethodStart inserts stmt as the first statement of the generated
// method in code: right after the line that opens its body. Used for the
// client shapes that carry no request-validation slot.
func tsInjectAtMethodStart(code, stmt string) string {
	if stmt == "" {
		return code
	}
	at := strings.Index(code, "async ")
	if at < 0 {
		return code
	}
	nl := strings.Index(code[at:], "{\n")
	if nl < 0 {
		return code
	}
	cut := at + nl + len("{\n")
	return code[:cut] + stmt + code[cut:]
}
