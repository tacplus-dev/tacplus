// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0
//
// Runtime coverage for (spec.rest_browser) in the generated Rust REST dispatch
// module: a browser form target (an identity provider's form_post callback)
// whose form-urlencoded body must reach the backend as the request message,
// and whose response must send the browser on with a 303. A real tonic backend
// serves the generated SigninService; the generated ROUTES entry,
// `form_body_to_json`, `dispatch` and `redirect_response` are driven in the
// order a host's REST handler calls them. Nothing here re-implements them.

use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::http::{header, HeaderValue, StatusCode};
use tonic::transport::{Channel, Endpoint, Server};

use crate::s2sm::rest_dispatch::{self, RestDispatcher, RouteInfo, ROUTES};
use crate::types::services::signin::signin_service_server::{SigninService, SigninServiceServer};
use crate::types::services::signin::{FormCallbackRequest, FormCallbackResponse};

const FORM_PATH: &str = "/signin.SigninService/BrokerFormCallback";
const JSON_ONLY_PATH: &str = "/catalog.CatalogService/GetItem";
const REDIRECT_URL: &str = "https://app.example/signin/done?step=2&from=apple";
const FORM: &str = "application/x-www-form-urlencoded";

/// Answers every callback with REDIRECT_URL, except a request whose state is
/// "no-redirect", which it answers with no URL at all. Keeps the last request
/// so a test can see exactly what the form body decoded into.
#[derive(Clone, Default)]
struct RecordingSignin {
    last: Arc<Mutex<Option<FormCallbackRequest>>>,
}

#[tonic::async_trait]
impl SigninService for RecordingSignin {
    async fn broker_form_callback(
        &self,
        request: tonic::Request<FormCallbackRequest>,
    ) -> Result<tonic::Response<FormCallbackResponse>, tonic::Status> {
        let request = request.into_inner();
        let redirect_url = if request.state == "no-redirect" {
            String::new()
        } else {
            REDIRECT_URL.to_string()
        };
        *self.last.lock().unwrap() = Some(request);
        Ok(tonic::Response::new(FormCallbackResponse {
            redirect_url,
            ..Default::default()
        }))
    }
}

struct FixtureDispatcher {
    channel: Channel,
}

#[tonic::async_trait]
impl RestDispatcher for FixtureDispatcher {
    async fn channel(&self, _service_id: &str) -> Result<Channel, tonic::Status> {
        Ok(self.channel.clone())
    }

    async fn auth_header(&self, _service_id: &str) -> Option<String> {
        None
    }
}

async fn start_backend() -> (FixtureDispatcher, RecordingSignin) {
    let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("bind loopback port");
    let addr: SocketAddr = listener.local_addr().expect("resolve loopback port");
    drop(listener);

    let backend = RecordingSignin::default();
    let serving = backend.clone();
    tokio::spawn(async move {
        Server::builder()
            .add_service(SigninServiceServer::new(serving))
            .serve(addr)
            .await
            .expect("serve generated SigninService");
    });

    let endpoint = Endpoint::from_shared(format!("http://{}", addr)).expect("build endpoint");
    let mut channel = None;
    for _ in 0..100 {
        match endpoint.connect().await {
            Ok(ch) => {
                channel = Some(ch);
                break;
            }
            Err(_) => tokio::time::sleep(Duration::from_millis(50)).await,
        }
    }
    let channel = channel.expect("backend never accepted a connection");
    (FixtureDispatcher { channel }, backend)
}

fn route(grpc_path: &str) -> &'static RouteInfo {
    ROUTES
        .iter()
        .find(|r| r.grpc_path == grpc_path)
        .unwrap_or_else(|| panic!("no generated route for {}", grpc_path))
}

/// One request through the generated seams, in a REST handler's order.
async fn post(
    dispatcher: &FixtureDispatcher,
    content_type: Option<&str>,
    body: &[u8],
) -> axum::response::Response {
    use axum::response::IntoResponse;

    let route = route(FORM_PATH);
    let content_type = content_type.map(|v| HeaderValue::from_str(v).unwrap());
    let json = match rest_dispatch::form_body_to_json(route, content_type.as_ref(), body.to_vec()).await {
        Ok(json) => json,
        Err(err) => return err.into_response(),
    };
    let response = match rest_dispatch::dispatch(dispatcher, route.grpc_path, &json, "req").await {
        Ok(response) => response,
        Err(err) => return err.into_response(),
    };
    rest_dispatch::redirect_response(route, &response).unwrap_or_else(|| response.into_response())
}

async fn body_text(response: axum::response::Response) -> String {
    let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
        .await
        .expect("read response body");
    String::from_utf8(bytes.to_vec()).expect("response body is utf-8")
}

fn assert_see_other(response: &axum::response::Response) {
    assert_eq!(response.status(), StatusCode::SEE_OTHER, "browser form target did not answer 303");
    assert_eq!(
        response.headers().get(header::LOCATION).map(|v| v.to_str().unwrap()),
        Some(REDIRECT_URL),
        "303 Location is not the response's redirect_url"
    );
    assert_eq!(
        response.headers().get(header::CACHE_CONTROL).map(|v| v.to_str().unwrap()),
        Some("no-store"),
        "303 must not be cached"
    );
}

#[test]
fn rust_rest_browser_routes_carry_the_annotation() {
    let form = route(FORM_PATH);
    assert!(form.form_body, "rest_browser form_body not emitted into ROUTES");
    // The response JSON spells redirect_url as its proto3 JSON name.
    assert_eq!(form.redirect_field, Some("redirectUrl"));

    let json_only = route(JSON_ONLY_PATH);
    assert!(!json_only.form_body, "a route without rest_browser must stay JSON-only");
    assert_eq!(json_only.redirect_field, None);
}

#[tokio::test]
async fn rust_rest_browser_form_body_decodes_into_the_request() {
    let (dispatcher, backend) = start_backend().await;

    // Percent-escapes and '+' decode; `user` is left out and stays unset.
    let body = b"code=c%2Fd%2Be%3D&state=s+t%26u&id_token=eyJ.a%2Eb";
    let response = post(&dispatcher, Some("application/x-www-form-urlencoded; charset=UTF-8"), body).await;
    assert_see_other(&response);

    let got = backend.last.lock().unwrap().clone().expect("form post never reached the backend");
    assert_eq!(got.code, "c/d+e=");
    assert_eq!(got.state, "s t&u");
    assert_eq!(got.id_token, "eyJ.a.b");
    assert_eq!(got.user, "", "a field the form leaves out must stay unset");
}

#[tokio::test]
async fn rust_rest_browser_form_body_refuses_a_repeated_field() {
    let (dispatcher, backend) = start_backend().await;

    let response = post(&dispatcher, Some(FORM), b"code=one&state=s&code=two").await;
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    let text = body_text(response).await;
    assert!(text.contains("code sent more than once"), "wrong refusal: {}", text);
    assert!(backend.last.lock().unwrap().is_none(), "a refused form post reached the backend");
}

#[tokio::test]
async fn rust_rest_browser_form_body_ignores_a_field_the_request_lacks() {
    // Exactly what the JSON path does with an unknown key: it is dropped, and
    // the declared fields still arrive (an IdP's `error=` post must not 400).
    let (dispatcher, backend) = start_backend().await;

    let response = post(&dispatcher, Some(FORM), b"code=c&error=user_cancelled_authorize").await;
    assert_see_other(&response);
    let got = backend.last.lock().unwrap().clone().expect("form post never reached the backend");
    assert_eq!(got.code, "c");
}

#[tokio::test]
async fn rust_rest_browser_json_body_is_unchanged() {
    // A JSON request to a form route takes the JSON path exactly as before.
    let json = br#"{"code":"c","state":"s"}"#.to_vec();
    let passed = rest_dispatch::form_body_to_json(
        route(FORM_PATH),
        Some(&HeaderValue::from_static("application/json")),
        json.clone(),
    )
    .await
    .expect("a JSON body passes through");
    assert_eq!(passed, json);

    let none = rest_dispatch::form_body_to_json(route(FORM_PATH), None, json.clone())
        .await
        .expect("a body with no content type passes through");
    assert_eq!(none, json);

    // A form content type on a route without form_body is not decoded either.
    let form = b"owner=a%40b.example".to_vec();
    let untouched = rest_dispatch::form_body_to_json(
        route(JSON_ONLY_PATH),
        Some(&HeaderValue::from_static(FORM)),
        form.clone(),
    )
    .await
    .expect("a non-form route passes its body through");
    assert_eq!(untouched, form);

    let (dispatcher, backend) = start_backend().await;
    let response = post(&dispatcher, Some("application/json"), &json).await;
    assert_see_other(&response);
    let got = backend.last.lock().unwrap().clone().expect("JSON post never reached the backend");
    assert_eq!((got.code.as_str(), got.state.as_str()), ("c", "s"));
}

#[tokio::test]
async fn rust_rest_browser_redirect_without_a_url_is_a_backend_fault() {
    let (dispatcher, _backend) = start_backend().await;

    let response = post(&dispatcher, Some(FORM), b"state=no-redirect").await;
    assert_eq!(response.status(), StatusCode::BAD_GATEWAY);
    assert!(response.headers().get(header::LOCATION).is_none());
}

#[test]
fn rust_rest_browser_json_route_answers_json() {
    assert!(
        rest_dispatch::redirect_response(route(JSON_ONLY_PATH), r#"{"owner":"a@b.example"}"#).is_none(),
        "a route without redirect_field must answer its JSON"
    );
}
