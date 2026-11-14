// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

// rustFlockRuntime is the schema-independent half of flock state (tacplus#206)
// for Rust: the hybrid clock, the deletion mark order, the op sets of SET_*
// fields and the canonical element encoding. flock_gen.go emits it once into
// the flock module of every package with a flock entry, beside that package's
// per-entry merge. It is the Go runtime (go/types/flock_runtime.go) line for
// line; change one and change them all (cross-test/flock checks they agree).
//
// Placeholders @MAX_SKEW_MS@, @REBUILD_AFTER_MS@ and @MAX_MESSAGE_AGE_MS@ are
// the parser's constants. Arithmetic on clock times wraps, as Go's does, so no
// input panics.
const rustFlockRuntime = `
    /// Flock timing constants (tacplus#206), the same in every language.
    /// FLOCK_MAX_SKEW_MS: a remote clock time further ahead of local wall time
    /// than this never moves the local clock; the write is held.
    pub const FLOCK_MAX_SKEW_MS: i64 = @MAX_SKEW_MS@;
    /// FLOCK_REBUILD_AFTER_MS: a copy absent longer than this is wiped and
    /// rebuilt from its peers, never merged.
    pub const FLOCK_REBUILD_AFTER_MS: i64 = @REBUILD_AFTER_MS@;
    /// FLOCK_MAX_MESSAGE_AGE_MS: replicated state older than this is dropped, and
    /// a mark is purged only this long after its last acknowledgement.
    pub const FLOCK_MAX_MESSAGE_AGE_MS: i64 = @MAX_MESSAGE_AGE_MS@;

    // google.rpc.Code values of the errors flock returns.
    const FLOCK_CODE_INVALID_ARGUMENT: i32 = 3;
    const FLOCK_CODE_NOT_FOUND: i32 = 5;
    const FLOCK_CODE_ALREADY_EXISTS: i32 = 6;
    const FLOCK_CODE_FAILED_PRECONDITION: i32 = 9;

    fn flock_error(code: i32, message: String) -> crate::sys::errors::Error {
        crate::sys::errors::Error { code, message, ..Default::default() }
    }

    /// FlockOverlap is the verdict of the unique check on two live entries that
    /// share an element of a (spec.unique) set.
    #[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
    pub enum FlockOverlap {
        /// Refuse to use the element and alert; a person resolves it with proof.
        Quarantine,
        /// The partition race (both entries were created with the element,
        /// within the window): merge them with flock_merge_entries_<t>.
        AutoMerge,
    }

    /// FlockClock is a copy's hybrid logical clock.
    pub struct FlockClock {
        node: String,
        now: Box<dyn Fn() -> i64 + Send + Sync>,
        last: fpb::Hlc,
    }

    fn flock_system_now_ms() -> i64 {
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_millis() as i64)
            .unwrap_or(0)
    }

    impl FlockClock {
        /// new returns the clock of copy node; now returns wall-clock Unix
        /// milliseconds (None: the system clock).
        pub fn new(node: &str, now: Option<Box<dyn Fn() -> i64 + Send + Sync>>) -> FlockClock {
            let now = now.unwrap_or_else(|| Box::new(flock_system_now_ms));
            FlockClock { node: node.to_string(), now, last: fpb::Hlc::default() }
        }

        /// now returns a time later than every time this clock has made or observed.
        pub fn now(&mut self) -> fpb::Hlc {
            let wall = (self.now)();
            if wall > self.last.wall_ms {
                self.last = fpb::Hlc { wall_ms: wall, counter: 0, node: self.node.clone(), ..Default::default() };
            } else {
                self.last = fpb::Hlc {
                    wall_ms: self.last.wall_ms,
                    counter: self.last.counter.wrapping_add(1),
                    node: self.node.clone(),
                    ..Default::default()
                };
            }
            self.last.clone()
        }

        /// observe moves the clock past a remote time, so this copy's next write
        /// is later than it. A remote time more than FLOCK_MAX_SKEW_MS ahead of
        /// local wall time is refused (false): the caller holds that write until
        /// local time catches up, and the clock does not move.
        pub fn observe(&mut self, remote: &fpb::Hlc) -> bool {
            if remote.wall_ms > (self.now)().wrapping_add(FLOCK_MAX_SKEW_MS) {
                return false;
            }
            if flock_hlc_compare(Some(remote), Some(&self.last)) == std::cmp::Ordering::Greater {
                self.last = fpb::Hlc { wall_ms: remote.wall_ms, counter: remote.counter, node: self.node.clone(), ..Default::default() };
            }
            true
        }
    }

    fn flock_hlc_parts(a: Option<&fpb::Hlc>) -> (i64, u32, &str) {
        match a {
            Some(h) => (h.wall_ms, h.counter, h.node.as_str()),
            None => (0, 0, ""),
        }
    }

    /// flock_hlc_compare orders two clock times by (wall_ms, counter, node); an
    /// unset time is the smallest.
    pub fn flock_hlc_compare(a: Option<&fpb::Hlc>, b: Option<&fpb::Hlc>) -> std::cmp::Ordering {
        flock_hlc_parts(a).cmp(&flock_hlc_parts(b))
    }

    fn flock_hlc_zero(a: Option<&fpb::Hlc>) -> bool {
        flock_hlc_parts(a) == (0, 0, "")
    }

    fn flock_hlc_max(a: Option<&fpb::Hlc>, b: Option<&fpb::Hlc>) -> Option<fpb::Hlc> {
        if flock_hlc_compare(a, b) != std::cmp::Ordering::Less {
            return flock_hlc_clone(a);
        }
        flock_hlc_clone(b)
    }

    // flock_hlc_clone stores a zero time as absent.
    fn flock_hlc_clone(a: Option<&fpb::Hlc>) -> Option<fpb::Hlc> {
        if flock_hlc_zero(a) {
            return None;
        }
        a.cloned()
    }

    /// flock_new_key returns a new random entry key (128 bits from the OS
    /// CSPRNG, hex). flock creates every key; a caller never chooses one.
    pub fn flock_new_key() -> Result<String, crate::sys::errors::Error> {
        let mut b = [0u8; 16];
        getrandom::getrandom(&mut b)
            .map_err(|_| flock_error(13, "flock: no randomness for a new key".to_string()))?;
        Ok(hex::encode(b))
    }

    /// flock_must_rebuild reports whether a copy last heard from at
    /// last_seen_ms is absent too long to merge: it is wiped and rebuilt from
    /// its peers.
    pub fn flock_must_rebuild(last_seen_ms: i64, now_ms: i64) -> bool {
        now_ms.wrapping_sub(last_seen_ms) > FLOCK_REBUILD_AFTER_MS
    }

    /// flock_message_too_old reports whether replicated state sent at sent is
    /// too old to apply: a mark may already be purged everywhere.
    pub fn flock_message_too_old(sent: &fpb::Hlc, now_ms: i64) -> bool {
        now_ms.wrapping_sub(sent.wall_ms) > FLOCK_MAX_MESSAGE_AGE_MS
    }

    fn flock_reason_rank(r: i32) -> i64 {
        if r == fpb::DeleteReason::User as i32 {
            return 3;
        }
        if r == fpb::DeleteReason::Merged as i32 {
            return 2;
        }
        if r == fpb::DeleteReason::Expired as i32 {
            return 1;
        }
        0
    }

    fn flock_marked(m: Option<&fpb::DeletionMark>) -> bool {
        flock_reason_rank(m.map(|m| m.reason).unwrap_or(0)) > 0
    }

    // flock_mark_compare orders two marks, acks aside: Greater when a wins. USER
    // over MERGED over EXPIRED; for MERGED the oldest target, then the smaller
    // target key; then the earliest mark.
    fn flock_mark_compare(a: &fpb::DeletionMark, b: &fpb::DeletionMark) -> std::cmp::Ordering {
        flock_reason_rank(a.reason)
            .cmp(&flock_reason_rank(b.reason))
            .then_with(|| flock_hlc_compare(b.merged_into_created.as_ref(), a.merged_into_created.as_ref()))
            .then_with(|| b.merged_into.cmp(&a.merged_into))
            .then_with(|| flock_hlc_compare(b.at.as_ref(), a.at.as_ref()))
    }

    // flock_merge_mark joins two marks: a mark wins over none, the larger mark
    // over the smaller, and the acks of equal marks are united (each copy's
    // earliest).
    fn flock_merge_mark(a: Option<&fpb::DeletionMark>, b: Option<&fpb::DeletionMark>) -> Option<fpb::DeletionMark> {
        match (a, b) {
            (Some(a), Some(b)) if flock_marked(Some(a)) && flock_marked(Some(b)) => match flock_mark_compare(a, b) {
                std::cmp::Ordering::Greater => Some(flock_mark_norm(a, &[])),
                std::cmp::Ordering::Less => Some(flock_mark_norm(b, &[])),
                std::cmp::Ordering::Equal => Some(flock_mark_norm(a, &b.acks)),
            },
            (Some(a), _) if flock_marked(Some(a)) => Some(flock_mark_norm(a, &[])),
            (_, Some(b)) if flock_marked(Some(b)) => Some(flock_mark_norm(b, &[])),
            _ => None,
        }
    }

    fn flock_mark_norm(m: &fpb::DeletionMark, more: &[fpb::MarkAck]) -> fpb::DeletionMark {
        let mut out = m.clone();
        let mut by_copy: std::collections::BTreeMap<String, Option<fpb::Hlc>> = std::collections::BTreeMap::new();
        for a in m.acks.iter().chain(more.iter()) {
            let earlier = match by_copy.get(&a.copy) {
                None => true,
                Some(at) => flock_hlc_compare(a.at.as_ref(), at.as_ref()) == std::cmp::Ordering::Less,
            };
            if earlier {
                by_copy.insert(a.copy.clone(), a.at.clone());
            }
        }
        out.acks = by_copy
            .into_iter()
            .map(|(copy, at)| fpb::MarkAck { copy, at: flock_hlc_clone(at.as_ref()), ..Default::default() })
            .collect();
        out
    }

    fn flock_mark_error(what: &str, key: &str, m: Option<&fpb::DeletionMark>) -> crate::sys::errors::Error {
        let reason = m.map(|m| m.reason).unwrap_or(0);
        if reason == fpb::DeleteReason::Merged as i32 {
            let into = m.map(|m| m.merged_into.as_str()).unwrap_or("");
            return flock_error(FLOCK_CODE_NOT_FOUND, format!("{} {} is merged into {}", what, key, into));
        }
        let name = fpb::DeleteReason::try_from(reason)
            .map(|r| r.as_str_name().to_string())
            .unwrap_or_else(|_| reason.to_string());
        flock_error(FLOCK_CODE_NOT_FOUND, format!("{} {} is deleted ({})", what, key, name))
    }

    fn flock_purge_ready(m: Option<&fpb::DeletionMark>, members: &[String], now_ms: i64) -> bool {
        let Some(m) = m.filter(|m| flock_marked(Some(m))) else {
            return false;
        };
        let mut acked: std::collections::HashMap<&str, i64> = std::collections::HashMap::new();
        for a in &m.acks {
            acked.insert(a.copy.as_str(), a.at.as_ref().map(|h| h.wall_ms).unwrap_or(0));
        }
        let mut last: i64 = 0;
        for c in members {
            let Some(at) = acked.get(c.as_str()) else {
                return false;
            };
            if *at > last {
                last = *at;
            }
        }
        now_ms.wrapping_sub(last) >= FLOCK_MAX_MESSAGE_AGE_MS
    }

    fn flock_ack(m: Option<&fpb::DeletionMark>, copy: &str, at: &fpb::Hlc) -> Option<fpb::DeletionMark> {
        match m {
            Some(m) if flock_marked(Some(m)) => Some(flock_mark_norm(
                m,
                &[fpb::MarkAck { copy: copy.to_string(), at: flock_hlc_clone(Some(at)), ..Default::default() }],
            )),
            _ => m.cloned(),
        }
    }

    // flock_meta_clone is an entry's merge metadata, present.
    fn flock_meta_clone(m: Option<&fpb::Meta>) -> fpb::Meta {
        m.cloned().unwrap_or_default()
    }

    fn flock_stamp<'a>(m: Option<&'a fpb::Meta>, path: &str) -> Option<&'a fpb::Hlc> {
        m?.stamps.iter().find(|s| s.path == path).and_then(|s| s.at.as_ref())
    }

    fn flock_set_stamp(m: &mut fpb::Meta, path: &str, at: Option<&fpb::Hlc>) {
        for s in m.stamps.iter_mut() {
            if s.path == path {
                s.at = flock_hlc_max(s.at.as_ref(), at);
                return;
            }
        }
        m.stamps.push(fpb::FieldStamp { path: path.to_string(), at: flock_hlc_clone(at), ..Default::default() });
        m.stamps.sort_by(|x, y| x.path.cmp(&y.path));
    }

    fn flock_add_conflict(m: &mut fpb::Meta, path: &str) {
        if m.conflicts.iter().any(|c| c == path) {
            return;
        }
        m.conflicts.push(path.to_string());
        m.conflicts.sort();
    }

    fn flock_ops<'a>(m: Option<&'a fpb::Meta>, path: &str) -> &'a [fpb::SetOp] {
        match m.and_then(|m| m.sets.iter().find(|s| s.path == path)) {
            Some(s) => &s.ops,
            None => &[],
        }
    }

    // flock_put_op adds op to path's op set, once per dot.
    fn flock_put_op(m: &mut fpb::Meta, path: &str, op: &fpb::SetOp) {
        if !m.sets.iter().any(|s| s.path == path) {
            m.sets.push(fpb::FieldOps { path: path.to_string(), ..Default::default() });
            m.sets.sort_by(|x, y| x.path.cmp(&y.path));
        }
        let Some(set) = m.sets.iter_mut().rev().find(|s| s.path == path) else {
            return;
        };
        if set.ops.iter().any(|o| flock_hlc_compare(o.dot.as_ref(), op.dot.as_ref()) == std::cmp::Ordering::Equal) {
            return;
        }
        let mut op = op.clone();
        op.observed.sort_by(|x, y| flock_hlc_compare(Some(x), Some(y)));
        set.ops.push(op);
        set.ops.sort_by(|x, y| flock_hlc_compare(x.dot.as_ref(), y.dot.as_ref()));
    }

    // flock_merge_meta joins two entries' metadata: each stamp the newer, the
    // op sets united by dot, the conflicts united.
    fn flock_merge_meta(a: Option<&fpb::Meta>, b: Option<&fpb::Meta>) -> fpb::Meta {
        let mut out = fpb::Meta::default();
        for m in [a, b].into_iter().flatten() {
            for s in &m.stamps {
                flock_set_stamp(&mut out, &s.path, s.at.as_ref());
            }
            for s in &m.sets {
                for op in &s.ops {
                    flock_put_op(&mut out, &s.path, op);
                }
            }
            for c in &m.conflicts {
                flock_add_conflict(&mut out, c);
            }
        }
        out
    }

    // flock_live returns the live add ops of path, by element (sorted), each
    // list in dot order. An add is live under ADD_WINS unless a remove observed
    // it; under REMOVE_WINS only if it observed every remove of its element.
    fn flock_live<'a>(
        m: Option<&'a fpb::Meta>,
        path: &str,
        remove_wins: bool,
    ) -> std::collections::BTreeMap<String, Vec<&'a fpb::SetOp>> {
        let ops = flock_ops(m, path);
        let mut removes: std::collections::HashMap<&str, Vec<&fpb::SetOp>> = std::collections::HashMap::new();
        for op in ops {
            if op.remove {
                removes.entry(op.element.as_str()).or_default().push(op);
            }
        }
        let mut live: std::collections::BTreeMap<String, Vec<&'a fpb::SetOp>> = std::collections::BTreeMap::new();
        for op in ops {
            if op.remove {
                continue;
            }
            let alive = removes.get(op.element.as_str()).map_or(true, |rs| {
                !rs.iter().any(|r| {
                    (remove_wins && !flock_saw(op, r.dot.as_ref())) || (!remove_wins && flock_saw(r, op.dot.as_ref()))
                })
            });
            if alive {
                live.entry(op.element.clone()).or_default().push(op);
            }
        }
        live
    }

    fn flock_saw(op: &fpb::SetOp, dot: Option<&fpb::Hlc>) -> bool {
        op.observed.iter().any(|o| flock_hlc_compare(Some(o), dot) == std::cmp::Ordering::Equal)
    }

    // flock_add_op records an add of element at at: it observes every remove of
    // the element so far.
    fn flock_add_op(m: &mut fpb::Meta, path: &str, element: String, payload: Vec<u8>, at: &fpb::Hlc) {
        let observed = flock_ops(Some(&*m), path)
            .iter()
            .filter(|o| o.remove && o.element == element)
            .map(|o| flock_hlc_clone(o.dot.as_ref()).unwrap_or_default())
            .collect();
        let op = fpb::SetOp { element, remove: false, dot: flock_hlc_clone(Some(at)), observed, payload, ..Default::default() };
        flock_put_op(m, path, &op);
    }

    // flock_remove_op records a remove of element at at: it observes every add
    // of the element so far.
    fn flock_remove_op(m: &mut fpb::Meta, path: &str, element: String, at: &fpb::Hlc) {
        let observed = flock_ops(Some(&*m), path)
            .iter()
            .filter(|o| !o.remove && o.element == element)
            .map(|o| flock_hlc_clone(o.dot.as_ref()).unwrap_or_default())
            .collect();
        let op = fpb::SetOp { element, remove: true, dot: flock_hlc_clone(Some(at)), observed, ..Default::default() };
        flock_put_op(m, path, &op);
    }

    // flock_born_with reports whether path holds an add of element made at the
    // entry's creation.
    fn flock_born_with(m: Option<&fpb::Meta>, path: &str, element: &str, created: Option<&fpb::Hlc>) -> bool {
        flock_ops(m, path).iter().any(|o| {
            !o.remove && o.element == element && flock_hlc_compare(o.dot.as_ref(), created) == std::cmp::Ordering::Equal
        })
    }

    // The canonical element encoding: each identity value as "<length>:<text>",
    // where text is a string as is, bytes in lowercase hex, an integer or enum
    // number in decimal, a bool "true" or "false".
    fn flock_id_string(s: &str) -> String {
        format!("{}:{}", s.len(), s)
    }
    fn flock_id_bytes(b: &[u8]) -> String {
        flock_id_string(&::hex::encode(b))
    }
    fn flock_id_int(v: i64) -> String {
        flock_id_string(&v.to_string())
    }
    fn flock_id_uint(v: u64) -> String {
        flock_id_string(&v.to_string())
    }
    fn flock_id_bool(v: bool) -> String {
        flock_id_string(if v { "true" } else { "false" })
    }

    fn flock_id_text(id: &str) -> &str {
        match id.find(':') {
            Some(i) => &id[i + 1..],
            None => "",
        }
    }

    // flock_marshal is the encoding the merge compares and the digest hashes:
    // prost writes fields in field number order, as Go's deterministic marshal.
    fn flock_marshal<M: ::prost::Message>(m: &M) -> Vec<u8> {
        m.encode_to_vec()
    }

    fn flock_min_non_zero<T: PartialOrd + Default + Clone>(a: &T, b: &T) -> T {
        let zero = T::default();
        if *a == zero {
            return b.clone();
        }
        if *b == zero {
            return a.clone();
        }
        if b < a {
            return b.clone();
        }
        a.clone()
    }

    fn flock_max_non_zero<T: PartialOrd + Default + Clone>(a: &T, b: &T) -> T {
        let zero = T::default();
        if *a == zero {
            return b.clone();
        }
        if *b == zero {
            return a.clone();
        }
        if b > a {
            return b.clone();
        }
        a.clone()
    }

    fn flock_digest<M: ::prost::Message>(m: &M) -> Vec<u8> {
        use sha2::Digest;
        sha2::Sha256::digest(flock_marshal(m)).to_vec()
    }
`
