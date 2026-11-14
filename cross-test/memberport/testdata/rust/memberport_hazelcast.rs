// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

//! The Rust member ports of the Hazelcast leg (tacplus#322):
//! cross-test/memberport/run.sh --language rust --interop runs it beside
//! HazelcastMemberPortInteropTest.java, both pointed at
//! MEMBERPORT_HAZELCAST_DIR. The Java side runs HazelcastMemberPortTest's
//! cases with Hazelcast members (which must be Java) whose member traffic goes
//! through Rust member ports, all of them here: this side owns the fleet, each
//! member's token minter and certificate, and serves the Java side's numbered
//! command files (cmd.N, answered by ack.N; the verbs are documented on
//! HazelcastMemberPortInteropTest).

mod support;

use std::collections::HashMap;
use std::net::SocketAddr;
use std::path::PathBuf;
use std::sync::atomic::Ordering;
use std::sync::{Arc, RwLock};
use std::time::Duration;

use memberport::MemberPort;
use support::*;

struct Member {
    port: MemberPort,
    minter: Arc<Minter>,
}

fn parse<T: std::str::FromStr>(words: &[&str], i: usize) -> Result<T, String> {
    let w = words.get(i).ok_or_else(|| format!("{:?}: missing word {}", words, i))?;
    w.parse().map_err(|_| format!("{:?}: bad word {:?}", words, w))
}

async fn run(fleet: &Arc<Fleet>, members: &mut HashMap<String, Member>, words: &[&str]) -> Result<String, String> {
    let id = words.get(1).copied().unwrap_or_default();
    match words.first().copied().unwrap_or_default() {
        "member" => {
            let namespace: String = parse(words, 2)?;
            let minter = Minter::new(fleet, &namespace, parse(words, 3)?, parse(words, 4)?);
            let target: SocketAddr = parse(words, 5)?;
            let cert = Arc::new(RwLock::new(fleet.leaf(&namespace, SVC, true)));
            let port = member_with(fleet, minter.source(), &cert, fleet.jwk_lookup());
            let addr = port.listen(loopback0(), target).await.map_err(|e| e.to_string())?;
            members.insert(id.to_string(), Member { port, minter });
            Ok(addr.to_string())
        }
        "forward" => {
            let remote: SocketAddr = parse(words, 2)?;
            let m = members.get(id).ok_or_else(|| format!("no member {:?}", id))?;
            Ok(m.port.forward(remote).await.map_err(|e| e.to_string())?.to_string())
        }
        "mints" => {
            let m = members.get(id).ok_or_else(|| format!("no member {:?}", id))?;
            Ok(m.minter.mints.load(Ordering::SeqCst).to_string())
        }
        "never-renew" => {
            let m = members.get(id).ok_or_else(|| format!("no member {:?}", id))?;
            m.minter.never_renew.store(true, Ordering::SeqCst);
            Ok(String::new())
        }
        "mint" => Ok(fleet.mint(SVC, id, &aud(), parse(words, 2)?)),
        "reset" | "end" => {
            for (_, m) in members.drain() {
                m.port.close();
            }
            Ok(String::new())
        }
        _ => Err(format!("unknown command {:?}", words)),
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn hazelcast_members_through_rust_member_ports() {
    let Some(dir) = std::env::var_os("MEMBERPORT_HAZELCAST_DIR").map(PathBuf::from) else {
        eprintln!("MEMBERPORT_HAZELCAST_DIR runs the Hazelcast leg against Rust member ports (run.sh --interop)");
        return;
    };
    let fleet = Fleet::new();
    let mut members = HashMap::new();
    let mut failures = Vec::new();
    // The first command waits for the Java side's build.
    let mut wait = Duration::from_secs(600);
    for n in 0.. {
        let line = wait_interop(&dir, &format!("cmd.{}", n), wait).await;
        wait = Duration::from_secs(300);
        let words: Vec<&str> = line.split_whitespace().collect();
        let answer = match run(&fleet, &mut members, &words).await {
            Ok(v) if v.is_empty() => "ok".to_string(),
            Ok(v) => format!("ok {}", v),
            Err(e) => {
                eprintln!("{}: {}", line, e);
                failures.push(format!("{}: {}", line, e));
                format!("err {}", e)
            }
        };
        write_interop(&dir, &format!("ack.{}", n), &format!("{}\n", answer));
        if words.first() == Some(&"end") {
            break;
        }
    }
    assert!(failures.is_empty(), "commands failed: {:?}", failures);
}
