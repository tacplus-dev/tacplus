// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"

	spec "gitlab.com/seabeam/schematic/tacplus/target/proto/spec/sys"
)

// RestBrowserDescriptor carries a method's (spec.rest_browser): its REST route
// is a browser form target. Nil on a MethodDescriptor/MethodInfo means the rpc
// declares none.
//
// The redirect field is resolved here, against the response descriptor, so the
// generators emit the key the REST JSON response actually carries instead of
// each re-deriving a field's JSON spelling.
type RestBrowserDescriptor struct {
	FormBody bool // the route accepts an application/x-www-form-urlencoded body
	// RedirectField is the response field the route redirects to, as declared;
	// empty when the route answers JSON as usual.
	RedirectField string
	// RedirectJSONName is RedirectField's proto3 JSON name, the key it carries
	// in the REST JSON response. Empty when RedirectField is.
	RedirectJSONName string
}

// restBrowserDescriptor resolves and validates one declared rest_browser
// against the rpc's request and response messages. Every failure is a
// generation-time refusal: a redirect field that is not a URL string would
// otherwise only show up as a browser stranded on a 502.
func restBrowserDescriptor(method protoreflect.MethodDescriptor, rb *spec.RestBrowser) (*RestBrowserDescriptor, error) {
	if !rb.GetFormBody() && rb.GetRedirectField() == "" {
		return nil, fmt.Errorf("%s declares (spec.rest_browser) with neither form_body nor redirect_field; it changes nothing, so remove it",
			method.FullName())
	}
	if method.IsStreamingClient() || method.IsStreamingServer() {
		return nil, fmt.Errorf("%s declares (spec.rest_browser) on a streaming rpc; a browser form target is one request answered by one response",
			method.FullName())
	}
	d := &RestBrowserDescriptor{FormBody: rb.GetFormBody()}
	if name := rb.GetRedirectField(); name != "" {
		output := method.Output()
		fd := output.Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			return nil, fmt.Errorf("%s declares (spec.rest_browser).redirect_field %q, but its response %s has no top-level field of that name",
				method.FullName(), name, output.FullName())
		}
		if fd.IsList() || fd.IsMap() || fd.Kind() != protoreflect.StringKind {
			return nil, fmt.Errorf("%s declares (spec.rest_browser).redirect_field %q, but %s.%s is not a singular string; the redirect Location is one URL",
				method.FullName(), name, output.FullName(), name)
		}
		d.RedirectField = name
		d.RedirectJSONName = fd.JSONName()
	}
	return d, nil
}
