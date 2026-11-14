// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The Rust leg of the flock gate (tacplus#206), built against the generated
// flockcheck types crate. It checks the generated merge of the people table the
// way the Go leg (flock_test.go) does, line for line:
//
//   - properties: over random copies of one entry (random writes on random
//     copies, with marks and acks), flock_merge is commutative, associative and
//     idempotent over whole entries plus marks, and flock_merge_entries picks
//     the same survivor and loser mark whatever the order;
//   - simulation: copies that write, gossip whole snapshots through a network
//     that delivers in random order, partitions, holds far-future writes, merges
//     partition duplicates, acks and purges marks, then heal and converge. It
//     checks convergence (equal digests everywhere), no resurrection (a copy
//     never again holds live an entry it saw marked) and no redirect cycles
//     (every MERGED mark points to a strictly older entry);
//   - the cross-language vector: the digests of a fixed scenario, printed one
//     `flock vector: <name> <hex>` line each, which the gate compares with
//     cross-test/fixtures/flock_vectors.txt.
//
// The random sequences come from a small deterministic generator of this leg's
// own: they need not match Go's, only the vector must. A failure panics, so the
// process exits non-zero.

use std::cmp::Ordering;
use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::atomic::{AtomicI64, Ordering as AtomicOrdering};
use std::sync::Arc;

use flockcheck_model_types::people::flockcheck::*;
use flockcheck_model_types::sys::flock::{DeleteReason, Hlc};

const PROPERTY_CASES: usize = 300;
const SIMULATIONS: u64 = 40;
const SIM_STEPS: usize = 400;
const SIM_COPIES: usize = 4;
const RACE_WINDOW_MS: i64 = 5_000;

const IDPS: [&str; 3] = ["google", "microsoft", "apple"];
const TENANTS: [&str; 3] = ["alpha", "beta", "gamma"];
const NAMES: [&str; 4] = ["ann", "bob", "cy", "di"];

// ---------- a deterministic generator (SplitMix64) ----------

struct Rng(u64);

impl Rng {
    fn new(seed: u64, stream: u64) -> Rng {
        let mut r = Rng(seed.wrapping_mul(0x9E37_79B9_7F4A_7C15) ^ stream.wrapping_mul(0xD1B5_4A32_D192_ED03));
        r.next();
        r
    }

    fn next(&mut self) -> u64 {
        self.0 = self.0.wrapping_add(0x9E37_79B9_7F4A_7C15);
        let mut z = self.0;
        z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
        z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
        z ^ (z >> 31)
    }

    fn intn(&mut self, n: usize) -> usize {
        (self.next() % n as u64) as usize
    }

    fn int64n(&mut self, n: usize) -> i64 {
        self.intn(n) as i64
    }

    fn pick(&mut self, from: &[&str]) -> String {
        from[self.intn(from.len())].to_string()
    }
}

fn clock_at(node: &str, wall: &Arc<AtomicI64>) -> FlockClock {
    let w = wall.clone();
    FlockClock::new(node, Some(Box::new(move || w.load(AtomicOrdering::SeqCst))))
}

fn hlc(wall_ms: i64, node: &str) -> Hlc {
    Hlc { wall_ms, node: node.to_string(), ..Default::default() }
}

fn wall_of(h: &Option<Hlc>) -> i64 {
    h.as_ref().map(|h| h.wall_ms).unwrap_or(0)
}

fn reason_of(e: &Person) -> i32 {
    e.deleted.as_ref().map(|m| m.reason).unwrap_or(0)
}

// ---------- properties ----------

// random_write applies one random write to e on a copy whose clock is c.
fn random_write(r: &mut Rng, e: &mut Person, c: &mut FlockClock) {
    let at = c.now();
    match r.intn(16) {
        0 => flock_set_person_display(e, r.pick(&NAMES), &at),
        1 => flock_set_person_generation(e, r.intn(5) as u64, &at),
        2 => flock_set_person_first_seen_ms(e, 1 + r.int64n(1000), &at),
        3 => {
            let _ = flock_set_person_home_region(e, r.pick(&["eu", "us"]), &at);
        }
        4 => flock_set_person_profile_nickname(e, r.pick(&NAMES), &at),
        5 => flock_set_person_profile_last_sign_in_day(e, r.intn(30) as u64, &at),
        6 => flock_add_person_profile_emails(e, r.pick(&NAMES) + "@x", &at),
        7 => flock_remove_person_profile_emails(e, r.pick(&NAMES) + "@x", &at),
        8 => flock_add_person_links(e, &random_link(r), &at),
        9 => flock_remove_person_links(e, &random_link(r), &at),
        10 => {
            let m = Membership { tenant: r.pick(&TENANTS), role: r.pick(&NAMES), ..Default::default() };
            flock_add_person_memberships(e, &m, &at)
        }
        11 => {
            let m = Membership { tenant: r.pick(&TENANTS), ..Default::default() };
            flock_remove_person_memberships(e, &m, &at)
        }
        12 => {
            let k = r.pick(&NAMES);
            let d = Device {
                name: r.pick(&NAMES),
                first_seen_ms: 1 + r.int64n(100),
                last_seen_ms: 1 + r.int64n(100),
                ..Default::default()
            };
            flock_put_person_devices(e, k, &d, &at)
        }
        13 => flock_remove_person_devices(e, r.pick(&NAMES), &at),
        14 => {
            if r.intn(2) == 0 {
                flock_add_person_tags(e, r.pick(&NAMES), &at)
            } else {
                flock_remove_person_tags(e, r.pick(&NAMES), &at)
            }
        }
        _ => match r.intn(4) {
            0 => {
                let _ = flock_mark_person(e, DeleteReason::User, &at);
            }
            1 => {
                let _ = flock_mark_person(e, DeleteReason::Expired, &at);
            }
            2 => flock_ack_person(e, &format!("c{}", r.intn(3)), &at),
            _ => {
                let v = vec![r.pick(&NAMES), r.pick(&NAMES)];
                flock_set_person_aliases(e, v, &at)
            }
        },
    }
}

fn random_link(r: &mut Rng) -> Link {
    Link {
        idp: r.pick(&IDPS),
        subject: r.pick(&NAMES),
        added_ms: 1 + r.int64n(100),
        assurance: r.intn(3) as u32,
        ..Default::default()
    }
}

// random_copies returns three copies of one entry that diverged from a common
// ancestor by random writes on three clocks, each also having merged in some of
// the others' states, so they share history.
fn random_copies(r: &mut Rng, wall: &Arc<AtomicI64>) -> [Person; 3] {
    let mut clocks = [clock_at("c0", wall), clock_at("c1", wall), clock_at("c2", wall)];
    let base = flock_new_person("k1", &clocks[0].now());
    let mut out = [base.clone(), base.clone(), base];
    for _ in 0..30 {
        wall.fetch_add(r.int64n(3), AtomicOrdering::SeqCst);
        let i = r.intn(3);
        if r.intn(6) == 0 {
            let j = r.intn(3);
            out[i] = must_merge(&out[i], &out[j]);
            continue;
        }
        random_write(r, &mut out[i], &mut clocks[i]);
    }
    out
}

fn must_merge(a: &Person, b: &Person) -> Person {
    flock_merge_person(a, b).unwrap_or_else(|e| panic!("flock_merge_person: {:?}", e))
}

fn same_person(what: &str, a: &Person, b: &Person) {
    if flock_digest_person(a) != flock_digest_person(b) || a != b {
        panic!("{}:\n  {:?}\n  {:?}", what, a, b);
    }
}

fn properties() {
    let wall = Arc::new(AtomicI64::new(1_000));
    let mut r = Rng::new(1, 206);
    for _ in 0..PROPERTY_CASES {
        let [a, b, d] = random_copies(&mut r, &wall);
        same_person("commutative", &must_merge(&a, &b), &must_merge(&b, &a));
        same_person("associative", &must_merge(&must_merge(&a, &b), &d), &must_merge(&a, &must_merge(&b, &d)));
        let ab = must_merge(&a, &b);
        same_person("idempotent", &must_merge(&ab, &ab), &ab);
        same_person("absorbing", &must_merge(&ab, &a), &ab);

        // Two different entries: the same survivor and loser, either order.
        let mut other = d.clone();
        other.key = "k0".to_string();
        other.created = Some(hlc(wall_of(&d.created) + r.int64n(3) - 1, "c9"));
        let now = wall.load(AtomicOrdering::SeqCst);
        let (at1, at2) = (hlc(now, "c0"), hlc(now + 1, "c1"));
        let (s1, l1) = flock_merge_entries_person(&ab, &other, &at1).expect("merge_entries");
        let (s2, l2) = flock_merge_entries_person(&other, &ab, &at2).expect("merge_entries");
        if s1.key != s2.key || l1.key != l2.key {
            panic!("merge_entries picked {} then {}", s1.key, s2.key);
        }
        // The two merges' marks differ only in time; merging the results converges.
        same_person("merge_entries survivor", &s1, &must_merge(&s1, &s2));
        same_person("merge_entries survivor (other order)", &must_merge(&s1, &s2), &must_merge(&s2, &s1));
        same_person("merge_entries loser", &must_merge(&l1, &l2), &must_merge(&l2, &l1));
        let l = must_merge(&l1, &l2);
        if reason_of(&l) != DeleteReason::Merged as i32 && reason_of(&l) != DeleteReason::User as i32 {
            panic!("loser not marked: {:?}", l.deleted);
        }
        if flock_hlc_compare(s1.created.as_ref(), l1.created.as_ref()) == Ordering::Greater {
            panic!("survivor {:?} is younger than loser {:?}", s1.created, l1.created);
        }
    }
    check_scenarios();
}

// check_scenarios pins the cases the panel named.
fn check_scenarios() {
    let now: i64 = 5_000;
    let wall = Arc::new(AtomicI64::new(now));
    let (mut ca, mut cb) = (clock_at("a", &wall), clock_at("b", &wall));
    let base = flock_new_person("p", &ca.now());
    let link = Link { idp: "google".to_string(), subject: "s".to_string(), ..Default::default() };

    // flock creates keys: 128 random bits, hex, never repeated.
    let (k1, k2) = (flock_new_key().expect("key"), flock_new_key().expect("key"));
    assert!(k1.len() == 32 && k1 != k2, "flock_new_key: {k1} {k2}");

    // A re-add after a remove it saw survives remove-wins.
    let mut x = base.clone();
    flock_add_person_links(&mut x, &link, &ca.now());
    flock_remove_person_links(&mut x, &link, &ca.now());
    flock_add_person_links(&mut x, &link, &ca.now());
    if x.links.len() != 1 {
        panic!("re-add lost: {:?}", x.links);
    }
    // A concurrent add and remove: remove wins on links, add wins on tags.
    let (mut y, mut z) = (x.clone(), x.clone());
    flock_remove_person_links(&mut y, &link, &ca.now());
    flock_add_person_links(&mut z, &link, &cb.now());
    flock_add_person_tags(&mut z, "t".to_string(), &cb.now());
    flock_add_person_tags(&mut y, "t".to_string(), &ca.now());
    flock_remove_person_tags(&mut y, "t".to_string(), &ca.now());
    let m = must_merge(&y, &z);
    if !m.links.is_empty() {
        panic!("remove-wins: concurrent add won: {:?}", m.links);
    }
    if m.tags.len() != 1 {
        panic!("add-wins: concurrent remove won: {:?}", m.tags);
    }
    // A removed device stays removed when an older copy comes back.
    let mut d = base.clone();
    flock_put_person_devices(&mut d, "phone".to_string(), &Device { name: "phone".to_string(), ..Default::default() }, &ca.now());
    let old = d.clone();
    flock_remove_person_devices(&mut d, "phone".to_string(), &ca.now());
    let got = must_merge(&old, &d).devices;
    if !got.is_empty() {
        panic!("revoked device came back: {:?}", got);
    }
    // A mark wins over a later write, and a USER mark over a MERGED one.
    let mut w = base.clone();
    let _ = flock_mark_person(&mut w, DeleteReason::Expired, &ca.now());
    let mut v = base.clone();
    flock_set_person_display(&mut v, "later".to_string(), &cb.now());
    if reason_of(&must_merge(&w, &v)) != DeleteReason::Expired as i32 {
        panic!("a write undid a mark");
    }
    if flock_live_person(&w).is_ok() {
        panic!("a marked entry is live");
    }
    // A conflicting IMMUTABLE write is recorded, not resolved silently.
    let (mut i1, mut i2) = (base.clone(), base.clone());
    let _ = flock_set_person_home_region(&mut i1, "eu".to_string(), &ca.now());
    let _ = flock_set_person_home_region(&mut i2, "us".to_string(), &cb.now());
    let got = must_merge(&i1, &i2).flock_meta.unwrap_or_default().conflicts;
    if got != vec!["home_region".to_string()] {
        panic!("immutable conflict not recorded: {:?}", got);
    }
    if flock_set_person_home_region(&mut i1, "us".to_string(), &ca.now()).is_ok() {
        panic!("an immutable field was overwritten");
    }
    // A remote time far ahead never moves the clock.
    let mut c = clock_at("h", &wall);
    if c.observe(&hlc(now + FLOCK_MAX_SKEW_MS + 1, "far")) {
        panic!("a far-future time was accepted");
    }
    let got = c.now().wall_ms;
    if got != now {
        panic!("clock moved to {}", got);
    }
    // The unique check refuses a local conflict; only the partition race merges.
    let mut p1 = flock_new_person("p1", &ca.now());
    let p1_created = p1.created.clone().unwrap_or_default();
    flock_add_person_links(&mut p1, &link, &p1_created);
    let mut p2 = flock_new_person("p2", &cb.now());
    let p2_created = p2.created.clone().unwrap_or_default();
    flock_add_person_links(&mut p2, &link, &p2_created);
    if flock_check_unique_person(&p2, |_| Some("p1".to_string())).is_ok() {
        panic!("a local unique conflict was accepted");
    }
    let k = flock_unique_keys_person(&p1)[0].clone();
    if flock_overlap_person(&p1, &p2, &k, RACE_WINDOW_MS) != FlockOverlap::AutoMerge {
        panic!("the partition race was not merged");
    }
    let mut p3 = flock_new_person("p3", &cb.now());
    flock_add_person_links(&mut p3, &link, &cb.now());
    if flock_overlap_person(&p1, &p3, &k, RACE_WINDOW_MS) != FlockOverlap::Quarantine {
        panic!("a link added later was merged, not quarantined");
    }
    // Session: the newer whole entry wins.
    let s = flock_new_session("s", &ca.now());
    let (mut s1, mut s2) = (s.clone(), s.clone());
    s1.device_id = "old".to_string();
    flock_write_session(&mut s1, &ca.now());
    s2.device_id = "new".to_string();
    cb.observe(&s1.written.clone().unwrap_or_default());
    flock_write_session(&mut s2, &cb.now());
    let got = flock_merge_session(&s1, &s2).expect("merge session");
    if got.device_id != "new" {
        panic!("whole-entry LAST_WRITE kept {:?}", got.device_id);
    }
}

// ---------- simulation ----------

struct Message {
    from: usize,
    to: usize,
    sent: Hlc,
    people: Vec<Person>,
}

struct Replica {
    id: String,
    clock: FlockClock,
    skew: Arc<AtomicI64>,
    // people, by key: a BTreeMap iterates in key order, as sortedKeys does.
    people: BTreeMap<String, Person>,
    seen_marked: HashSet<String>,
    last_contact: i64,
    offline: bool,
    // source says where each entry last came from, for a failure message.
    source: HashMap<String, String>,
    // max_local is the latest local time this copy has had.
    max_local: i64,
}

struct Sim {
    seed: u64,
    r: Rng,
    wall: Arc<AtomicI64>,
    copies: Vec<Replica>,
    net: Vec<Message>,
    cut: HashSet<(usize, usize)>,
    members: Vec<String>,
    next_key: usize,
    // long_absence: a copy has already been away past the rebuild limit.
    long_absence: bool,
}

// SimStats counts what the simulations exercised; each must happen.
type SimStats = BTreeMap<&'static str, usize>;

fn bump(stats: &mut SimStats, what: &'static str) {
    *stats.entry(what).or_insert(0) += 1;
}

impl Sim {
    fn wall(&self) -> i64 {
        self.wall.load(AtomicOrdering::SeqCst)
    }

    fn add_wall(&self, d: i64) {
        self.wall.fetch_add(d, AtomicOrdering::SeqCst);
    }

    fn now(&self, c: usize) -> i64 {
        self.wall() + self.copies[c].skew.load(AtomicOrdering::SeqCst)
    }

    fn local_write(&mut self, ci: usize, stats: &mut SimStats) {
        if self.copies[ci].offline {
            return;
        }
        // Create a person, born with a link from a small pool: duplicates across
        // a partition are the race the unique check merges.
        if self.copies[ci].people.is_empty() || self.r.intn(5) == 0 {
            self.next_key += 1;
            let key = format!("{}-{:04}", self.copies[ci].id, self.next_key);
            let created = self.copies[ci].clock.now();
            let mut e = flock_new_person(&key, &created);
            let link = Link {
                idp: IDPS[self.r.intn(2)].to_string(),
                subject: NAMES[self.r.intn(2)].to_string(),
                added_ms: self.now(ci),
                ..Default::default()
            };
            flock_add_person_links(&mut e, &link, &created);
            if flock_check_unique_person(&e, owner(&self.copies[ci])).is_err() {
                bump(stats, "refused");
                return; // refused: a local conflict
            }
            self.copies[ci].people.insert(key, e);
            return;
        }
        let keys: Vec<String> = self.copies[ci].people.keys().cloned().collect();
        let k = keys[self.r.intn(keys.len())].clone();
        let mut e = self.copies[ci].people[&k].clone();
        if flock_live_person(&e).is_err() {
            return; // refused: marked
        }
        random_write(&mut self.r, &mut e, &mut self.copies[ci].clock);
        if flock_check_unique_person(&e, owner(&self.copies[ci])).is_err() {
            bump(stats, "refused");
            return;
        }
        if flock_live_person(&e).is_err() {
            bump(stats, "mark");
        }
        self.copies[ci].people.insert(k, e);
    }

    fn send(&mut self, i: usize, j: usize) {
        if self.copies[i].offline || i == j {
            return;
        }
        let people: Vec<Person> = self.copies[i].people.values().cloned().collect();
        let sent = self.copies[i].clock.now();
        self.net.push(Message { from: i, to: j, sent, people });
    }

    fn deliver(&mut self, stats: &mut SimStats) {
        if self.net.is_empty() {
            return;
        }
        let n = self.r.intn(self.net.len());
        let m = self.net.remove(n);
        let to = m.to;
        if self.copies[to].offline || self.cut.contains(&(m.from, m.to)) {
            return; // lost
        }
        if flock_message_too_old(&m.sent, self.now(to)) {
            bump(stats, "too-old");
            return;
        }
        // A message from a clock far ahead is held until local time catches up.
        if !self.copies[to].clock.observe(&m.sent) {
            bump(stats, "held");
            self.net.push(m);
            return;
        }
        self.copies[to].last_contact = self.wall();
        for p in &m.people {
            if !self.copies[to].clock.observe(&p.written.clone().unwrap_or_default()) {
                self.net.push(m);
                return;
            }
        }
        let now_to = self.now(to);
        let wall = self.wall();
        let from_id = self.copies[m.from].id.clone();
        let c = &mut self.copies[to];
        for p in &m.people {
            match c.people.get(&p.key) {
                None => {
                    if flock_purge_ready_person(p, &self.members, now_to) {
                        continue; // already purged here, or about to be
                    }
                    c.people.insert(p.key.clone(), p.clone());
                    c.source.insert(p.key.clone(), format!("inserted from {} sent {:?} at wall {}", from_id, m.sent, wall));
                }
                Some(cur) => {
                    let merged = must_merge(cur, p);
                    c.people.insert(p.key.clone(), merged);
                }
            }
        }
        resolve(c, stats);
    }

    fn partition(&mut self, stats: &mut SimStats) {
        match self.r.intn(4) {
            0 => self.cut.clear(),
            1 => {
                let (i, j) = (self.r.intn(SIM_COPIES), self.r.intn(SIM_COPIES));
                self.cut.insert((i, j));
                self.cut.insert((j, i));
            }
            2 => {
                // Split the copies in two halves.
                self.cut.clear();
                for i in 0..SIM_COPIES {
                    for j in 0..SIM_COPIES {
                        if (i < SIM_COPIES / 2) != (j < SIM_COPIES / 2) {
                            self.cut.insert((i, j));
                        }
                    }
                }
            }
            _ => {
                let ci = self.r.intn(SIM_COPIES);
                self.copies[ci].offline = !self.copies[ci].offline;
                if self.copies[ci].offline && !self.long_absence && self.r.intn(3) == 0 {
                    // This copy stays away longer than the rebuild limit.
                    self.long_absence = true;
                    self.add_wall(FLOCK_REBUILD_AFTER_MS + 24 * 60 * 60 * 1000);
                }
                let wall = self.wall();
                let c = &mut self.copies[ci];
                if !c.offline && flock_must_rebuild(c.last_contact, wall) {
                    bump(stats, "rebuild");
                    // Wiped: a new copy, which serves nothing until it has caught up.
                    c.people.clear();
                    c.seen_marked.clear();
                }
            }
        }
    }

    // housekeep: a copy acks the marks it holds and purges the ready ones.
    fn housekeep(&mut self, ci: usize, stats: &mut SimStats) {
        if self.copies[ci].offline {
            return;
        }
        let keys: Vec<String> = self.copies[ci].people.keys().cloned().collect();
        for k in keys {
            if flock_live_person(&self.copies[ci].people[&k]).is_ok() {
                continue;
            }
            let at = self.copies[ci].clock.now();
            let id = self.copies[ci].id.clone();
            if let Some(e) = self.copies[ci].people.get_mut(&k) {
                flock_ack_person(e, &id, &at);
            }
            let now = self.now(ci);
            if flock_purge_ready_person(&self.copies[ci].people[&k], &self.members, now) {
                bump(stats, "purge");
                self.copies[ci].people.remove(&k);
            }
        }
    }

    fn check_invariants(&mut self) {
        for ci in 0..self.copies.len() {
            let now = self.now(ci);
            let c = &mut self.copies[ci];
            let keys: Vec<String> = c.people.keys().cloned().collect();
            for k in &keys {
                let e = &c.people[k];
                let marked = flock_live_person(e).is_err();
                if !marked && c.seen_marked.contains(k) {
                    panic!("{}: {} came back to life ({:?})", c.id, k, c.source.get(k));
                }
                if marked {
                    c.seen_marked.insert(k.clone());
                }
                if let Some(m) = e.deleted.as_ref().filter(|m| m.reason == DeleteReason::Merged as i32) {
                    if flock_hlc_compare(m.merged_into_created.as_ref(), e.created.as_ref()) != Ordering::Less {
                        panic!("{}: {} redirects to {}, which is not older", c.id, k, m.merged_into);
                    }
                }
            }
            // Redirect chains end.
            for k in &keys {
                let mut seen: HashSet<String> = HashSet::new();
                let mut cur = Some(k.clone());
                while let Some(at) = cur {
                    if !seen.insert(at.clone()) {
                        panic!("{}: redirect cycle through {}", c.id, at);
                    }
                    cur = c.people.get(&at).and_then(flock_redirect_person);
                }
            }
            // A remote time never moved this clock past local time plus the bound.
            if now > c.max_local {
                c.max_local = now;
            }
            let got = c.clock.now().wall_ms;
            if got > c.max_local + FLOCK_MAX_SKEW_MS {
                panic!("{}: clock at {}, beyond local time {} plus the skew bound", c.id, got, c.max_local);
            }
        }
    }

    fn check_converged(&self) {
        let reference = &self.copies[0];
        for c in &self.copies[1..] {
            let a: Vec<&String> = reference.people.keys().collect();
            let b: Vec<&String> = c.people.keys().collect();
            if a != b {
                panic!("seed {}: {} holds {:?}, {} holds {:?}", self.seed, reference.id, a, c.id, b);
            }
            for k in a {
                if flock_digest_person(&reference.people[k]) != flock_digest_person(&c.people[k]) {
                    panic!(
                        "seed {}: {} differs:\n  {}: {:?}\n  {}: {:?}",
                        self.seed, k, reference.id, reference.people[k], c.id, c.people[k]
                    );
                }
            }
        }
        if std::env::var_os("FLOCK_SIM_VERBOSE").is_some() {
            let live = reference.people.values().filter(|e| flock_live_person(e).is_ok()).count();
            println!("seed {}: {} live, {} marked", self.seed, live, reference.people.len() - live);
        }
    }
}

// owner is copy c's unique index.
fn owner(c: &Replica) -> impl Fn(&str) -> Option<String> {
    let mut idx: HashMap<String, String> = HashMap::new();
    for (k, e) in &c.people {
        for u in flock_unique_keys_person(e) {
            idx.entry(u).or_insert_with(|| k.clone());
        }
    }
    move |u: &str| idx.get(u).cloned()
}

// resolve runs the unique check over gossip on copy c: the partition race
// merges, any other overlap stays (quarantined); every MERGED entry is
// re-absorbed into its survivor.
fn resolve(c: &mut Replica, stats: &mut SimStats) {
    loop {
        let mut changed = false;
        let mut idx: HashMap<String, String> = HashMap::new();
        let keys: Vec<String> = c.people.keys().cloned().collect();
        'entries: for k in &keys {
            for u in flock_unique_keys_person(&c.people[k]) {
                let Some(o) = idx.get(&u).cloned() else {
                    idx.insert(u, k.clone());
                    continue;
                };
                if flock_overlap_person(&c.people[&o], &c.people[k], &u, RACE_WINDOW_MS) != FlockOverlap::AutoMerge {
                    bump(stats, "quarantine");
                    continue; // quarantined
                }
                bump(stats, "auto-merge");
                let at = c.clock.now();
                let (sv, l) = flock_merge_entries_person(&c.people[&o], &c.people[k], &at).expect("merge_entries");
                c.people.insert(sv.key.clone(), sv);
                c.people.insert(l.key.clone(), l);
                changed = true;
                break 'entries;
            }
        }
        if !changed {
            break;
        }
    }
    let keys: Vec<String> = c.people.keys().cloned().collect();
    for k in &keys {
        let Some(target) = flock_redirect_person(&c.people[k]) else {
            continue;
        };
        if c.people.contains_key(&target) {
            bump(stats, "redirect");
            let at = c.clock.now();
            let (nsv, _) = flock_merge_entries_person(&c.people[&target], &c.people[k], &at).expect("merge_entries");
            c.people.insert(target, nsv);
        }
    }
}

fn run_sim(seed: u64, stats: &mut SimStats) {
    let wall = Arc::new(AtomicI64::new(1_000_000));
    let mut s = Sim {
        seed,
        r: Rng::new(seed, 2026),
        wall: wall.clone(),
        copies: Vec::new(),
        net: Vec::new(),
        cut: HashSet::new(),
        members: Vec::new(),
        next_key: 0,
        long_absence: false,
    };
    for i in 0..SIM_COPIES {
        let id = format!("c{}", i);
        let skew = Arc::new(AtomicI64::new(0));
        let (w, sk) = (wall.clone(), skew.clone());
        let clock = FlockClock::new(
            &id,
            Some(Box::new(move || w.load(AtomicOrdering::SeqCst) + sk.load(AtomicOrdering::SeqCst))),
        );
        s.members.push(id.clone());
        s.copies.push(Replica {
            id,
            clock,
            skew,
            people: BTreeMap::new(),
            seen_marked: HashSet::new(),
            last_contact: 0,
            offline: false,
            source: HashMap::new(),
            max_local: 0,
        });
    }
    // One copy runs a clock far ahead for a while: its writes are held.
    s.copies[SIM_COPIES - 1].skew.store(FLOCK_MAX_SKEW_MS * 2, AtomicOrdering::SeqCst);
    for step in 0..SIM_STEPS {
        let d = 1 + s.r.int64n(400);
        s.add_wall(d);
        if step == SIM_STEPS / 3 {
            s.copies[SIM_COPIES - 1].skew.store(0, AtomicOrdering::SeqCst);
        }
        let k = s.r.intn(20);
        if k < 7 {
            let ci = s.r.intn(SIM_COPIES);
            s.local_write(ci, stats);
        } else if k < 11 {
            let (i, j) = (s.r.intn(SIM_COPIES), s.r.intn(SIM_COPIES));
            s.send(i, j);
        } else if k < 16 {
            s.deliver(stats);
        } else if k < 17 {
            s.partition(stats);
        } else if k < 18 && s.r.intn(8) == 0 {
            // A quiet spell: what is still in flight is now too old to apply.
            s.add_wall(FLOCK_MAX_MESSAGE_AGE_MS + 1);
        } else {
            let ci = s.r.intn(SIM_COPIES);
            s.housekeep(ci, stats);
        }
        s.check_invariants();
    }
    // Heal: every copy online, no partition, rounds of full gossip until quiet.
    s.cut.clear();
    let wall_now = s.wall();
    for c in s.copies.iter_mut() {
        c.skew.store(0, AtomicOrdering::SeqCst);
        if c.offline {
            c.offline = false;
            if flock_must_rebuild(c.last_contact, wall_now) {
                c.people.clear();
                c.seen_marked.clear();
            }
        }
    }
    for _ in 0..40 {
        s.add_wall(FLOCK_MAX_MESSAGE_AGE_MS / 4);
        for i in 0..SIM_COPIES {
            for j in 0..SIM_COPIES {
                if i != j {
                    s.send(i, j);
                }
            }
        }
        while !s.net.is_empty() {
            s.deliver(stats);
        }
        for ci in 0..SIM_COPIES {
            s.housekeep(ci, stats);
        }
        s.check_invariants();
    }
    s.check_converged();
}

fn simulation() -> SimStats {
    let mut stats = SimStats::new();
    for seed in 0..SIMULATIONS {
        run_sim(seed, &mut stats);
    }
    for what in ["auto-merge", "quarantine", "redirect", "mark", "purge", "held", "too-old", "refused", "rebuild"] {
        if stats.get(what).copied().unwrap_or(0) == 0 {
            panic!("the simulations never exercised {}: {:?}", what, stats);
        }
    }
    stats
}

// ---------- the cross-language vector ----------

// vector_scenario is the same fixed sequence of writes, merges and marks in
// every leg; its digests must equal cross-test/fixtures/flock_vectors.txt byte
// for byte, which pins the canonical encoding and every merge rule across
// languages.
fn vector_scenario() -> Vec<String> {
    let now: i64 = 1_700_000_000_000;
    let wall = Arc::new(AtomicI64::new(now));
    let mut a = clock_at("a", &wall);
    let mut b = clock_at("b", &wall);
    let mut p = flock_new_person("person-1", &a.now());
    let created = p.created.clone().unwrap_or_default();
    let link = |idp: &str, subject: &str, added_ms: i64, assurance: u32| Link {
        idp: idp.to_string(),
        subject: subject.to_string(),
        added_ms,
        assurance,
        ..Default::default()
    };
    flock_add_person_links(&mut p, &link("google", "s1", 10, 2), &created);
    flock_set_person_display(&mut p, "Ann".to_string(), &a.now());
    let mut q = p.clone();
    flock_set_person_display(&mut q, "Annie".to_string(), &b.now());
    flock_add_person_tags(&mut q, "x".to_string(), &b.now());
    let pixel = Device { name: "Pixel".to_string(), first_seen_ms: 5, last_seen_ms: 9, ..Default::default() };
    flock_put_person_devices(&mut q, "phone".to_string(), &pixel, &b.now());
    flock_remove_person_links(&mut p, &link("google", "s1", 0, 0), &a.now());
    flock_add_person_links(&mut q, &link("apple", "s2", 7, 1), &b.now());
    let admin = Membership { tenant: "alpha".to_string(), role: "admin".to_string(), ..Default::default() };
    flock_add_person_memberships(&mut q, &admin, &b.now());
    flock_set_person_generation(&mut p, 3, &a.now());
    flock_set_person_generation(&mut q, 5, &b.now());
    flock_set_person_first_seen_ms(&mut p, 40, &a.now());
    flock_set_person_first_seen_ms(&mut q, 30, &b.now());
    let _ = flock_set_person_home_region(&mut p, "eu".to_string(), &a.now());
    let _ = flock_set_person_home_region(&mut q, "us".to_string(), &b.now());
    flock_set_person_profile_nickname(&mut q, "nick".to_string(), &b.now());
    flock_set_person_profile_last_sign_in_day(&mut p, 12, &a.now());
    flock_add_person_profile_emails(&mut p, "a@x".to_string(), &a.now());
    flock_set_person_aliases(&mut p, vec!["x".to_string(), "y".to_string()], &a.now());
    let m = must_merge(&p, &q);
    let mut o = flock_new_person("person-0", &hlc(now - 1, "b"));
    flock_add_person_tags(&mut o, "y".to_string(), &b.now());
    let book = Device { name: "Book".to_string(), first_seen_ms: 3, last_seen_ms: 4, ..Default::default() };
    flock_put_person_devices(&mut o, "laptop".to_string(), &book, &b.now());
    let (s, mut l) = flock_merge_entries_person(&m, &o, &a.now()).expect("merge_entries");
    flock_ack_person(&mut l, "a", &a.now());
    let mut gone = s.clone();
    let _ = flock_mark_person(&mut gone, DeleteReason::User, &b.now());

    let sess = flock_new_session("session-1", &a.now());
    let (mut s1, mut s2) = (sess.clone(), sess.clone());
    s1.device_id = "phone".to_string();
    s1.expires_ms = 100;
    flock_write_session(&mut s1, &a.now());
    s2.device_id = "laptop".to_string();
    s2.expires_ms = 200;
    flock_write_session(&mut s2, &b.now());
    let sm = flock_merge_session(&s1, &s2).expect("merge session");
    let hex = |d: Vec<u8>| d.iter().map(|c| format!("{:02x}", c)).collect::<String>();
    vec![
        format!("merged {}", hex(flock_digest_person(&m))),
        format!("survivor {}", hex(flock_digest_person(&s))),
        format!("loser {}", hex(flock_digest_person(&l))),
        format!("marked {}", hex(flock_digest_person(&gone))),
        format!("session {}", hex(flock_digest_session(&sm))),
    ]
}

fn main() {
    properties();
    let stats = simulation();
    println!("flock rust: {} property cases, {} simulations", PROPERTY_CASES, SIMULATIONS);
    println!("flock rust simulation: {:?}", stats);
    for line in vector_scenario() {
        println!("flock vector: {}", line);
    }
}
