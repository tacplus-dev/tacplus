// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

//! memberport-peer (tacplus#212, #321, #322): the test fixture that stands in
//! for the black-box protocol behind a member port, the Rust twin of
//! MemberportPeer.java. Test code only, never shipped in a service. It talks
//! raw TCP on plain threads. Every byte it sends is a seeded pseudo-random
//! stream, and the other side checks what it receives byte for byte as it
//! arrives, so a dropped, duplicated, reordered or corrupted byte fails at the
//! first wrong offset.
//!
//! A [`Server`] accepts and either echoes (Echo) or exchanges (Push): it sends
//! its own seeded stream while it reads and checks the client's, in both
//! directions at once. [`exchange`] is the client side.

#![allow(dead_code)]

use std::collections::HashMap;
use std::io::{ErrorKind, Read, Write};
use std::net::{Shutdown, SocketAddr, TcpListener, TcpStream};
use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

/// The seeded stream: byte i of seed s is the same however it is chunked. It
/// is java.util.SplittableRandom(seed).nextInt(256), byte by byte, so a Rust
/// peer and MemberportPeer.java check each other's streams (the interop leg).
pub struct Stream(u64);

impl Stream {
    pub fn new(seed: u64) -> Self {
        Self(seed)
    }

    /// SplittableRandom.nextInt(): mix32 of the next seed; nextInt(256) keeps
    /// its low 8 bits.
    fn next(&mut self) -> u8 {
        self.0 = self.0.wrapping_add(0x9E37_79B9_7F4A_7C15);
        let mut z = self.0;
        z = (z ^ (z >> 33)).wrapping_mul(0x62A9_D9ED_7997_05F5);
        ((z ^ (z >> 28)).wrapping_mul(0xCB24_D0A5_C88C_35B3) >> 32) as u8
    }

    pub fn fill(&mut self, buf: &mut [u8]) {
        for b in buf.iter_mut() {
            *b = self.next();
        }
    }

    /// Checks received bytes against the stream; the first wrong index, if any.
    pub fn check(&mut self, buf: &[u8]) -> Option<usize> {
        buf.iter().position(|b| *b != self.next())
    }
}

/// What one side of a connection saw.
#[derive(Debug, Clone, Default)]
pub struct PeerResult {
    pub received: u64,
    pub first_mismatch: Option<u64>,
    pub error: Option<String>,
    pub error_kind: Option<ErrorKind>,
}

impl PeerResult {
    pub fn ok(&self, expected: u64) -> bool {
        self.error.is_none() && self.first_mismatch.is_none() && self.received == expected
    }

    /// The connection was reset: on a read, or on a write when the writer met
    /// the RST first (a later read then sees EOF).
    pub fn reset(&self) -> bool {
        matches!(self.error_kind, Some(ErrorKind::ConnectionReset) | Some(ErrorKind::BrokenPipe))
    }

    fn failed(received: u64, e: &std::io::Error) -> Self {
        Self { received, first_mismatch: None, error: Some(e.to_string()), error_kind: Some(e.kind()) }
    }
}

/// Sends `n` bytes of seed's stream in seeded chunk sizes, then half-closes.
pub fn send(s: &TcpStream, seed: u64, n: u64, max_chunk: usize, pause: Duration) -> std::io::Result<()> {
    let mut stream = Stream::new(seed);
    let mut chunks = Stream::new(seed ^ 0x5DEE_CE66);
    let mut buf = vec![0u8; max_chunk];
    let mut out = s;
    let mut sent = 0u64;
    while sent < n {
        let pick = (chunks.next() as usize) << 8 | chunks.next() as usize;
        let len = ((n - sent) as usize).min(1 + pick % max_chunk);
        stream.fill(&mut buf[..len]);
        out.write_all(&buf[..len])?;
        sent += len as u64;
        if !pause.is_zero() {
            thread::sleep(pause);
        }
    }
    s.shutdown(Shutdown::Write)
}

/// Reads to EOF, checking every byte against seed's stream; after each read
/// it pauses while `pause()` says so (re-read, so setting it to 0 wakes it).
pub fn receive(s: &TcpStream, seed: u64, pause: &dyn Fn() -> u64) -> PeerResult {
    let mut stream = Stream::new(seed);
    let mut buf = vec![0u8; 16 * 1024];
    let mut received = 0u64;
    let mut input = s;
    loop {
        let n = match input.read(&mut buf) {
            Ok(0) => return PeerResult { received, ..Default::default() },
            Ok(n) => n,
            Err(e) => return PeerResult::failed(received, &e),
        };
        if let Some(bad) = stream.check(&buf[..n]) {
            return PeerResult {
                received: received + n as u64,
                first_mismatch: Some(received + bad as u64),
                ..Default::default()
            };
        }
        received += n as u64;
        let until = Instant::now() + Duration::from_millis(pause());
        while pause() > 0 && Instant::now() < until {
            thread::sleep(Duration::from_millis(10));
        }
    }
}

/// The client side: connects to `addr`, sends `send_bytes` of `send_seed`
/// while it reads and checks `expect_seed`'s stream.
pub fn exchange(addr: SocketAddr, send_seed: u64, send_bytes: u64, expect_seed: u64) -> PeerResult {
    exchange_with(addr, send_seed, send_bytes, expect_seed, 16 * 1024, Duration::ZERO, 0)
}

pub fn exchange_with(
    addr: SocketAddr,
    send_seed: u64,
    send_bytes: u64,
    expect_seed: u64,
    max_chunk: usize,
    send_pause: Duration,
    read_pause_ms: u64,
) -> PeerResult {
    let s = match TcpStream::connect_timeout(&addr, Duration::from_secs(10)) {
        Ok(s) => s,
        Err(e) => return PeerResult::failed(0, &e),
    };
    let _ = s.set_read_timeout(Some(Duration::from_secs(60)));
    let writer_socket = s.try_clone().expect("clone socket");
    let writer = thread::spawn(move || send(&writer_socket, send_seed, send_bytes, max_chunk, send_pause));
    let r = receive(&s, expect_seed, &|| read_pause_ms);
    let sent = writer.join().expect("writer thread");
    match (r.error.is_none(), sent) {
        (true, Err(e)) => PeerResult {
            error: Some(format!("send failed: {}", e)),
            error_kind: Some(e.kind()),
            ..r
        },
        _ => r,
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Mode {
    Echo,
    Push,
}

/// The server side: echoes, or exchanges seeded streams with every client.
pub struct Server {
    pub addr: SocketAddr,
    /// Connections accepted: zero means nothing ever reached the protocol.
    pub accepted: AtomicUsize,
    /// Bytes received over all connections.
    pub bytes_received: AtomicU64,
    pub results: Mutex<HashMap<usize, PeerResult>>,
    pub read_pause_ms: AtomicU64,
}

impl Server {
    pub fn echo() -> Arc<Self> {
        Self::start(Mode::Echo, 0, 0, 0, 16 * 1024, Duration::ZERO)
    }

    pub fn push(push_seed: u64, push_bytes: u64, expect_seed: u64) -> Arc<Self> {
        Self::start(Mode::Push, push_seed, push_bytes, expect_seed, 16 * 1024, Duration::ZERO)
    }

    /// Push in chunks of at most `push_chunk` bytes, pausing after each.
    pub fn start(
        mode: Mode,
        push_seed: u64,
        push_bytes: u64,
        expect_seed: u64,
        push_chunk: usize,
        push_pause: Duration,
    ) -> Arc<Self> {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind peer server");
        let server = Arc::new(Self {
            addr: listener.local_addr().expect("peer server address"),
            accepted: AtomicUsize::new(0),
            bytes_received: AtomicU64::new(0),
            results: Mutex::new(HashMap::new()),
            read_pause_ms: AtomicU64::new(0),
        });
        let me = server.clone();
        thread::spawn(move || {
            for conn in listener.incoming() {
                let Ok(s) = conn else { return };
                let id = me.accepted.fetch_add(1, Ordering::SeqCst) + 1;
                let me = me.clone();
                thread::spawn(move || {
                    let r = me.serve(mode, &s, push_seed, push_bytes, expect_seed, push_chunk, push_pause);
                    me.results.lock().unwrap().insert(id, r);
                });
            }
        });
        server
    }

    #[allow(clippy::too_many_arguments)]
    fn serve(
        &self,
        mode: Mode,
        s: &TcpStream,
        push_seed: u64,
        push_bytes: u64,
        expect_seed: u64,
        push_chunk: usize,
        push_pause: Duration,
    ) -> PeerResult {
        if mode == Mode::Echo {
            let mut buf = vec![0u8; 16 * 1024];
            let (mut input, mut out) = (s, s);
            loop {
                let n = match input.read(&mut buf) {
                    Ok(0) => break,
                    Ok(n) => n,
                    Err(e) => return PeerResult::failed(0, &e),
                };
                self.bytes_received.fetch_add(n as u64, Ordering::SeqCst);
                if let Err(e) = out.write_all(&buf[..n]) {
                    return PeerResult::failed(0, &e);
                }
                let pause = self.read_pause_ms.load(Ordering::SeqCst);
                if pause > 0 {
                    thread::sleep(Duration::from_millis(pause));
                }
            }
            let _ = s.shutdown(Shutdown::Write);
            return PeerResult::default();
        }
        let writer_socket = s.try_clone().expect("clone socket");
        let writer = thread::spawn(move || {
            // The reader's result records the connection's fate.
            let _ = send(&writer_socket, push_seed, push_bytes, push_chunk, push_pause);
        });
        let r = receive(s, expect_seed, &|| self.read_pause_ms.load(Ordering::SeqCst));
        self.bytes_received.fetch_add(r.received, Ordering::SeqCst);
        let _ = writer.join();
        r
    }

    pub fn result(&self, id: usize) -> Option<PeerResult> {
        self.results.lock().unwrap().get(&id).cloned()
    }

    pub fn result_count(&self) -> usize {
        self.results.lock().unwrap().len()
    }
}
