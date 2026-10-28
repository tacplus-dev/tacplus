// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// Issue #170: a routed rpc ((spec.rpc).route_field) names its target tenant in
// a request field. The gateway routes a Python client's call by that field, so
// the client refuses an empty or malformed tid before anything is sent, with
// the shared INVALID_ARGUMENT ProtoError naming the field. It never fills the
// field in, from the token or anywhere else.

// pyServiceHasRouteField reports whether any rpc of svc is routed.
func pyServiceHasRouteField(svc *parser.ServiceInfo) bool {
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

// pyRouteHelpers is the module-level route helper block a client file with a
// routed rpc carries, spliced onto its imports line; "" for a platform service
// so its file is byte-for-byte what it was. exceptionsImportFrom is the
// module path pyExceptionsImportFrom yields for the file.
func pyRouteHelpers(svc *parser.ServiceInfo, exceptionsImportFrom string) string {
	if !pyServiceHasRouteField(svc) {
		return ""
	}
	return fmt.Sprintf(`import re as _route_re
from %s.exceptions import invalid_argument as _route_invalid_argument

# A tid is exactly 8 characters of [0-9a-z]: no trimming, no case folding.
_ROUTE_TID = _route_re.compile(r"[0-9a-z]{8}")


def _route_tenant(request, field: str) -> str:
    """The target tenant a routed rpc's request names in its route field.

    Refused (INVALID_ARGUMENT) when it is empty or not a tid, before anything
    is sent: the gateway routes the call by this field alone, and it is never
    defaulted from the caller's token.
    """
    value = getattr(request, field, "")
    if not isinstance(value, str) or _ROUTE_TID.fullmatch(value) is None:
        raise _route_invalid_argument(
            f'route field "{field}" must be a tenant id (8 characters of [0-9a-z]), got {value!r}'
        )
    return value


class _RoutePin:
    """A routed stream's tenant, fixed by its first message: every message must
    name a tid, and the same one, or it is refused before it is sent."""

    def __init__(self, field: str) -> None:
        self._field = field
        self._tenant = None

    def check(self, request) -> str:
        tid = _route_tenant(request, self._field)
        if self._tenant is None:
            self._tenant = tid
        elif tid != self._tenant:
            raise _route_invalid_argument(
                f'route field "{self._field}": the stream is routed to tenant {self._tenant!r}, this message names {tid!r}'
            )
        return tid


async def _route_pinned_requests(requests, field: str):
    pin = _RoutePin(field)
    async for request in requests:
        pin.check(request)
        yield request
`, exceptionsImportFrom)
}

// pyRouteCheck is the statement a routed rpc's client method runs on its
// request before sending ("" for a platform rpc).
func pyRouteCheck(method *parser.MethodInfo, varName, indent string) string {
	if method.RouteField == nil {
		return ""
	}
	return fmt.Sprintf("%s_route_tenant(%s, %q)\n", indent, varName, method.RouteField.Field)
}
