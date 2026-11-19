// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Runtime coverage for the generated Rust standalone C2D host. NOT generated:
// run.sh copies it into the generated crate's tests/ directory, so it is an
// external crate that reaches the daemon only the way a cluster-less daemon's
// main does: it implements the generated s2sm Handler and calls the public
// ServiceManager::serve_c2d_standalone, with no config, no identity and no
// state store anywhere in the process. Calls go over the real Unix socket,
// through the generated c2dm client.
//
// What each assertion would catch:
//   - a valid call succeeds with NO token and NO authorizer wired: a c2d
//     wrapper that demanded token auth could not serve it;
//   - an invalid request sent on the raw stub (the c2dm client validates
//     before sending, so it would never reach the server) is InvalidArgument
//     and never reaches the handler: the standalone host serves the generated
//     wrapper, not the bare handler;
//   - a handler response that breaks its constraints is Internal: the full
//     handler pipeline runs, not just the request half;
//   - the socket file is a socket, mode 0600, and replaced a stale file left
//     at the path: the access boundary and the stale-socket removal;
//   - resolving the shutdown future ends serve_c2d_standalone with Ok, the
//     socket file is gone, and Handler::shutdown ran exactly once, after the
//     drain.

use std::os::unix::fs::{FileTypeExt, PermissionsExt};
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::Arc;
use std::time::Duration;

use c2dserver_model::s2sm::handler::Handler;
use c2dserver_model::s2sm::internal::grpc::servers::svc::c2dserver::local_daemon::LocalDaemonHandler;
use c2dserver_model::s2sm::ServiceManager;
use c2dserver_model::types::svc::c2dserver::local_daemon_client::LocalDaemonClient;
use c2dserver_model::types::svc::c2dserver::{LocalEvent, LocalRequest, LocalResponse};
use futures::StreamExt;

const SOCKET_PATH: &str = "/tmp/tacplus-c2d-server-check/daemon.sock";

#[derive(Default)]
struct RecordingDaemon {
    unary_hits: AtomicU32,
    stream_hits: AtomicU32,
    shutdown_calls: AtomicU32,
}

struct Daemon(Arc<RecordingDaemon>);

#[tonic::async_trait]
impl LocalDaemonHandler for Daemon {
    async fn get_local(
        &self,
        caller: Option<&servicemanager::auth::Caller>,
        req: LocalRequest,
    ) -> Result<LocalResponse, tonic::Status> {
        self.0.unary_hits.fetch_add(1, Ordering::SeqCst);
        if caller.is_some() {
            return Err(tonic::Status::internal("c2d handler got a caller identity; no token was verified"));
        }
        if req.item_id == "bad-response" {
            return Ok(LocalResponse::default()); // result_id is assert_required
        }
        Ok(LocalResponse { result_id: format!("result-{}", req.item_id), ..Default::default() })
    }

    async fn generate_local_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: LocalRequest,
    ) -> Result<
        tonic::Response<std::pin::Pin<Box<dyn tokio_stream::Stream<Item = Result<LocalEvent, tonic::Status>> + Send>>>,
        tonic::Status,
    > {
        self.0.stream_hits.fetch_add(1, Ordering::SeqCst);
        let item_id = req.item_id.clone();
        let events: Vec<Result<LocalEvent, tonic::Status>> = (0..req.count)
            .map(|seq| Ok(LocalEvent { item_id: item_id.clone(), seq, ..Default::default() }))
            .collect();
        Ok(tonic::Response::new(Box::pin(tokio_stream::iter(events))))
    }
}

// The generated Handler: the c2d getter and shutdown, nothing cluster-bound.
#[tonic::async_trait]
impl Handler for Daemon {
    fn local_daemon_grpc_impl(&self) -> Arc<dyn LocalDaemonHandler> {
        Arc::new(Daemon(self.0.clone()))
    }

    async fn shutdown(&self) {
        self.0.shutdown_calls.fetch_add(1, Ordering::SeqCst);
    }
}

#[tokio::test]
async fn generated_c2d_standalone_host_over_socket() {
    // A stale file at the socket path, as a crashed daemon leaves behind.
    std::fs::create_dir_all("/tmp/tacplus-c2d-server-check").unwrap();
    let _ = std::fs::remove_file(SOCKET_PATH);
    std::fs::write(SOCKET_PATH, "stale").unwrap();

    // A daemon passes the generated constant for the declared socket_path.
    assert_eq!(c2dserver_model::s2sm::C2D_SOCKET_PATH, SOCKET_PATH);
    let state = Arc::new(RecordingDaemon::default());
    let (stop_tx, stop_rx) = tokio::sync::oneshot::channel::<()>();
    let host = tokio::spawn({
        let handler: Arc<dyn Handler> = Arc::new(Daemon(state.clone()));
        async move {
            ServiceManager::serve_c2d_standalone(c2dserver_model::s2sm::C2D_SOCKET_PATH, handler, async {
                let _ = stop_rx.await;
            })
            .await
            .map_err(|e| e.to_string())
        }
    });

    // Ready once the stale file has been replaced by a socket AND the socket is
    // locked down: the chassis binds first and chmods after, so the mode is only
    // final a moment after the file appears. A host that never restricts it
    // fails here at the deadline.
    let deadline = tokio::time::Instant::now() + Duration::from_secs(10);
    loop {
        if let Ok(meta) = std::fs::metadata(SOCKET_PATH) {
            if meta.file_type().is_socket() && meta.permissions().mode() & 0o777 == 0o600 {
                break;
            }
        }
        if host.is_finished() {
            panic!("standalone c2d host exited before listening: {:?}", host.await);
        }
        assert!(
            tokio::time::Instant::now() < deadline,
            "no owner-only (0600) c2d socket at {SOCKET_PATH} (metadata: {:?}); the socket ACL is the c2d auth boundary",
            std::fs::metadata(SOCKET_PATH).map(|m| m.permissions().mode())
        );
        tokio::time::sleep(Duration::from_millis(20)).await;
    }

    let client = c2dserver_model::c2dm::ServiceManager::connect(SOCKET_PATH).await.expect("dial c2d socket");
    let daemon_client = client.grpc().c2dserver().local_daemon_client();

    // Valid unary call: no token anywhere, and it must still be served.
    let resp = daemon_client
        .get_local(LocalRequest { item_id: "abc".into(), ..Default::default() })
        .await
        .expect("valid c2d call without a token failed (token auth not skipped?)");
    assert_eq!(resp.get_ref().result_id, "result-abc");
    assert_eq!(state.unary_hits.load(Ordering::SeqCst), 1);

    // Invalid request on the raw stub: the wrapper must reject it before the
    // handler (item_id has assert_min_size 3).
    let mut raw = LocalDaemonClient::new(client.conn().clone());
    let err = raw
        .get_local(LocalRequest { item_id: "ab".into(), ..Default::default() })
        .await
        .expect_err("invalid request was served");
    assert_eq!(err.code(), tonic::Code::InvalidArgument, "invalid request: {err:?}");
    assert_eq!(state.unary_hits.load(Ordering::SeqCst), 1, "invalid request reached the handler");

    // Response validation still runs on the c2d surface.
    let err = raw
        .get_local(LocalRequest { item_id: "bad-response".into(), ..Default::default() })
        .await
        .expect_err("constraint-breaking response was served");
    assert_eq!(err.code(), tonic::Code::Internal, "constraint-breaking response: {err:?}");

    // Server streaming over the socket.
    let mut stream = daemon_client
        .generate_local_events(LocalRequest { item_id: "abc".into(), count: 3, ..Default::default() })
        .await
        .expect("open server stream")
        .into_inner();
    let mut seqs = Vec::new();
    while let Some(ev) = stream.next().await {
        seqs.push(ev.expect("stream recv").seq);
    }
    assert_eq!(seqs, vec![0, 1, 2]);

    // Invalid streaming request: rejected before the handler.
    let err = match raw.generate_local_events(LocalRequest { item_id: "ab".into(), count: 1, ..Default::default() }).await {
        Err(e) => e,
        Ok(resp) => resp.into_inner().next().await.expect("stream item").expect_err("invalid stream was served"),
    };
    assert_eq!(err.code(), tonic::Code::InvalidArgument, "invalid streaming request: {err:?}");
    assert_eq!(state.stream_hits.load(Ordering::SeqCst), 1);
    assert_eq!(state.shutdown_calls.load(Ordering::SeqCst), 0, "Handler::shutdown ran while serving");

    // Shutdown: the entry returns Ok, removes the socket file and calls
    // Handler::shutdown once. Drop the client first: tonic's graceful shutdown
    // waits for open connections to close.
    drop(raw);
    drop(client);
    stop_tx.send(()).unwrap();
    let result = tokio::time::timeout(Duration::from_secs(35), host)
        .await
        .expect("standalone c2d host did not stop after its shutdown future resolved")
        .expect("host task panicked");
    assert!(result.is_ok(), "standalone c2d host returned {result:?} on shutdown");
    assert!(std::fs::metadata(SOCKET_PATH).is_err(), "socket file still present after shutdown");
    assert_eq!(state.shutdown_calls.load(Ordering::SeqCst), 1);
}
