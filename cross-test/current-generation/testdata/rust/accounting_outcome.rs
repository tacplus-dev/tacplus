// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for tacplus#273. NOT generated: run.sh copies this file into
// the accounting-sink fixture's generated crate as tests/accounting_outcome.rs
// and runs it with `cargo test --test accounting_outcome`. Twin of
// testdata/go/accounting_outcome_test.go, case for case.
//
// It drives the real generated AgentApi ValidationWrapper with the real
// generated Emitter and EventBusSink, and reads the AccountingRecords the sink
// hands the publisher:
//   - a query that returns N records produces exactly one record, PERMITTED,
//     records_returned=N, and no request or response value;
//   - a denied call produces exactly one record, DENIED_AUTHZ with its reason
//     code, and no error text; an unauthenticated one DENIED_AUTHN;
//   - one user call produces exactly one record end to end: the call that
//     stores it carries the accounting_internal marker and, made by a service,
//     records nothing, while a user's call with the marker is still recorded.

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use agentsensor_model::s2sm::internal::grpc::servers::services::agentsensor::agent_api::{
    AgentApiHandler, Authorizer, ValidationWrapper,
};
use agentsensor_model::types::services::agentsensor::agent_api_server::AgentApi;
use agentsensor_model::types::services::agentsensor::*;
use agentsensor_model::types::sys::accounting::accounting::{EventLevel, RpcOutcome, RpcReason};
use agentsensor_model::types::vservices::accountingrecord::accountingrecord::AccountingRecord;
use base64::Engine as _;
use prost::Message as _;
use servicemanager::auth::Caller;
use tonic::{Code, Status};

const SECRET_FILTER: &str = "SECRET-FILTER-VALUE";
const SECRET_ID: &str = "SECRET-EVENT-ID";
const SECRET_DENIAL: &str = "SECRET-DENIAL-DETAIL";
const QUERY_N: usize = 3;

struct QueryHandler;

#[tonic::async_trait]
impl AgentApiHandler for QueryHandler {
    async fn ping(&self, _caller: Option<&Caller>, _req: AgentEvent) -> Result<AgentEvent, Status> {
        Ok(AgentEvent { id: SECRET_ID.to_string(), ..Default::default() })
    }

    async fn get_agent_events(&self, _caller: Option<&Caller>, req: GetAgentEventsRequest) -> Result<GetAgentEventsResponse, Status> {
        let events: Vec<AgentEvent> = (0..QUERY_N)
            .map(|_| AgentEvent { id: SECRET_ID.to_string(), body: req.filter.clone().into_bytes(), ..Default::default() })
            .collect();
        am::set_records_returned(events.len() as i64);
        Ok(GetAgentEventsResponse { total: QUERY_N as i32, events, ..Default::default() })
    }
}

/// Refuses with `err`, or admits and returns the token's subject as the
/// verified identity. The wrapper re-reads the token's sub_type from the same
/// header, exactly as it does behind the real authorizer.
struct FakeAuthorizer(Option<Status>);

impl FakeAuthorizer {
    fn verdict(&self, authorization: Option<&str>) -> Result<HashMap<String, String>, Status> {
        if let Some(err) = &self.0 {
            return Err(err.clone());
        }
        let sub = authorization
            .and_then(|v| v.strip_prefix("Bearer "))
            .and_then(|t| token::Token::parse(t).ok())
            .and_then(|t| t.subject().map(str::to_string))
            .unwrap_or_default();
        Ok(HashMap::from([("sub".to_string(), sub)]))
    }
}

#[tonic::async_trait]
impl Authorizer for FakeAuthorizer {
    async fn authorize(&self, _full_method: &str, authorization: Option<&str>) -> Result<HashMap<String, String>, Status> {
        self.verdict(authorization)
    }
    async fn check_at_or_below(&self, _full_method: &str, authorization: Option<&str>, _target_namespace: &str) -> Result<HashMap<String, String>, Status> {
        self.verdict(authorization)
    }
    async fn authenticate(&self, _full_method: &str, authorization: Option<&str>) -> Result<HashMap<String, String>, Status> {
        self.verdict(authorization)
    }
}

/// An unsigned bearer token with the given sub_type and subject.
fn bearer(sub_type: &str, sub: &str) -> String {
    let enc = |v: serde_json::Value| base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(v.to_string());
    format!(
        "Bearer {}.{}.sig",
        enc(serde_json::json!({"typ": "JWT", "alg": "EdDSA"})),
        enc(serde_json::json!({"iss": "test", "sub": sub, "sub_type": sub_type})),
    )
}

/// A wrapper whose auth check returns `auth_err`, and the queue the accounting
/// sink feeds.
async fn accounted(auth_err: Option<Status>) -> (ValidationWrapper, tokio::sync::mpsc::Receiver<AccountingRecord>) {
    let (tx, rx) = tokio::sync::mpsc::channel::<AccountingRecord>(16);
    let wrapper = ValidationWrapper::new(Arc::new(QueryHandler));
    wrapper.set_authorize_func(Some(Arc::new(FakeAuthorizer(auth_err)))).await;
    let sink = am::EventBusSink::new(tx, "agentsensor-test".to_string());
    wrapper
        .set_emitter(Some(Arc::new(am::Emitter::new("agentsensor-test".to_string(), Box::new(sink)))))
        .await;
    (wrapper, rx)
}

fn query(bearer: &str, internal: bool) -> tonic::Request<GetAgentEventsRequest> {
    let mut r = tonic::Request::new(GetAgentEventsRequest { filter: SECRET_FILTER.to_string(), ..Default::default() });
    r.metadata_mut().insert("authorization", bearer.parse().unwrap());
    if internal {
        r.metadata_mut().insert(am::ACCOUNTING_INTERNAL_HEADER, "1".parse().unwrap());
    }
    r
}

/// Reads the one record a call produced and fails on a second.
async fn exactly_one(rx: &mut tokio::sync::mpsc::Receiver<AccountingRecord>) -> AccountingRecord {
    let rec = tokio::time::timeout(Duration::from_secs(2), rx.recv())
        .await
        .expect("the call produced no accounting record")
        .expect("accounting queue closed");
    if let Ok(Some(extra)) = tokio::time::timeout(Duration::from_millis(200), rx.recv()).await {
        panic!("one call produced a second accounting record: {extra:?}");
    }
    rec
}

/// Fails when a call produced any accounting record.
async fn no_record(rx: &mut tokio::sync::mpsc::Receiver<AccountingRecord>) {
    if let Ok(Some(rec)) = tokio::time::timeout(Duration::from_millis(300), rx.recv()).await {
        panic!("a call that stores accounting produced an accounting record: {rec:?}");
    }
}

/// Fails when any value the call carried reaches the record.
fn no_data(rec: &AccountingRecord) {
    let raw = rec.encode_to_vec();
    for v in [SECRET_FILTER, SECRET_ID, SECRET_DENIAL] {
        if raw.windows(v.len()).any(|w| w == v.as_bytes()) {
            panic!("accounting record carries data {v:?}: {rec:?}");
        }
    }
}

#[tokio::test]
async fn query_records_outcome_and_records_returned() {
    let (wrapper, mut rx) = accounted(None).await;
    let resp = wrapper
        .get_agent_events(query(&bearer("user", "alice"), false))
        .await
        .expect("query")
        .into_inner();
    assert_eq!(resp.events.len(), QUERY_N);
    let rec = exactly_one(&mut rx).await;
    assert_eq!(rec.outcome, i32::from(RpcOutcome::Permitted), "{rec:?}");
    assert_eq!(rec.code, 0, "{rec:?}");
    assert_eq!(rec.reason, i32::from(RpcReason::Undefined), "{rec:?}");
    assert_eq!(rec.records_returned, QUERY_N as i64, "{rec:?}");
    assert_eq!(rec.method, "GetAgentEvents", "{rec:?}");
    assert_eq!(rec.level, i32::from(EventLevel::Info), "{rec:?}");
    assert_eq!(rec.principal_sub, "alice", "{rec:?}");
    no_data(&rec);
}

#[tokio::test]
async fn denied_call_records_reason() {
    for (name, err, outcome, reason) in [
        ("authz", Status::permission_denied(SECRET_DENIAL), RpcOutcome::DeniedAuthz, RpcReason::NotEntitled),
        ("authn", Status::unauthenticated(SECRET_DENIAL), RpcOutcome::DeniedAuthn, RpcReason::TokenRejected),
    ] {
        let code = err.code();
        let (wrapper, mut rx) = accounted(Some(err)).await;
        let got = wrapper.get_agent_events(query(&bearer("user", "alice"), false)).await;
        assert_eq!(got.err().map(|s| s.code()), Some(code), "{name}: denied call returned the wrong status");
        let rec = exactly_one(&mut rx).await;
        assert_eq!(rec.outcome, i32::from(outcome), "{name}: {rec:?}");
        assert_eq!(rec.reason, i32::from(reason), "{name}: {rec:?}");
        assert_eq!(rec.code, code as i32, "{name}: {rec:?}");
        assert_eq!(rec.level, i32::from(EventLevel::Warn), "{name}: {rec:?}");
        assert_eq!(rec.records_returned, 0, "{name}: {rec:?}");
        assert!(rec.principal_sub.is_empty(), "{name}: a refused caller has no verified identity: {rec:?}");
        no_data(&rec);
    }
    assert_eq!(Code::PermissionDenied as i32, 7);
}

// The no-recursion requirement of tacplus#273: a user call is recorded once,
// and the call that carries its record onward (a service, with the marker the
// generated accounting publisher sends) is not recorded, so the record does not
// amplify. The marker on a user's own call changes nothing.
#[tokio::test]
async fn one_call_one_record_end_to_end() {
    let (user, mut user_rx) = accounted(None).await;
    user.get_agent_events(query(&bearer("user", "alice"), false)).await.expect("user call");
    let rec = exactly_one(&mut user_rx).await;
    assert_eq!(rec.principal_sub, "alice", "{rec:?}");
    assert_eq!(rec.outcome, i32::from(RpcOutcome::Permitted), "{rec:?}");

    let (store, mut store_rx) = accounted(None).await;
    store.get_agent_events(query(&bearer("service", "stratum"), true)).await.expect("store call");
    no_record(&mut store_rx).await;

    let (evader, mut evader_rx) = accounted(None).await;
    evader.get_agent_events(query(&bearer("user", "mallory"), true)).await.expect("evader call");
    let rec = exactly_one(&mut evader_rx).await;
    assert_eq!(rec.principal_sub, "mallory", "a user's call with the marker must still be recorded as theirs: {rec:?}");
}
