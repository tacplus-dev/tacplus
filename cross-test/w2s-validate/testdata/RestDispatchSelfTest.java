// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Runtime coverage for the generated Java REST dispatch plane, in the
// main()-style the other generated Java self-tests use.
//
// W2S REQUEST (ingress) validation: a real gRPC backend serves the generated
// CatalogService; the generated RestDispatch.dispatch / dispatchStream are
// driven directly (no hand-rolled dispatch logic) with both an invalid and a
// valid body. The invalid body must be rejected BEFORE the backend is reached;
// the valid body must reach it. The backend hit counters are what make the
// rejection meaningful.
//
// (spec.rest_browser): a browser form target whose form-urlencoded body must
// reach the backend as the request message, and whose response must send the
// browser on with a 303. The generated ROUTES entry, formBodyToJson, dispatch
// and redirectResponse are driven in the order a host's REST handler calls
// them. Nothing here re-implements them.

package com.example.w2svalidate.model.s2sm.restdispatch;

import com.example.w2svalidate.model.types.services.catalog.Catalog;
import com.example.w2svalidate.model.types.services.catalog.CatalogServiceGrpc;
import com.example.w2svalidate.model.types.services.signin.Signin;
import com.example.w2svalidate.model.types.services.signin.SigninServiceGrpc;
import io.grpc.Channel;
import io.grpc.Grpc;
import io.grpc.InsecureChannelCredentials;
import io.grpc.InsecureServerCredentials;
import io.grpc.ManagedChannel;
import io.grpc.Server;
import io.grpc.stub.StreamObserver;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;

public class RestDispatchSelfTest {
    private static int failures = 0;

    private static void check(boolean ok, String message) {
        if (!ok) {
            System.err.println("FAIL: " + message);
            failures++;
        }
    }

    private static final String UNARY_PATH = "/catalog.CatalogService/GetItem";
    private static final String STREAM_PATH = "/catalog.CatalogService/WatchItems";
    private static final String FORM_PATH = "/signin.SigninService/BrokerFormCallback";
    private static final String INVALID_BODY = "{\"owner\":\"not-an-email\"}";
    private static final String VALID_BODY = "{\"owner\":\"owner@example.com\"}";
    private static final String REDIRECT_URL = "https://app.example/signin/done?step=2&from=apple";
    private static final String FORM = "application/x-www-form-urlencoded";

    /** A recorded HTTP answer: what the dispatch plane wrote. */
    static final class Recorder implements RestDispatch.HttpResponse {
        int status = 0;
        final Map<String, String> headers = new LinkedHashMap<>();
        final StringBuilder body = new StringBuilder();
        @Override public void status(int code) { status = code; }
        @Override public void header(String name, String value) { headers.put(name, value); }
        @Override public void write(String chunk) { body.append(chunk); }
        @Override public void flush() {}
    }

    static final class RecordingCatalog extends CatalogServiceGrpc.CatalogServiceImplBase {
        final AtomicInteger unaryHits = new AtomicInteger();
        final AtomicInteger streamHits = new AtomicInteger();

        @Override
        public void getItem(Catalog.GetItemRequest request, StreamObserver<Catalog.GetItemResponse> observer) {
            unaryHits.incrementAndGet();
            observer.onNext(Catalog.GetItemResponse.newBuilder().setOwner(request.getOwner()).build());
            observer.onCompleted();
        }

        @Override
        public void watchItems(Catalog.GetItemRequest request, StreamObserver<Catalog.GetItemResponse> observer) {
            streamHits.incrementAndGet();
            observer.onNext(Catalog.GetItemResponse.newBuilder().setOwner(request.getOwner()).build());
            observer.onCompleted();
        }
    }

    /**
     * Answers every callback with REDIRECT_URL, except a request whose state
     * is "no-redirect", which it answers with no URL at all. Keeps the last
     * request so a test can see exactly what the form body decoded into.
     */
    static final class RecordingSignin extends SigninServiceGrpc.SigninServiceImplBase {
        final AtomicReference<Signin.FormCallbackRequest> last = new AtomicReference<>();

        @Override
        public void brokerFormCallback(Signin.FormCallbackRequest request, StreamObserver<Signin.FormCallbackResponse> observer) {
            last.set(request);
            Signin.FormCallbackResponse.Builder response = Signin.FormCallbackResponse.newBuilder();
            if ("bad-redirect".equals(request.getState())) {
                response.setRedirectUrl("https://app.example/\r\nSet-Cookie: x=1");
            } else if (!"no-redirect".equals(request.getState())) {
                response.setRedirectUrl(REDIRECT_URL);
            }
            observer.onNext(response.build());
            observer.onCompleted();
        }
    }

    /** Hands every service the one loopback channel and no auth. */
    static final class FixtureDispatcher implements RestDispatch.Dispatcher {
        private final Channel channel;
        FixtureDispatcher(Channel channel) { this.channel = channel; }
        @Override public Channel channel(String serviceId) { return channel; }
        @Override public String authHeader(String serviceId) { return ""; }
    }

    static final class Backend implements AutoCloseable {
        final Server server;
        final ManagedChannel channel;
        final FixtureDispatcher dispatcher;

        Backend(io.grpc.BindableService service) throws Exception {
            server = Grpc.newServerBuilderForPort(0, InsecureServerCredentials.create()).addService(service).build().start();
            channel = Grpc.newChannelBuilder("127.0.0.1:" + server.getPort(), InsecureChannelCredentials.create()).build();
            dispatcher = new FixtureDispatcher(channel);
        }

        @Override public void close() {
            channel.shutdownNow();
            server.shutdownNow();
        }
    }

    private static RestDispatch.RouteInfo route(String grpcPath) {
        for (RestDispatch.RouteInfo r : RestDispatch.ROUTES) {
            if (r.grpcPath.equals(grpcPath)) {
                return r;
            }
        }
        throw new IllegalStateException("no generated route for " + grpcPath);
    }

    /** One request through the generated seams, in a REST handler's order. */
    private static Recorder post(FixtureDispatcher dispatcher, String contentType, String body) {
        Recorder rec = new Recorder();
        RestDispatch.RouteInfo r = route(FORM_PATH);
        String json;
        try {
            json = RestDispatch.formBodyToJson(r, contentType, body);
        } catch (RestDispatch.HttpError e) {
            e.write(rec);
            return rec;
        }
        String response;
        try {
            response = RestDispatch.dispatch(dispatcher, r.grpcPath, json, "req");
        } catch (RestDispatch.HttpError e) {
            e.write(rec);
            return rec;
        }
        if (!RestDispatch.redirectResponse(r, response, rec)) {
            rec.header("Content-Type", "application/json");
            rec.status(200);
            rec.write(response);
        }
        return rec;
    }

    private static void checkSeeOther(Recorder rec, String what) {
        check(rec.status == 303, what + ": browser form target did not answer 303: " + rec.status + " " + rec.body);
        check(REDIRECT_URL.equals(rec.headers.get("Location")), what + ": 303 Location is not the response's redirect_url: " + rec.headers.get("Location"));
        check("no-store".equals(rec.headers.get("Cache-Control")), what + ": 303 must not be cached");
    }

    // ---- W2S request validation ----

    private static void testUnaryDispatchValidatesRequestBeforeForwarding() throws Exception {
        RecordingCatalog backend = new RecordingCatalog();
        try (Backend b = new Backend(backend)) {
            boolean rejected = false;
            try {
                RestDispatch.dispatch(b.dispatcher, UNARY_PATH, INVALID_BODY, "req-invalid");
            } catch (RestDispatch.HttpError e) {
                rejected = true;
                check(e.status == 400, "invalid request rejected with wrong status: " + e.status + " " + e.body);
                check(e.body.contains("request validation failed"), "invalid request rejected for the wrong reason: " + e.body);
            }
            check(rejected, "invalid request body must be rejected, not forwarded");
            check(backend.unaryHits.get() == 0, "invalid request reached the backend service");

            // Positive control: the same seam must still forward a valid request.
            String ok = RestDispatch.dispatch(b.dispatcher, UNARY_PATH, VALID_BODY, "req-valid");
            check(ok.contains("owner@example.com"), "valid response did not carry the request owner: " + ok);
            check(backend.unaryHits.get() == 1, "valid request did not reach the backend service");
        }
    }

    private static void testStreamDispatchValidatesRequestBeforeForwarding() throws Exception {
        RecordingCatalog backend = new RecordingCatalog();
        try (Backend b = new Backend(backend)) {
            Recorder rejected = new Recorder();
            RestDispatch.dispatchStream(b.dispatcher, STREAM_PATH, INVALID_BODY, "sse-invalid", rejected);
            check(rejected.status == 400, "invalid SSE request was not rejected: " + rejected.status + " " + rejected.body);
            check(rejected.body.toString().contains("request validation failed"), "invalid SSE request rejected for the wrong reason: " + rejected.body);
            check(backend.streamHits.get() == 0, "invalid SSE request reached the backend service");

            // Positive control: the same seam must still open a valid stream.
            Recorder ok = new Recorder();
            RestDispatch.dispatchStream(b.dispatcher, STREAM_PATH, VALID_BODY, "sse-valid", ok);
            check(ok.status == 200, "valid SSE request was not accepted: " + ok.status + " " + ok.body);
            check("text/event-stream".equals(ok.headers.get("Content-Type")), "valid SSE request did not answer an event stream");
            String body = ok.body.toString();
            check(body.contains("data: ") && body.contains("owner@example.com"), "valid SSE stream did not carry the request owner: " + body);
            check(backend.streamHits.get() == 1, "valid SSE request did not reach the backend service");
        }
    }

    // ---- (spec.rest_browser) ----

    private static void testRoutesCarryTheAnnotation() {
        RestDispatch.RouteInfo form = route(FORM_PATH);
        check(form.formBody, "rest_browser form_body not emitted into ROUTES");
        // The route table carries the proto3 JSON name in every language; the
        // Java Helpers emit the proto field name, which the lookup also accepts.
        check("redirectUrl".equals(form.redirectField) && "redirect_url".equals(form.redirectFieldProto),
            "rest_browser redirect_field = " + form.redirectField + " / " + form.redirectFieldProto + ", want redirectUrl / redirect_url");
        check(form.authPolicy == RestDispatch.AuthPolicy.NO_AUTH, "RPC_FLAG_PUBLIC_NO_AUTH must plan NO_AUTH");

        RestDispatch.RouteInfo jsonOnly = route(UNARY_PATH);
        check(!jsonOnly.formBody, "a route without rest_browser must stay JSON-only");
        check(jsonOnly.redirectField.isEmpty(), "a route without rest_browser must answer JSON");
        check(jsonOnly.authPolicy == RestDispatch.AuthPolicy.ANY_USER, "RPC_FLAG_AUTH_ANY_USER must plan ANY_USER");
    }

    private static void testFormBodyDecodesIntoTheRequest() throws Exception {
        RecordingSignin backend = new RecordingSignin();
        try (Backend b = new Backend(backend)) {
            // Percent-escapes and '+' decode; user is left out and stays unset.
            Recorder rec = post(b.dispatcher, "application/x-www-form-urlencoded; charset=UTF-8",
                "code=c%2Fd%2Be%3D&state=s+t%26u&id_token=eyJ.a%2Eb");
            checkSeeOther(rec, "form post");
            Signin.FormCallbackRequest got = backend.last.get();
            check(got != null, "form post never reached the backend");
            if (got != null) {
                check("c/d+e=".equals(got.getCode()) && "s t&u".equals(got.getState()) && "eyJ.a.b".equals(got.getIdToken()),
                    "form body decoded wrongly: code=" + got.getCode() + " state=" + got.getState() + " id_token=" + got.getIdToken());
                check(got.getUser().isEmpty(), "a field the form leaves out must stay unset, got user=" + got.getUser());
            }
        }
    }

    private static void testFormBodyRefusesARepeatedField() throws Exception {
        RecordingSignin backend = new RecordingSignin();
        try (Backend b = new Backend(backend)) {
            Recorder rec = post(b.dispatcher, FORM, "code=one&state=s&code=two");
            check(rec.status == 400, "repeated field was not refused: " + rec.status + " " + rec.body);
            check(rec.body.toString().contains("code sent more than once"), "wrong refusal: " + rec.body);
            check(backend.last.get() == null, "a refused form post reached the backend");
        }
    }

    private static void testFormBodyIgnoresAFieldTheRequestLacks() throws Exception {
        // Exactly what the JSON path does with an unknown key: it is dropped,
        // and the declared fields still arrive (an IdP's error= post must not 400).
        RecordingSignin backend = new RecordingSignin();
        try (Backend b = new Backend(backend)) {
            Recorder rec = post(b.dispatcher, FORM, "code=c&error=user_cancelled_authorize");
            checkSeeOther(rec, "unknown field");
            Signin.FormCallbackRequest got = backend.last.get();
            check(got != null && "c".equals(got.getCode()), "declared field did not arrive beside the unknown one");
        }
    }

    private static void testJsonBodyIsUnchanged() throws Exception {
        // A JSON request to a form route takes the JSON path exactly as before.
        String json = "{\"code\":\"c\",\"state\":\"s\"}";
        check(json.equals(RestDispatch.formBodyToJson(route(FORM_PATH), "application/json", json)), "a JSON body must pass through");
        check(json.equals(RestDispatch.formBodyToJson(route(FORM_PATH), null, json)), "a body with no content type must pass through");
        // A form content type on a route without form_body is not decoded either.
        String form = "owner=a%40b.example";
        check(form.equals(RestDispatch.formBodyToJson(route(UNARY_PATH), FORM, form)), "a non-form route must pass its body through");

        RecordingSignin backend = new RecordingSignin();
        try (Backend b = new Backend(backend)) {
            Recorder rec = post(b.dispatcher, "application/json", json);
            checkSeeOther(rec, "JSON post");
            Signin.FormCallbackRequest got = backend.last.get();
            check(got != null && "c".equals(got.getCode()) && "s".equals(got.getState()), "JSON body decoded wrongly");
        }
    }

    private static void testRedirectWithoutAUrlIsABackendFault() throws Exception {
        try (Backend b = new Backend(new RecordingSignin())) {
            Recorder rec = post(b.dispatcher, FORM, "state=no-redirect");
            check(rec.status == 502, "a redirect response without a URL must be a 502: " + rec.status + " " + rec.body);
            check(!rec.headers.containsKey("Location"), "a 502 must not carry a Location");
        }
    }

    private static void testRedirectWithAnInvalidHeaderValueIsABackendFault() throws Exception {
        try (Backend b = new Backend(new RecordingSignin())) {
            Recorder rec = post(b.dispatcher, FORM, "state=bad-redirect");
            check(rec.status == 502, "a redirect URL that is not a header value must be a 502: " + rec.status + " " + rec.body);
            check(!rec.headers.containsKey("Location"), "a 502 must not carry a Location");
        }
    }

    private static void testJsonRouteAnswersJson() {
        Recorder rec = new Recorder();
        check(!RestDispatch.redirectResponse(route(UNARY_PATH), "{\"owner\":\"a@b.example\"}", rec),
            "a route without redirect_field must answer its JSON");
    }

    public static void main(String[] args) throws Exception {
        List<String> ran = new ArrayList<>();
        testUnaryDispatchValidatesRequestBeforeForwarding(); ran.add("unary request validation");
        testStreamDispatchValidatesRequestBeforeForwarding(); ran.add("stream request validation");
        testRoutesCarryTheAnnotation(); ran.add("rest_browser routes");
        testFormBodyDecodesIntoTheRequest(); ran.add("form body decode");
        testFormBodyRefusesARepeatedField(); ran.add("form body repeated field");
        testFormBodyIgnoresAFieldTheRequestLacks(); ran.add("form body unknown field");
        testJsonBodyIsUnchanged(); ran.add("JSON body unchanged");
        testRedirectWithoutAUrlIsABackendFault(); ran.add("redirect without URL");
        testRedirectWithAnInvalidHeaderValueIsABackendFault(); ran.add("redirect with invalid header value");
        testJsonRouteAnswersJson(); ran.add("JSON route answers JSON");
        if (failures > 0) {
            System.err.println("RestDispatchSelfTest: " + failures + " failure(s) across " + ran);
            System.exit(1);
        }
        System.out.println("RestDispatchSelfTest: OK (" + ran.size() + " checks)");
    }
}
