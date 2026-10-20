// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

// outbound-deadline RUST fixture (issue #38): a three-hop chain of REAL
// generated ContractApi servers and clients, each on its own loopback port, so
// every hop is real HTTP/2 gRPC:
//
//   root (generated client, 5 s budget)
//     -> mid  (generated server; its handler calls leaf with a generated client)
//       -> leaf (generated server; its handler sleeps 14 s)
//
// The fixture supplies only the generated handler trait impl. Dispatch,
// validation, the outbound timeout and the outbound retry are generated code.
//
// Two facts are checked, and both are about what the generated runtime does:
//
//   1. WIRE: the grpc-timeout that leaf receives must not exceed the 5 s budget
//      the root set. The generated client overwrites the inbound budget with
//      its own fixed timeout (30 s) on every hop.
//   2. LIFETIME: leaf's handler must stop when the root's deadline passes. It
//      must not run on to its 14 s sleep.
//
// Prints the event log, then exits non-zero if either fact fails.

use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use contractclient_model::c2sm::ServiceManager;
use servicemanager::deadline::InboundDeadline;
use contractclient_model::s2sm::internal::grpc::servers::contract::contractclient::contract_api::{
    ContractApiHandler, ValidationWrapper,
};
use contractclient_model::types::contract::contractclient::contract_api_client::ContractApiClient;
use contractclient_model::types::contract::contractclient::contract_api_server::ContractApiServer;
use contractclient_model::types::contract::contractclient::{
    ContractEvent, ContractMode, ContractPageRequest, ContractRequest, ContractResponse,
    ContractWatchChange, ContractWatchRequest,
};
use tokio::net::TcpListener;
use tokio_stream::wrappers::TcpListenerStream;
use tokio_stream::Stream;
use tonic::service::interceptor::InterceptedService;
use tonic::transport::Channel;

const ROOT_BUDGET: Duration = Duration::from_secs(5);
const LEAF_DELAY: Duration = Duration::from_secs(14);
// Slack for scheduling and loopback latency when comparing against the budget.
const SLACK: Duration = Duration::from_millis(1000);

type EventStream = std::pin::Pin<Box<dyn Stream<Item = Result<ContractEvent, tonic::Status>> + Send>>;

// Events is the one log every hop and the root write to, stamped from one
// clock, so the root can compare leaf's handler lifetime with its own deadline.
struct Events {
    t0: Instant,
    lines: Mutex<Vec<(u128, String)>>,
}

impl Events {
    fn record(&self, msg: String) {
        let at = self.t0.elapsed().as_millis();
        self.lines.lock().unwrap().push((at, msg));
    }

    fn find(&self, needle: &str) -> Option<u128> {
        self.lines.lock().unwrap().iter().find(|(_, m)| m.contains(needle)).map(|(at, _)| *at)
    }

    fn dump(&self) {
        for (at, msg) in self.lines.lock().unwrap().iter() {
            println!("  {:>6} ms  {}", at, msg);
        }
    }
}

#[derive(Clone, Copy, PartialEq)]
enum Role {
    Leaf,
    Mid,
}

// Lifetime records how long leaf's handler future lives. Dropping it before it
// finishes means the caller's cancellation reached the handler.
struct Lifetime {
    events: Arc<Events>,
    finished: bool,
}

impl Lifetime {
    fn start(events: Arc<Events>) -> Self {
        events.record("leaf handler started".to_string());
        Self { events, finished: false }
    }
}

impl Drop for Lifetime {
    fn drop(&mut self) {
        let what = if self.finished { "completed" } else { "dropped" };
        self.events.record(format!("leaf handler {what}"));
    }
}

struct Handler {
    role: Role,
    leaf: Option<ServiceManager>,
    events: Arc<Events>,
}

fn valid_response() -> ContractResponse {
    ContractResponse { result_id: "result-123".to_string(), accepted: true, ..Default::default() }
}

#[tonic::async_trait]
impl ContractApiHandler for Handler {
    async fn get_contract(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractRequest,
    ) -> Result<ContractResponse, tonic::Status> {
        Err(tonic::Status::unimplemented("not used by the outbound-deadline gate"))
    }

    async fn get_public_contract(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: ContractRequest,
    ) -> Result<ContractResponse, tonic::Status> {
        match self.role {
            Role::Leaf => {
                let mut life = Lifetime::start(self.events.clone());
                tokio::time::sleep(LEAF_DELAY).await;
                life.finished = true;
                Ok(valid_response())
            }
            Role::Mid => {
                let leaf = self.leaf.as_ref().expect("mid holds a client for leaf");
                let resp = leaf.grpc().contractclient().contract_api_client().get_public_contract(req).await?;
                Ok(resp.into_inner())
            }
        }
    }

    async fn list_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractRequest,
    ) -> Result<tonic::Response<EventStream>, tonic::Status> {
        Err(tonic::Status::unimplemented("not used by the outbound-deadline gate"))
    }

    async fn watch(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractWatchRequest,
    ) -> Result<collection::WatchSource<ContractEvent, ContractWatchChange>, tonic::Status> {
        Err(tonic::Status::unimplemented("not used by the outbound-deadline gate"))
    }

    async fn list_events_paged(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractPageRequest,
        _page: collection::PageParams,
    ) -> Result<collection::Page<ContractEvent>, tonic::Status> {
        Err(tonic::Status::unimplemented("not used by the outbound-deadline gate"))
    }

    async fn send_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: tonic::Request<EventStream>,
    ) -> Result<tonic::Response<ContractResponse>, tonic::Status> {
        Err(tonic::Status::unimplemented("not used by the outbound-deadline gate"))
    }

    async fn assist_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: tonic::Request<EventStream>,
    ) -> Result<tonic::Response<EventStream>, tonic::Status> {
        Err(tonic::Status::unimplemented("not used by the outbound-deadline gate"))
    }
}

// Serves the generated ContractApi wrapper on loopback and logs the grpc-timeout
// that arrives on each request, before the generated code runs.
async fn serve(name: &'static str, handler: Handler, events: Arc<Events>) -> SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind loopback");
    let addr = listener.local_addr().expect("local addr");
    let log = events.clone();
    let intercept = move |req: tonic::Request<()>| {
        let budget = req
            .metadata()
            .get("grpc-timeout")
            .map(|v| v.to_str().unwrap_or("unreadable").to_string())
            .unwrap_or_else(|| "none".to_string());
        log.record(format!("{name} received grpc-timeout={budget}"));
        Ok(req)
    };
    let service = InboundDeadline::new(ContractApiServer::new(ValidationWrapper::new(Arc::new(handler))));
    let service = InterceptedService::new(service, intercept);
    tokio::spawn(async move {
        tonic::transport::Server::builder()
            .add_service(service)
            .serve_with_incoming(TcpListenerStream::new(listener))
            .await
            .expect("serve");
    });
    addr
}

async fn connect(addr: SocketAddr) -> Channel {
    tonic::transport::Endpoint::from_shared(format!("http://{addr}"))
        .expect("endpoint")
        .connect()
        .await
        .expect("connect")
}

// grpc-timeout is <digits><unit>. tonic writes "m" (milliseconds) for sub-second
// budgets and "S" for whole seconds.
fn grpc_timeout_ms(value: &str) -> Option<u128> {
    let (digits, unit) = value.split_at(value.len().checked_sub(1)?);
    let n: u128 = digits.parse().ok()?;
    match unit {
        "H" => Some(n * 3_600_000),
        "M" => Some(n * 60_000),
        "S" => Some(n * 1_000),
        "m" => Some(n),
        "u" => Some(n / 1_000),
        "n" => Some(n / 1_000_000),
        _ => None,
    }
}

fn valid_request() -> ContractRequest {
    ContractRequest {
        tenant_namespace: "tenant.alpha".to_string(),
        item_id: "item-123".to_string(),
        mode: ContractMode::Read as i32,
        tags: vec!["one".to_string()],
        ..Default::default()
    }
}

fn fail(msg: String) -> ! {
    println!("outbound-deadline rust FAILED: {msg}");
    std::process::exit(1);
}

#[tokio::main]
async fn main() {
    let events = Arc::new(Events { t0: Instant::now(), lines: Mutex::new(Vec::new()) });

    let leaf_addr = serve("leaf", Handler { role: Role::Leaf, leaf: None, events: events.clone() }, events.clone()).await;
    let leaf_sm = ServiceManager::new(connect(leaf_addr).await, None);
    let mid_addr = serve(
        "mid",
        Handler { role: Role::Mid, leaf: Some(leaf_sm), events: events.clone() },
        events.clone(),
    )
    .await;

    // The root is an external caller: a plain tonic client that sets the 5 s
    // budget on the request. The generated code under test is mid's outbound
    // client and leaf's server wrapper.
    let mut root = ContractApiClient::new(connect(mid_addr).await);
    let mut req = tonic::Request::new(valid_request());
    req.set_timeout(ROOT_BUDGET);

    let started = Instant::now();
    let result = root.get_public_contract(req).await;
    let root_elapsed = started.elapsed();
    events.record(format!("root call returned after {} ms", root_elapsed.as_millis()));

    // Let leaf's handler reach its end (completed or dropped) or the 14 s sleep
    // ends, so its lifetime is observed rather than cut off by this process.
    let wait_until = Instant::now() + LEAF_DELAY + Duration::from_secs(2);
    while events.find("leaf handler completed").is_none()
        && events.find("leaf handler dropped").is_none()
        && Instant::now() < wait_until
    {
        tokio::time::sleep(Duration::from_millis(50)).await;
    }

    println!("event log (ms since start):");
    events.dump();

    // The root gives up at its budget. tonic reports the expiry as a status
    // (Cancelled or DeadlineExceeded, depending on the path), so any error at
    // the budget counts; success would mean the budget was not enforced.
    match &result {
        Ok(_) => fail("root call succeeded, want the 5 s budget to expire".to_string()),
        Err(status) => println!("root call failed with {:?} after {} ms", status.code(), root_elapsed.as_millis()),
    }
    if root_elapsed > ROOT_BUDGET + SLACK {
        fail(format!("root call took {} ms, budget is {} ms", root_elapsed.as_millis(), ROOT_BUDGET.as_millis()));
    }

    let mut failures: Vec<String> = Vec::new();

    // FACT 1 (wire): leaf must receive a budget no larger than the root's.
    let budget_line = events
        .lines
        .lock()
        .unwrap()
        .iter()
        .find(|(_, m)| m.starts_with("leaf received grpc-timeout="))
        .map(|(_, m)| m.clone())
        .unwrap_or_else(|| fail("leaf never received a request".to_string()));
    let leaf_budget = budget_line
        .rsplit('=')
        .next()
        .and_then(grpc_timeout_ms)
        .unwrap_or_else(|| fail(format!("cannot parse the leaf's grpc-timeout from {budget_line:?}")));
    if leaf_budget > (ROOT_BUDGET + SLACK).as_millis() {
        failures.push(format!(
            "FACT 1 wire: {budget_line:?}, but the root set {} ms; mid's generated client replaced the inbound budget with its own fixed timeout",
            ROOT_BUDGET.as_millis()
        ));
    } else {
        println!("FACT 1 wire: ok, {budget_line:?} within the {} ms budget", ROOT_BUDGET.as_millis());
    }

    // FACT 2 (lifetime): leaf's handler must stop when the root's deadline passes.
    let deadline_ms = (ROOT_BUDGET + SLACK).as_millis();
    match events.find("leaf handler completed").or_else(|| events.find("leaf handler dropped")) {
        None => failures.push("FACT 2 lifetime: leaf handler never ended within the observation window".to_string()),
        Some(leaf_end) if leaf_end > deadline_ms => failures.push(format!(
            "FACT 2 lifetime: leaf handler ended at {leaf_end} ms; the root's deadline was {} ms, so leaf kept working after its caller gave up",
            ROOT_BUDGET.as_millis()
        )),
        Some(leaf_end) => println!("FACT 2 lifetime: ok, leaf handler ended at {leaf_end} ms (root deadline {} ms)", ROOT_BUDGET.as_millis()),
    }

    if !failures.is_empty() {
        fail(failures.join("\n  "));
    }

    println!("outbound-deadline rust OK: both facts hold");
}
