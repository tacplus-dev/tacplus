// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0
//
// Runtime coverage for (spec.rest_browser) in the generated Go REST dispatch
// plane: a browser form target (an identity provider's form_post callback)
// whose form-urlencoded body must reach the backend as the request message,
// and whose response must send the browser on with a 303. A real gRPC backend
// serves the generated SigninService; the generated Routes entry,
// FormBodyToJSON, Dispatch and RedirectResponse are driven in the order a
// host's REST handler calls them. Nothing here re-implements them.

package restdispatch_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"

	restdispatch "example.com/w2svalidate/model/s2sm/rest_dispatch"
	signin "example.com/w2svalidate/model/types/services/signin"
)

const (
	formPath     = "/signin.SigninService/BrokerFormCallback"
	jsonOnlyPath = "/catalog.CatalogService/GetItem"
	redirectURL  = "https://app.example/signin/done?step=2&from=apple"
	formType     = "application/x-www-form-urlencoded"
)

// recordingSignin answers every callback with redirectURL, except a request
// whose state is "no-redirect", which it answers with no URL at all. Keeps
// the last request so a test can see exactly what the form body decoded into.
type recordingSignin struct {
	signin.UnimplementedSigninServiceServer
	mu   sync.Mutex
	last *signin.FormCallbackRequest
}

func (s *recordingSignin) BrokerFormCallback(_ context.Context, req *signin.FormCallbackRequest) (*signin.FormCallbackResponse, error) {
	s.mu.Lock()
	s.last = req
	s.mu.Unlock()
	if req.GetState() == "no-redirect" {
		return &signin.FormCallbackResponse{}, nil
	}
	if req.GetState() == "bad-redirect" {
		return &signin.FormCallbackResponse{RedirectUrl: "https://app.example/\r\nSet-Cookie: x=1"}, nil
	}
	return &signin.FormCallbackResponse{RedirectUrl: redirectURL}, nil
}

func (s *recordingSignin) lastRequest() *signin.FormCallbackRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func startSignin(t *testing.T) (fixtureDispatcher, *recordingSignin) {
	t.Helper()
	backend := &recordingSignin{}
	conn := serve(t, func(s *grpc.Server) { signin.RegisterSigninServiceServer(s, backend) })
	return fixtureDispatcher{conn: conn}, backend
}

func route(t *testing.T, grpcPath string) *restdispatch.RouteInfo {
	t.Helper()
	for i := range restdispatch.Routes {
		if restdispatch.Routes[i].GrpcPath == grpcPath {
			return &restdispatch.Routes[i]
		}
	}
	t.Fatalf("no generated route for %s", grpcPath)
	return nil
}

// post drives one request through the generated seams, in a REST handler's order.
func post(t *testing.T, dispatcher fixtureDispatcher, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := route(t, formPath)
	jsonBody, herr := restdispatch.FormBodyToJSON(r, contentType, body)
	if herr != nil {
		herr.Write(rec)
		return rec
	}
	response, herr := restdispatch.Dispatch(context.Background(), dispatcher, r.GrpcPath, jsonBody, "req")
	if herr != nil {
		herr.Write(rec)
		return rec
	}
	if !restdispatch.RedirectResponse(r, response, rec) {
		rec.Header().Set("Content-Type", "application/json")
		_, _ = rec.WriteString(response)
	}
	return rec
}

func assertSeeOther(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("browser form target did not answer 303: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != redirectURL {
		t.Fatalf("303 Location is not the response's redirect_url: %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("303 must not be cached: Cache-Control %q", got)
	}
}

func TestGoRestBrowserRoutesCarryTheAnnotation(t *testing.T) {
	form := route(t, formPath)
	if !form.FormBody {
		t.Fatal("rest_browser form_body not emitted into Routes")
	}
	// The route table carries the proto3 JSON name in every language; the Go
	// JSON helpers emit the proto field name, which the lookup also accepts.
	if form.RedirectField != "redirectUrl" || form.RedirectFieldProto != "redirect_url" {
		t.Fatalf("rest_browser redirect_field = %q / %q, want redirectUrl / redirect_url", form.RedirectField, form.RedirectFieldProto)
	}

	jsonOnly := route(t, jsonOnlyPath)
	if jsonOnly.FormBody {
		t.Fatal("a route without rest_browser must stay JSON-only")
	}
	if jsonOnly.RedirectField != "" {
		t.Fatalf("a route without rest_browser must answer JSON, got redirect field %q", jsonOnly.RedirectField)
	}
}

func TestGoRestBrowserFormBodyDecodesIntoTheRequest(t *testing.T) {
	dispatcher, backend := startSignin(t)

	// Percent-escapes and '+' decode; user is left out and stays unset.
	body := []byte("code=c%2Fd%2Be%3D&state=s+t%26u&id_token=eyJ.a%2Eb")
	rec := post(t, dispatcher, "application/x-www-form-urlencoded; charset=UTF-8", body)
	assertSeeOther(t, rec)

	got := backend.lastRequest()
	if got == nil {
		t.Fatal("form post never reached the backend")
	}
	if got.GetCode() != "c/d+e=" || got.GetState() != "s t&u" || got.GetIdToken() != "eyJ.a.b" {
		t.Fatalf("form body decoded wrongly: code=%q state=%q id_token=%q", got.GetCode(), got.GetState(), got.GetIdToken())
	}
	if got.GetUser() != "" {
		t.Fatalf("a field the form leaves out must stay unset, got user=%q", got.GetUser())
	}
}

func TestGoRestBrowserFormBodyRefusesARepeatedField(t *testing.T) {
	dispatcher, backend := startSignin(t)

	rec := post(t, dispatcher, formType, []byte("code=one&state=s&code=two"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("repeated field was not refused: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "code sent more than once") {
		t.Fatalf("wrong refusal: %s", rec.Body.String())
	}
	if backend.lastRequest() != nil {
		t.Fatal("a refused form post reached the backend")
	}
}

func TestGoRestBrowserFormBodyIgnoresAFieldTheRequestLacks(t *testing.T) {
	// Exactly what the JSON path does with an unknown key: it is dropped, and
	// the declared fields still arrive (an IdP's error= post must not 400).
	dispatcher, backend := startSignin(t)

	rec := post(t, dispatcher, formType, []byte("code=c&error=user_cancelled_authorize"))
	assertSeeOther(t, rec)
	got := backend.lastRequest()
	if got == nil {
		t.Fatal("form post never reached the backend")
	}
	if got.GetCode() != "c" {
		t.Fatalf("declared field did not arrive beside the unknown one: code=%q", got.GetCode())
	}
}

func TestGoRestBrowserJSONBodyIsUnchanged(t *testing.T) {
	// A JSON request to a form route takes the JSON path exactly as before.
	jsonBody := []byte(`{"code":"c","state":"s"}`)
	passed, herr := restdispatch.FormBodyToJSON(route(t, formPath), "application/json", jsonBody)
	if herr != nil || string(passed) != string(jsonBody) {
		t.Fatalf("a JSON body must pass through: %v %s", herr, passed)
	}
	none, herr := restdispatch.FormBodyToJSON(route(t, formPath), "", jsonBody)
	if herr != nil || string(none) != string(jsonBody) {
		t.Fatalf("a body with no content type must pass through: %v %s", herr, none)
	}

	// A form content type on a route without form_body is not decoded either.
	form := []byte("owner=a%40b.example")
	untouched, herr := restdispatch.FormBodyToJSON(route(t, jsonOnlyPath), formType, form)
	if herr != nil || string(untouched) != string(form) {
		t.Fatalf("a non-form route must pass its body through: %v %s", herr, untouched)
	}

	dispatcher, backend := startSignin(t)
	rec := post(t, dispatcher, "application/json", jsonBody)
	assertSeeOther(t, rec)
	got := backend.lastRequest()
	if got == nil {
		t.Fatal("JSON post never reached the backend")
	}
	if got.GetCode() != "c" || got.GetState() != "s" {
		t.Fatalf("JSON body decoded wrongly: code=%q state=%q", got.GetCode(), got.GetState())
	}
}

func TestGoRestBrowserRedirectWithoutAURLIsABackendFault(t *testing.T) {
	dispatcher, _ := startSignin(t)

	rec := post(t, dispatcher, formType, []byte("state=no-redirect"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("a redirect response without a URL must be a 502: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "" {
		t.Fatal("a 502 must not carry a Location")
	}
}

func TestGoRestBrowserRedirectWithAnInvalidHeaderValueIsABackendFault(t *testing.T) {
	dispatcher, _ := startSignin(t)

	rec := post(t, dispatcher, formType, []byte("state=bad-redirect"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("a redirect URL that is not a header value must be a 502: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "" {
		t.Fatal("a 502 must not carry a Location")
	}
}

func TestGoRestBrowserJSONRouteAnswersJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	if restdispatch.RedirectResponse(route(t, jsonOnlyPath), `{"owner":"a@b.example"}`, rec) {
		t.Fatal("a route without redirect_field must answer its JSON")
	}
}
