// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The trace-helpers gate's Rust leg (tacplus#327). The generated state client
// and record reader talk to the recorder (recorder/main.go), which logs
// "<method> <trace_id>" for every request it receives. Each traced form must
// put its trace on every request it sends, and today's form none. sys_check
// (written by run.sh) builds every generated sys message with trace_id set.

mod sys_check;

use std::sync::Arc;
use std::time::Duration;

use servicemanager::ChannelProvider;
use tonic::transport::Channel;
use tracehelpers_model::s2sm::internal::grpc::clients::vservices::helperquery::helper_record_map::HelperRecordMapGrpcClient;
use tracehelpers_model::s2sm::internal::state::services::helper_state::state::HelperStateClient;
use tracehelpers_model::s2sm::record_readers::HelperRowRecordReader;
use tracehelpers_model_types::services::tracehelpers::Player;
use tracehelpers_model_types::vservices::helperquery::HelperRowFilter;

// The state client's own watch loops: no request causes them, so they are not
// traced, and they run beside the cases.
const BACKGROUND: &[&str] = &["WatchSettings", "WatchPlayers", "WatchBench"];

struct Recorder {
    log: String,
    seen: usize,
}

impl Recorder {
    // take returns the requests the recorder logged since the last take, but
    // the background watches'.
    fn take(&mut self) -> Vec<(String, String)> {
        let text = std::fs::read_to_string(&self.log).expect("read the recorder log");
        let lines: Vec<&str> = text.lines().collect();
        let fresh = lines[self.seen..].to_vec();
        self.seen = lines.len();
        fresh
            .into_iter()
            .map(|l| {
                let (m, t) = l.split_once(' ').unwrap_or((l, ""));
                (m.to_string(), t.to_string())
            })
            .filter(|(m, _)| !BACKGROUND.contains(&m.as_str()))
            .collect()
    }

    fn check(&mut self, name: &str, form: &str, want_calls: &[&str], want_trace: &str, failures: &mut Vec<String>) {
        let got = self.take();
        let methods: Vec<&str> = got.iter().map(|(m, _)| m.as_str()).collect();
        if methods != want_calls {
            failures.push(format!("{name} ({form}): sent {methods:?}, want {want_calls:?}"));
            return;
        }
        for (m, t) in &got {
            if t != want_trace {
                failures.push(format!("{name} ({form}): {m} carried trace_id {t:?}, want {want_trace:?}"));
            }
        }
    }
}

macro_rules! case {
    ($rec:expr, $failures:expr, $count:expr, $name:expr, $calls:expr, |$tr:ident| $traced:expr, $untraced:expr) => {{
        let $tr = concat!("t-", $name);
        let _ = $traced.await;
        $rec.check($name, "traced", $calls, $tr, &mut $failures);
        let _ = $untraced.await;
        $rec.check($name, "untraced", $calls, "", &mut $failures);
        $count += 1;
    }};
}

#[tokio::main]
async fn main() {
    let addr = std::env::var("TRACE_HELPERS_ADDR").expect("TRACE_HELPERS_ADDR");
    let log = std::env::var("TRACE_HELPERS_LOG").expect("TRACE_HELPERS_LOG");
    let channel = Channel::from_shared(format!("http://{addr}"))
        .expect("recorder address")
        .connect()
        .await
        .expect("connect to the recorder");
    let mut rec = Recorder { log, seen: 0 };
    let mut failures = Vec::new();

    // State client.
    let state = HelperStateClient::new(channel.clone(), None).await.expect("state client");
    state.set_config_fetch_timeout_secs(1);
    // Let the watch loops send their first requests before the cases start.
    tokio::time::sleep(Duration::from_millis(500)).await;
    rec.take();
    let player = Player { player_id: "p-1".into(), trace_id: "stored-value-trace".into(), ..Default::default() };
    let mut state_cases = 0;
    case!(rec, failures, state_cases, "settings.fetch", &["GetSettings"],
        |tr| state.settings().fetch_traced(tr), state.settings().fetch());
    case!(rec, failures, state_cases, "fetch_all_players", &["GetAllPlayers"],
        |tr| state.fetch_all_players_traced(tr), state.fetch_all_players());
    case!(rec, failures, state_cases, "put_players", &["PutPlayers"],
        |tr| state.put_players_traced("p-1".into(), player.clone(), tr), state.put_players("p-1".into(), player.clone()));
    case!(rec, failures, state_cases, "delete_players", &["DeletePlayers"],
        |tr| state.delete_players_traced("p-1".into(), tr), state.delete_players("p-1".into()));
    println!("trace-helpers rust state: {state_cases} cases");

    // Record reader.
    let token: tracehelpers_model::s2sm::internal::grpc::clients::vservices::helperquery::helper_record_map::TokenProvider =
        Arc::new(|| Box::pin(async { Ok("token".to_string()) }));
    let client = HelperRecordMapGrpcClient::new(ChannelProvider::from_channel("helperstratum", channel.clone()), Some(token));
    let reader = HelperRowRecordReader::new(&client);
    let list = &["CreateHelperRowQuery", "ListHelperRow", "DeleteHelperRowQuery"];
    let mut reader_cases = 0;
    case!(rec, failures, reader_cases, "get", &["GetHelperRowRows"],
        |tr| reader.get_traced("r-1", tr), reader.get("r-1"));
    case!(rec, failures, reader_cases, "list", list,
        |tr| reader.list_traced(Some(HelperRowFilter::default()), 10, "", tr), reader.list(Some(HelperRowFilter::default()), 10, ""));
    println!("trace-helpers rust reader: {reader_cases} cases");

    let sys = sys_check::sys_contract();
    println!("trace-helpers rust sys: {sys} messages");

    if !failures.is_empty() {
        for f in &failures {
            eprintln!("FAIL {f}");
        }
        std::process::exit(1);
    }
}
