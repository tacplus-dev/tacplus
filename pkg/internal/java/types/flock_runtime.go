// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

// javaFlockRuntime is the schema-independent half of flock state (tacplus#206)
// for Java: the hybrid clock, the deletion mark order, the op sets of SET_*
// fields and the canonical element encoding. flock_gen.go emits it once into
// the FlockHelpers class of every package with a flock entry, beside that
// package's per-entry merge. It is a line-for-line port of goFlockRuntime
// (pkg/internal/go/types/flock_runtime.go); change one and change them all
// (cross-test/flock checks they agree byte for byte).
//
// A Go nil message is a Java null where a value is optional (a clock time, a
// mark): flockHlcClone returns null for the zero time, so a zero time is
// stored absent, as Go stores it. Readers treat null and the default instance
// alike, as Go's getters do. Strings compare by code point, which is Go's
// byte order of their UTF-8 encoding.
//
// Placeholders: $F is the Java outer class of sys/flock.proto; $SKEW,
// $REBUILD and $MAXAGE the parser's timing constants.
const javaFlockRuntime = `// ============ flock runtime (tacplus#206) ============

/** FLOCK_MAX_SKEW_MS: a remote clock time further ahead of local wall time than this never moves the local clock; the write is held. */
public static final long FLOCK_MAX_SKEW_MS = $SKEWL;
/** FLOCK_REBUILD_AFTER_MS: a copy absent longer than this is wiped and rebuilt from its peers, never merged. */
public static final long FLOCK_REBUILD_AFTER_MS = $REBUILDL;
/** FLOCK_MAX_MESSAGE_AGE_MS: replicated state older than this is dropped, and a mark is purged only this long after its last acknowledgement. */
public static final long FLOCK_MAX_MESSAGE_AGE_MS = $MAXAGEL;

/** The order of strings flock sorts by: Go's, the byte order of their UTF-8 encoding. */
static final java.util.Comparator<String> FLOCK_STRING_ORDER = FlockHelpers::flockStrCmp;

private FlockHelpers() {
}

/** FlockOverlap is the verdict of the unique check on two live entries that share an element of a (spec.unique) set. */
public enum FlockOverlap {
/** Refuse to use the element and alert; a person resolves it with proof. */
QUARANTINE,
/** The partition race (both entries were created with the element, within the window): merge them. */
AUTO_MERGE
}

/** FlockEntries is the result of merging two entries that are one: the survivor and the loser, marked MERGED into it. */
public static final class FlockEntries<T> {
private final T survivor;
private final T loser;

FlockEntries(T survivor, T loser) {
this.survivor = survivor;
this.loser = loser;
}

/** The older entry, which absorbed the other. */
public T survivor() {
return survivor;
}

/** The other entry, marked MERGED into the survivor. */
public T loser() {
return loser;
}
}

/** FlockClock is a copy's hybrid logical clock. */
public static final class FlockClock {
private final String node;
private final java.util.function.LongSupplier wall;
private $F.Hlc last = $F.Hlc.getDefaultInstance();

/** Returns the clock of copy node; wall returns wall-clock Unix milliseconds (null: the system clock). */
public FlockClock(String node, java.util.function.LongSupplier wall) {
this.node = node;
this.wall = wall == null ? System::currentTimeMillis : wall;
}

/** Returns a time later than every time this clock has made or observed. */
public $F.Hlc now() {
long w = wall.getAsLong();
if (w > last.getWallMs()) {
last = $F.Hlc.newBuilder().setWallMs(w).setNode(node).build();
} else {
last = $F.Hlc.newBuilder().setWallMs(last.getWallMs()).setCounter(last.getCounter() + 1).setNode(node).build();
}
return last;
}

/**
 * Moves the clock past a remote time, so this copy's next write is later than
 * it. A remote time more than FLOCK_MAX_SKEW_MS ahead of local wall time is
 * refused (false): the caller holds that write until local time catches up,
 * and the clock does not move.
 */
public boolean observe($F.Hlc remote) {
$F.Hlc r = flockHlcOrZero(remote);
if (r.getWallMs() > wall.getAsLong() + FLOCK_MAX_SKEW_MS) {
return false;
}
if (flockHlcCompare(r, last) > 0) {
last = $F.Hlc.newBuilder().setWallMs(r.getWallMs()).setCounter(r.getCounter()).setNode(node).build();
}
return true;
}
}

/** Compares two strings in Go's order: by code point, the byte order of their UTF-8 encoding. */
static int flockStrCmp(String a, String b) {
int i = 0;
int j = 0;
while (i < a.length() && j < b.length()) {
int ca = a.codePointAt(i);
int cb = b.codePointAt(j);
if (ca != cb) {
return ca < cb ? -1 : 1;
}
i += Character.charCount(ca);
j += Character.charCount(cb);
}
boolean aMore = i < a.length();
boolean bMore = j < b.length();
if (aMore == bMore) {
return 0;
}
return aMore ? 1 : -1;
}

/** Compares two byte strings as Go's bytes.Compare does: unsigned, lexicographic. */
static int flockBytesCompare(byte[] a, byte[] b) {
int n = Math.min(a.length, b.length);
for (int i = 0; i < n; i++) {
int c = Integer.compare(a[i] & 0xff, b[i] & 0xff);
if (c != 0) {
return c;
}
}
return Integer.compare(a.length, b.length);
}

static $F.Hlc flockHlcOrZero($F.Hlc a) {
return a == null ? $F.Hlc.getDefaultInstance() : a;
}

/** Orders two clock times by (wall_ms, counter, node); an unset time is the smallest. */
public static int flockHlcCompare($F.Hlc a, $F.Hlc b) {
$F.Hlc x = flockHlcOrZero(a);
$F.Hlc y = flockHlcOrZero(b);
if (x.getWallMs() != y.getWallMs()) {
return Long.compare(x.getWallMs(), y.getWallMs());
}
if (x.getCounter() != y.getCounter()) {
return Integer.compareUnsigned(x.getCounter(), y.getCounter());
}
return flockStrCmp(x.getNode(), y.getNode());
}

static boolean flockHlcZero($F.Hlc a) {
return a == null || a.getWallMs() == 0 && a.getCounter() == 0 && a.getNode().isEmpty();
}

static $F.Hlc flockHlcMax($F.Hlc a, $F.Hlc b) {
if (flockHlcCompare(a, b) >= 0) {
return flockHlcClone(a);
}
return flockHlcClone(b);
}

/** A clock time as flock stores it: the zero time is absent (null). */
static $F.Hlc flockHlcClone($F.Hlc a) {
if (flockHlcZero(a)) {
return null;
}
return a;
}

/** Returns a new random entry key (128 bits, hex). flock creates every key; a caller never chooses one. */
public static String flockNewKey() {
byte[] b = new byte[16];
try {
new java.security.SecureRandom().nextBytes(b);
} catch (RuntimeException e) {
throw new ProtoException(ProtoException.INTERNAL, "flock: no randomness for a new key", e);
}
return flockHex(b);
}

/** Reports whether a copy last heard from at lastSeenMs is absent too long to merge: it is wiped and rebuilt from its peers. */
public static boolean flockMustRebuild(long lastSeenMs, long nowMs) {
return nowMs - lastSeenMs > FLOCK_REBUILD_AFTER_MS;
}

/** Reports whether replicated state sent at sent is too old to apply: a mark may already be purged everywhere. */
public static boolean flockMessageTooOld($F.Hlc sent, long nowMs) {
return nowMs - flockHlcOrZero(sent).getWallMs() > FLOCK_MAX_MESSAGE_AGE_MS;
}

static int flockReasonRank(int reason) {
if (reason == $F.DeleteReason.DELETE_REASON_USER_VALUE) {
return 3;
}
if (reason == $F.DeleteReason.DELETE_REASON_MERGED_VALUE) {
return 2;
}
if (reason == $F.DeleteReason.DELETE_REASON_EXPIRED_VALUE) {
return 1;
}
return 0;
}

static boolean flockMarked($F.DeletionMark m) {
return m != null && flockReasonRank(m.getReasonValue()) > 0;
}

/**
 * Orders two marks, acks aside: &gt; 0 when a wins. USER over MERGED over
 * EXPIRED; for MERGED the oldest target, then the smaller target key; then
 * the earliest mark.
 */
static int flockMarkCompare($F.DeletionMark a, $F.DeletionMark b) {
int c = Integer.compare(flockReasonRank(a.getReasonValue()), flockReasonRank(b.getReasonValue()));
if (c != 0) {
return c;
}
c = flockHlcCompare(b.getMergedIntoCreated(), a.getMergedIntoCreated());
if (c != 0) {
return c;
}
c = flockStrCmp(b.getMergedInto(), a.getMergedInto());
if (c != 0) {
return c;
}
return flockHlcCompare(b.getAt(), a.getAt());
}

/** Joins two marks: a mark wins over none, the larger mark over the smaller, and the acks of equal marks are united (each copy's earliest). */
static $F.DeletionMark flockMergeMark($F.DeletionMark a, $F.DeletionMark b) {
if (!flockMarked(a) && !flockMarked(b)) {
return null;
}
if (!flockMarked(b)) {
return flockMarkNorm(a, java.util.Collections.emptyList());
}
if (!flockMarked(a)) {
return flockMarkNorm(b, java.util.Collections.emptyList());
}
int c = flockMarkCompare(a, b);
if (c > 0) {
return flockMarkNorm(a, java.util.Collections.emptyList());
}
if (c < 0) {
return flockMarkNorm(b, java.util.Collections.emptyList());
}
return flockMarkNorm(a, b.getAcksList());
}

static $F.DeletionMark flockMarkNorm($F.DeletionMark m, java.util.List<$F.MarkAck> more) {
java.util.TreeMap<String, $F.Hlc> byCopy = new java.util.TreeMap<>(FLOCK_STRING_ORDER);
java.util.List<$F.MarkAck> all = new java.util.ArrayList<>(m.getAcksList());
all.addAll(more);
for ($F.MarkAck a : all) {
if (!byCopy.containsKey(a.getCopy()) || flockHlcCompare(a.getAt(), byCopy.get(a.getCopy())) < 0) {
byCopy.put(a.getCopy(), a.getAt());
}
}
$F.DeletionMark.Builder out = m.toBuilder().clearAcks();
for (java.util.Map.Entry<String, $F.Hlc> e : byCopy.entrySet()) {
$F.MarkAck.Builder ack = $F.MarkAck.newBuilder().setCopy(e.getKey());
$F.Hlc at = flockHlcClone(e.getValue());
if (at != null) {
ack.setAt(at);
}
out.addAcks(ack);
}
return out.build();
}

static String flockReasonName(int reason) {
$F.DeleteReason r = $F.DeleteReason.forNumber(reason);
return r == null ? Integer.toString(reason) : r.name();
}

static ProtoException flockMarkError(String what, String key, $F.DeletionMark m) {
if (m.getReasonValue() == $F.DeleteReason.DELETE_REASON_MERGED_VALUE) {
return new ProtoException(ProtoException.NOT_FOUND, what + " " + key + " is merged into " + m.getMergedInto());
}
return new ProtoException(ProtoException.NOT_FOUND, what + " " + key + " is deleted (" + flockReasonName(m.getReasonValue()) + ")");
}

static boolean flockPurgeReady($F.DeletionMark m, java.util.Collection<String> members, long nowMs) {
if (!flockMarked(m)) {
return false;
}
java.util.Map<String, Long> acked = new java.util.HashMap<>();
for ($F.MarkAck a : m.getAcksList()) {
acked.put(a.getCopy(), a.getAt().getWallMs());
}
long last = 0;
for (String c : members) {
Long at = acked.get(c);
if (at == null) {
return false;
}
if (at > last) {
last = at;
}
}
return nowMs - last >= FLOCK_MAX_MESSAGE_AGE_MS;
}

static $F.DeletionMark flockAck($F.DeletionMark m, String copy, $F.Hlc at) {
if (!flockMarked(m)) {
return m;
}
$F.MarkAck.Builder ack = $F.MarkAck.newBuilder().setCopy(copy);
$F.Hlc a = flockHlcClone(at);
if (a != null) {
ack.setAt(a);
}
return flockMarkNorm(m, java.util.Collections.singletonList(ack.build()));
}

/** An entry's merge metadata, as a builder to change. */
static $F.Meta.Builder flockMetaClone($F.Meta m) {
if (m == null) {
return $F.Meta.newBuilder();
}
return m.toBuilder();
}

static $F.Hlc flockStamp($F.MetaOrBuilder m, String path) {
if (m == null) {
return null;
}
for ($F.FieldStamp s : m.getStampsList()) {
if (s.getPath().equals(path)) {
return s.getAt();
}
}
return null;
}

static void flockSetStamp($F.Meta.Builder m, String path, $F.Hlc at) {
for (int i = 0; i < m.getStampsCount(); i++) {
$F.FieldStamp s = m.getStamps(i);
if (s.getPath().equals(path)) {
$F.FieldStamp.Builder nb = s.toBuilder();
$F.Hlc mx = flockHlcMax(s.getAt(), at);
if (mx == null) {
nb.clearAt();
} else {
nb.setAt(mx);
}
m.setStamps(i, nb);
return;
}
}
$F.FieldStamp.Builder ns = $F.FieldStamp.newBuilder().setPath(path);
$F.Hlc c = flockHlcClone(at);
if (c != null) {
ns.setAt(c);
}
java.util.List<$F.FieldStamp> stamps = new java.util.ArrayList<>(m.getStampsList());
stamps.add(ns.build());
stamps.sort((x, y) -> flockStrCmp(x.getPath(), y.getPath()));
m.clearStamps().addAllStamps(stamps);
}

static void flockAddConflict($F.Meta.Builder m, String path) {
for (String c : m.getConflictsList()) {
if (c.equals(path)) {
return;
}
}
java.util.List<String> conflicts = new java.util.ArrayList<>(m.getConflictsList());
conflicts.add(path);
conflicts.sort(FLOCK_STRING_ORDER);
m.clearConflicts().addAllConflicts(conflicts);
}

static java.util.List<$F.SetOp> flockOps($F.MetaOrBuilder m, String path) {
if (m == null) {
return java.util.Collections.emptyList();
}
for ($F.FieldOps s : m.getSetsList()) {
if (s.getPath().equals(path)) {
return s.getOpsList();
}
}
return java.util.Collections.emptyList();
}

/** Adds op to path's op set, once per dot. */
static void flockPutOp($F.Meta.Builder m, String path, $F.SetOp op) {
java.util.List<$F.FieldOps> sets = new java.util.ArrayList<>(m.getSetsList());
$F.FieldOps set = null;
for ($F.FieldOps s : sets) {
if (s.getPath().equals(path)) {
set = s;
}
}
if (set == null) {
set = $F.FieldOps.newBuilder().setPath(path).build();
sets.add(set);
sets.sort((x, y) -> flockStrCmp(x.getPath(), y.getPath()));
}
for ($F.SetOp o : set.getOpsList()) {
if (flockHlcCompare(o.getDot(), op.getDot()) == 0) {
m.clearSets().addAllSets(sets);
return;
}
}
java.util.List<$F.Hlc> observed = new java.util.ArrayList<>(op.getObservedList());
observed.sort(FlockHelpers::flockHlcCompare);
$F.SetOp nop = op.toBuilder().clearObserved().addAllObserved(observed).build();
java.util.List<$F.SetOp> ops = new java.util.ArrayList<>(set.getOpsList());
ops.add(nop);
ops.sort((x, y) -> flockHlcCompare(x.getDot(), y.getDot()));
$F.FieldOps nset = set.toBuilder().clearOps().addAllOps(ops).build();
for (int i = 0; i < sets.size(); i++) {
if (sets.get(i).getPath().equals(path)) {
sets.set(i, nset);
}
}
m.clearSets().addAllSets(sets);
}

/** Joins two entries' metadata: each stamp the newer, the op sets united by dot, the conflicts united. */
static $F.Meta.Builder flockMergeMeta($F.Meta a, $F.Meta b) {
$F.Meta.Builder out = $F.Meta.newBuilder();
for ($F.Meta m : java.util.Arrays.asList(a, b)) {
if (m == null) {
continue;
}
for ($F.FieldStamp s : m.getStampsList()) {
flockSetStamp(out, s.getPath(), s.getAt());
}
for ($F.FieldOps s : m.getSetsList()) {
for ($F.SetOp op : s.getOpsList()) {
flockPutOp(out, s.getPath(), op);
}
}
for (String c : m.getConflictsList()) {
flockAddConflict(out, c);
}
}
return out;
}

/**
 * Returns the live add ops of path, by element (in element order), each list
 * in dot order. An add is live under ADD_WINS unless a remove observed it;
 * under REMOVE_WINS only if it observed every remove of its element.
 */
static java.util.SortedMap<String, java.util.List<$F.SetOp>> flockLive($F.MetaOrBuilder m, String path, boolean removeWins) {
java.util.List<$F.SetOp> ops = flockOps(m, path);
java.util.Map<String, java.util.List<$F.SetOp>> removes = new java.util.HashMap<>();
for ($F.SetOp op : ops) {
if (op.getRemove()) {
removes.computeIfAbsent(op.getElement(), k -> new java.util.ArrayList<>()).add(op);
}
}
java.util.SortedMap<String, java.util.List<$F.SetOp>> live = new java.util.TreeMap<>(FLOCK_STRING_ORDER);
for ($F.SetOp op : ops) {
if (op.getRemove()) {
continue;
}
boolean alive = true;
for ($F.SetOp r : removes.getOrDefault(op.getElement(), java.util.Collections.emptyList())) {
if (removeWins && !flockSaw(op, r.getDot()) || !removeWins && flockSaw(r, op.getDot())) {
alive = false;
break;
}
}
if (alive) {
live.computeIfAbsent(op.getElement(), k -> new java.util.ArrayList<>()).add(op);
}
}
return live;
}

static boolean flockSaw($F.SetOp op, $F.Hlc dot) {
for ($F.Hlc o : op.getObservedList()) {
if (flockHlcCompare(o, dot) == 0) {
return true;
}
}
return false;
}

/** Records an add of element at at: it observes every remove of the element so far. */
static void flockAddOp($F.Meta.Builder m, String path, String element, com.google.protobuf.ByteString payload, $F.Hlc at) {
$F.SetOp.Builder op = $F.SetOp.newBuilder().setElement(element).setPayload(payload);
$F.Hlc dot = flockHlcClone(at);
if (dot != null) {
op.setDot(dot);
}
for ($F.SetOp o : flockOps(m, path)) {
if (o.getRemove() && o.getElement().equals(element)) {
op.addObserved(flockHlcOrZero(flockHlcClone(o.getDot())));
}
}
flockPutOp(m, path, op.build());
}

/** Records a remove of element at at: it observes every add of the element so far. */
static void flockRemoveOp($F.Meta.Builder m, String path, String element, $F.Hlc at) {
$F.SetOp.Builder op = $F.SetOp.newBuilder().setElement(element).setRemove(true);
$F.Hlc dot = flockHlcClone(at);
if (dot != null) {
op.setDot(dot);
}
for ($F.SetOp o : flockOps(m, path)) {
if (!o.getRemove() && o.getElement().equals(element)) {
op.addObserved(flockHlcOrZero(flockHlcClone(o.getDot())));
}
}
flockPutOp(m, path, op.build());
}

/** Reports whether path holds an add of element made at the entry's creation. */
static boolean flockBornWith($F.MetaOrBuilder m, String path, String element, $F.Hlc created) {
for ($F.SetOp o : flockOps(m, path)) {
if (!o.getRemove() && o.getElement().equals(element) && flockHlcCompare(o.getDot(), created) == 0) {
return true;
}
}
return false;
}

// The canonical element encoding: each identity value as "<length>:<text>",
// where length counts UTF-8 bytes and text is a string as is, bytes in
// lowercase hex, an integer or enum number in decimal, a bool "true" or "false".
static String flockIDString(String s) {
return s.getBytes(java.nio.charset.StandardCharsets.UTF_8).length + ":" + s;
}

static String flockIDBytes(com.google.protobuf.ByteString b) {
return flockIDString(flockHex(b.toByteArray()));
}

static String flockIDInt(long v) {
return flockIDString(Long.toString(v));
}

static String flockIDUint(long v) {
return flockIDString(Long.toUnsignedString(v));
}

static String flockIDBool(boolean v) {
return flockIDString(v ? "true" : "false");
}

static String flockIDText(String id) {
int i = id.indexOf(':');
if (i >= 0) {
return id.substring(i + 1);
}
return "";
}

static long flockParseInt(String t) {
try {
return Long.parseLong(t);
} catch (NumberFormatException e) {
return 0L;
}
}

static long flockParseUint(String t) {
try {
return Long.parseUnsignedLong(t);
} catch (NumberFormatException e) {
return 0L;
}
}

static String flockHex(byte[] b) {
StringBuilder sb = new StringBuilder(b.length * 2);
for (byte x : b) {
sb.append(Character.forDigit((x >> 4) & 0xf, 16));
sb.append(Character.forDigit(x & 0xf, 16));
}
return sb.toString();
}

/** Decodes lowercase or uppercase hex, as far as it is valid (Go's hex.DecodeString with the error dropped). */
static com.google.protobuf.ByteString flockUnhex(String s) {
java.io.ByteArrayOutputStream out = new java.io.ByteArrayOutputStream();
for (int i = 0; i + 1 < s.length(); i += 2) {
int hi = Character.digit(s.charAt(i), 16);
int lo = Character.digit(s.charAt(i + 1), 16);
if (hi < 0 || lo < 0) {
break;
}
out.write((hi << 4) | lo);
}
return com.google.protobuf.ByteString.copyFrom(out.toByteArray());
}

/** An enum value's number; an unrecognized value is refused. */
static int flockEnumNumber(com.google.protobuf.ProtocolMessageEnum v) {
try {
return v.getNumber();
} catch (IllegalArgumentException e) {
throw new ProtoException(ProtoException.INVALID_ARGUMENT, "flock: an unrecognized enum value", e);
}
}

/** The deterministic protobuf encoding of m (map entries sorted by key), as Go's Deterministic marshal. */
static byte[] flockMarshal(com.google.protobuf.MessageLite m) {
try {
byte[] out = new byte[m.getSerializedSize()];
com.google.protobuf.CodedOutputStream cos = com.google.protobuf.CodedOutputStream.newInstance(out);
cos.useDeterministicSerialization();
m.writeTo(cos);
cos.checkNoSpaceLeft();
return out;
} catch (java.io.IOException e) {
throw new ProtoException(ProtoException.INTERNAL, "flock: cannot encode an entry", e);
}
}

static long flockMinNonZeroLong(long a, long b) {
if (a == 0) {
return b;
}
if (b == 0) {
return a;
}
return b < a ? b : a;
}

static long flockMaxNonZeroLong(long a, long b) {
if (a == 0) {
return b;
}
if (b == 0) {
return a;
}
return b > a ? b : a;
}

static int flockMinNonZeroInt(int a, int b) {
if (a == 0) {
return b;
}
if (b == 0) {
return a;
}
return b < a ? b : a;
}

static int flockMaxNonZeroInt(int a, int b) {
if (a == 0) {
return b;
}
if (b == 0) {
return a;
}
return b > a ? b : a;
}

static long flockMinNonZeroUlong(long a, long b) {
if (a == 0) {
return b;
}
if (b == 0) {
return a;
}
return Long.compareUnsigned(b, a) < 0 ? b : a;
}

static long flockMaxNonZeroUlong(long a, long b) {
if (a == 0) {
return b;
}
if (b == 0) {
return a;
}
return Long.compareUnsigned(b, a) > 0 ? b : a;
}

static int flockMinNonZeroUint(int a, int b) {
if (a == 0) {
return b;
}
if (b == 0) {
return a;
}
return Integer.compareUnsigned(b, a) < 0 ? b : a;
}

static int flockMaxNonZeroUint(int a, int b) {
if (a == 0) {
return b;
}
if (b == 0) {
return a;
}
return Integer.compareUnsigned(b, a) > 0 ? b : a;
}

static String flockMinNonZeroString(String a, String b) {
if (a.isEmpty()) {
return b;
}
if (b.isEmpty()) {
return a;
}
return flockStrCmp(b, a) < 0 ? b : a;
}

static String flockMaxNonZeroString(String a, String b) {
if (a.isEmpty()) {
return b;
}
if (b.isEmpty()) {
return a;
}
return flockStrCmp(b, a) > 0 ? b : a;
}

static byte[] flockDigest(com.google.protobuf.MessageLite m) {
try {
return java.security.MessageDigest.getInstance("SHA-256").digest(flockMarshal(m));
} catch (java.security.NoSuchAlgorithmException e) {
throw new ProtoException(ProtoException.INTERNAL, "flock: no SHA-256", e);
}
}
`
