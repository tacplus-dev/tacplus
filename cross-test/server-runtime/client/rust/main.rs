// Copyright (c) 2026 Second Star Cavalier LLC

// server-runtime RUST client driver (LED-2 phase 3): the GENERATED RUST C2S
// CLIENT (c2sm/internal/grpc/clients/contract/contractclient/contract_api.rs),
// dialed at a generated server hosted by ANOTHER language's fixture (java).
// Nothing here speaks gRPC by hand: request validation, bearer-token
// attachment, the PAGE auto-pager and response decoding are generated code.
//
// Mirrors client/go/main.go leg for leg so the driving clients assert the same
// wire facts about the same hosted servers.
//
// Reads TACPLUS_SERVER_ADDR (host:port) and TACPLUS_SERVER_LANG (label used in
// failure messages only).

use std::sync::Arc;

use contractclient_model::c2sm::internal::grpc::clients::grpc_clients::TokenProvider;
use contractclient_model::c2sm::ServiceManager;
use contractclient_model::types::contract::contractclient::{
    ContractMode, ContractPageRequest, ContractRequest,
};
use futures::StreamExt;

const FIXTURE_TOKEN: &str = "fixture-token";

fn valid_request() -> ContractRequest {
    ContractRequest {
        tenant_namespace: "tenant.alpha".to_string(),
        item_id: "item-123".to_string(),
        mode: ContractMode::Read as i32,
        tags: vec!["one".to_string()],
        metadata: std::collections::HashMap::from([(
            "owner".to_string(),
            "server-runtime".to_string(),
        )]),
        ..Default::default()
    }
}

fn fail(message: String) -> ! {
    eprintln!("server-runtime rust client FAILED: {}", message);
    std::process::exit(1);
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let addr = std::env::var("TACPLUS_SERVER_ADDR")
        .unwrap_or_else(|_| fail("TACPLUS_SERVER_ADDR is required".to_string()));
    let lang = std::env::var("TACPLUS_SERVER_LANG")
        .unwrap_or_else(|_| fail("TACPLUS_SERVER_LANG is required".to_string()));

    let channel = tonic::transport::Endpoint::from_shared(format!("http://{}", addr))?
        .connect_timeout(std::time::Duration::from_secs(5))
        .connect()
        .await?;
    let token_provider: TokenProvider =
        Arc::new(|| Box::pin(async { Ok(FIXTURE_TOKEN.to_string()) }));
    let sm = ServiceManager::new(channel, Some(token_provider));
    let api = sm.grpc().contractclient().contract_api_client();

    // auth_disabled unary: the remote generated wrapper validates the request,
    // dispatches, validates the response and marshals it back.
    let resp = api
        .get_public_contract(valid_request())
        .await
        .unwrap_or_else(|e| fail(format!("{} GetPublicContract: {}", lang, e)))
        .into_inner();
    if resp.result_id != "result-123" {
        fail(format!(
            "{} GetPublicContract result_id = {}, want result-123",
            lang, resp.result_id
        ));
    }
    if !resp.accepted {
        fail(format!("{} GetPublicContract accepted = false, want true", lang));
    }
    println!("  {}: GetPublicContract unary round-trip ok", lang);

    // auth_any_user server-streaming (SNAPSHOT).
    let mut stream = api
        .list_events(valid_request())
        .await
        .unwrap_or_else(|e| fail(format!("{} ListEvents: {}", lang, e)))
        .into_inner();
    let mut ids: Vec<String> = Vec::new();
    while let Some(event) = stream
        .message()
        .await
        .unwrap_or_else(|e| fail(format!("{} ListEvents stream: {}", lang, e)))
    {
        if event.tenant_namespace != "tenant.alpha" {
            fail(format!(
                "{} ListEvents tenant_namespace = {}, want tenant.alpha",
                lang, event.tenant_namespace
            ));
        }
        ids.push(event.event_id);
    }
    if ids != vec!["event-1".to_string()] {
        fail(format!("{} ListEvents event ids = {:?}, want [event-1]", lang, ids));
    }
    println!("  {}: ListEvents SNAPSHOT stream round-trip ok", lang);

    // auth_any_user PAGE: the generated auto-pager follows the remote server's
    // next_page_token, so two pages prove the remote server encoded a resume
    // cursor and decoded it back on the follow-up call.
    let paged = api.list_events_paged(ContractPageRequest {
        tenant_namespace: "tenant.alpha".to_string(),
        page_size: 10,
        ..Default::default()
    });
    futures::pin_mut!(paged);
    let mut ids: Vec<String> = Vec::new();
    while let Some(item) = paged.next().await {
        let event = item.unwrap_or_else(|e| fail(format!("{} ListEventsPaged: {}", lang, e)));
        ids.push(event.event_id);
    }
    if ids != vec!["event-1".to_string(), "event-2".to_string()] {
        fail(format!(
            "{} ListEventsPaged event ids = {:?}, want [event-1, event-2]",
            lang, ids
        ));
    }
    println!("  {}: ListEventsPaged PAGE keyset continuation round-trip ok", lang);

    // auth_entitled_users unary with a token the remote server cannot accept.
    // The remote generated wrapper must reject before the handler — the fixture
    // handlers return result_id "HANDLER-REACHED" for this method, so a success
    // (or that marker) here means auth was bypassed.
    //
    // Both hosted fixtures run the REAL generated Authorizer over an explicitly
    // empty verifying-key set, so the opaque fixture bearer is rejected at token
    // verification in both languages: UNAUTHENTICATED, exactly, either side.
    match api.get_contract(valid_request()).await {
        Ok(resp) => fail(format!(
            "{} GetContract was accepted (result_id={}) — auth is not fail-closed",
            lang,
            resp.into_inner().result_id
        )),
        Err(status) => {
            let code = status.code();
            if code != tonic::Code::Unauthenticated {
                fail(format!(
                    "{} GetContract rejected with {:?}, want Unauthenticated: {}",
                    lang, code, status
                ));
            }
            println!("  {}: GetContract fail-closed rejection ok ({:?})", lang, code);
        }
    }

    println!("rust generated client OK against {} generated server ({})", lang, addr);
    Ok(())
}
