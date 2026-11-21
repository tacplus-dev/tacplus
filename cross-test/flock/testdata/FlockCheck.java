// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The java leg of the flock gate (tacplus#206), compiled against the GENERATED
// flockcheck model by cross-test/flock/run.sh. A port of the Go leg
// (flock_test.go), check for check: it tests the generated merge of the people
// table two ways, and the vector pins it to the other languages:
//
//   - properties: over random copies of one entry (random writes on random
//     copies, with marks and acks), flockMergePerson is commutative,
//     associative and idempotent over whole entries plus marks, and
//     flockMergeEntriesPerson picks the same survivor and loser mark whatever
//     the order;
//   - simulation: copies that write, gossip whole snapshots through a network
//     that delivers in random order, partitions, holds far-future writes,
//     merges partition duplicates, acks and purges marks, then heal and
//     converge. It checks convergence (equal digests everywhere), no
//     resurrection (a copy never again holds live an entry it saw marked) and
//     no redirect cycles (every MERGED mark points to a strictly older entry);
//   - the vector: the same fixed scenario as every leg, whose digests must equal
//     cross-test/fixtures/flock_vectors.txt byte for byte.
//
// The random sequence differs from Go's (a different generator); only the
// vector must match.
package flockcheck;

import com.gitlab.example.tacplus.flock.flockcheck.model.types.people.flockcheck.FlockHelpers;
import com.gitlab.example.tacplus.flock.flockcheck.model.types.people.flockcheck.FlockHelpers.FlockClock;
import com.gitlab.example.tacplus.flock.flockcheck.model.types.people.flockcheck.FlockHelpers.FlockEntries;
import com.gitlab.example.tacplus.flock.flockcheck.model.types.people.flockcheck.FlockHelpers.FlockOverlap;
import com.gitlab.example.tacplus.flock.flockcheck.model.types.people.flockcheck.Flockcheck.Device;
import com.gitlab.example.tacplus.flock.flockcheck.model.types.people.flockcheck.Flockcheck.Link;
import com.gitlab.example.tacplus.flock.flockcheck.model.types.people.flockcheck.Flockcheck.Membership;
import com.gitlab.example.tacplus.flock.flockcheck.model.types.people.flockcheck.Flockcheck.Person;
import com.gitlab.example.tacplus.flock.flockcheck.model.types.people.flockcheck.Flockcheck.Session;
import com.gitlab.example.tacplus.flock.flockcheck.model.types.sys.errors.ProtoException;
import com.gitlab.example.tacplus.flock.flockcheck.model.types.sys.flock.Flock.DeleteReason;
import com.gitlab.example.tacplus.flock.flockcheck.model.types.sys.flock.Flock.Hlc;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.HashSet;
import java.util.HexFormat;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.SplittableRandom;
import java.util.TreeMap;
import java.util.function.Function;

public final class FlockCheck {
    private FlockCheck() {}

    static final int PROPERTY_CASES = 300;
    static final int SIMULATIONS = 40;
    static final int SIM_STEPS = 400;
    static final int SIM_COPIES = 4;
    static final long RACE_WINDOW_MS = 5_000;

    static final String[] IDPS = {"google", "microsoft", "apple"};
    static final String[] TENANTS = {"alpha", "beta", "gamma"};
    static final String[] NAMES = {"ann", "bob", "cy", "di"};

    static void fail(String msg) {
        throw new AssertionError(msg);
    }

    // ---------- properties ----------

    /** Applies one random write to e on a copy whose clock is c. */
    static void randomWrite(SplittableRandom r, Person.Builder e, FlockClock c) {
        Hlc at = c.now();
        switch (r.nextInt(16)) {
            case 0:
                FlockHelpers.flockSetPersonDisplay(e, NAMES[r.nextInt(NAMES.length)], at);
                break;
            case 1:
                FlockHelpers.flockSetPersonGeneration(e, r.nextInt(5), at);
                break;
            case 2:
                FlockHelpers.flockSetPersonFirstSeenMs(e, 1 + r.nextInt(1000), at);
                break;
            case 3:
                try {
                    FlockHelpers.flockSetPersonHomeRegion(e, new String[] {"eu", "us"}[r.nextInt(2)], at);
                } catch (ProtoException ignored) {
                    // refused: immutable
                }
                break;
            case 4:
                FlockHelpers.flockSetPersonProfileNickname(e, NAMES[r.nextInt(NAMES.length)], at);
                break;
            case 5:
                FlockHelpers.flockSetPersonProfileLastSignInDay(e, r.nextInt(30), at);
                break;
            case 6:
                FlockHelpers.flockAddPersonProfileEmails(e, NAMES[r.nextInt(NAMES.length)] + "@x", at);
                break;
            case 7:
                FlockHelpers.flockRemovePersonProfileEmails(e, NAMES[r.nextInt(NAMES.length)] + "@x", at);
                break;
            case 8:
                FlockHelpers.flockAddPersonLinks(e, randomLink(r), at);
                break;
            case 9:
                FlockHelpers.flockRemovePersonLinks(e, randomLink(r), at);
                break;
            case 10: {
                String tenant = TENANTS[r.nextInt(TENANTS.length)];
                String role = NAMES[r.nextInt(NAMES.length)];
                FlockHelpers.flockAddPersonMemberships(e, Membership.newBuilder().setTenant(tenant).setRole(role).build(), at);
                break;
            }
            case 11:
                FlockHelpers.flockRemovePersonMemberships(e, Membership.newBuilder().setTenant(TENANTS[r.nextInt(TENANTS.length)]).build(), at);
                break;
            case 12: {
                String k = NAMES[r.nextInt(NAMES.length)];
                String name = NAMES[r.nextInt(NAMES.length)];
                long first = 1 + r.nextInt(100);
                long last = 1 + r.nextInt(100);
                FlockHelpers.flockPutPersonDevices(e, k, Device.newBuilder().setName(name).setFirstSeenMs(first).setLastSeenMs(last).build(), at);
                break;
            }
            case 13:
                FlockHelpers.flockRemovePersonDevices(e, NAMES[r.nextInt(NAMES.length)], at);
                break;
            case 14:
                if (r.nextInt(2) == 0) {
                    FlockHelpers.flockAddPersonTags(e, NAMES[r.nextInt(NAMES.length)], at);
                } else {
                    FlockHelpers.flockRemovePersonTags(e, NAMES[r.nextInt(NAMES.length)], at);
                }
                break;
            default:
                switch (r.nextInt(4)) {
                    case 0:
                        FlockHelpers.flockMarkPerson(e, DeleteReason.DELETE_REASON_USER, at);
                        break;
                    case 1:
                        FlockHelpers.flockMarkPerson(e, DeleteReason.DELETE_REASON_EXPIRED, at);
                        break;
                    case 2:
                        FlockHelpers.flockAckPerson(e, "c" + r.nextInt(3), at);
                        break;
                    default: {
                        String x = NAMES[r.nextInt(NAMES.length)];
                        String y = NAMES[r.nextInt(NAMES.length)];
                        FlockHelpers.flockSetPersonAliases(e, Arrays.asList(x, y), at);
                        break;
                    }
                }
                break;
        }
    }

    static Link randomLink(SplittableRandom r) {
        String idp = IDPS[r.nextInt(IDPS.length)];
        String subject = NAMES[r.nextInt(NAMES.length)];
        long added = 1 + r.nextInt(100);
        int assurance = r.nextInt(3);
        return Link.newBuilder().setIdp(idp).setSubject(subject).setAddedMs(added).setAssurance(assurance).build();
    }

    /**
     * Returns three copies of one entry that diverged from a common ancestor by
     * random writes on three clocks, each also having merged in some of the
     * others' states, so they share history.
     */
    static Person[] randomCopies(SplittableRandom r, long[] wall) {
        FlockClock[] clocks = {
            new FlockClock("c0", () -> wall[0]), new FlockClock("c1", () -> wall[0]), new FlockClock("c2", () -> wall[0]),
        };
        Person base = FlockHelpers.flockNewPerson("k1", clocks[0].now());
        Person[] out = {base, base, base};
        for (int step = 0; step < 30; step++) {
            wall[0] += r.nextInt(3);
            int i = r.nextInt(3);
            if (r.nextInt(6) == 0) {
                int j = r.nextInt(3);
                out[i] = FlockHelpers.flockMergePerson(out[i], out[j]);
                continue;
            }
            Person.Builder b = out[i].toBuilder();
            randomWrite(r, b, clocks[i]);
            out[i] = b.build();
        }
        return out;
    }

    static Person merge(Person a, Person b) {
        return FlockHelpers.flockMergePerson(a, b);
    }

    static void samePerson(String what, Person a, Person b) {
        if (!Arrays.equals(FlockHelpers.flockDigestPerson(a), FlockHelpers.flockDigestPerson(b)) || !a.equals(b)) {
            fail(what + ":\n  " + a + "\n  " + b);
        }
    }

    /** marked reports whether flockLivePerson refuses e. */
    static boolean marked(Person e) {
        try {
            FlockHelpers.flockLivePerson(e);
            return false;
        } catch (ProtoException ex) {
            return true;
        }
    }

    static void properties() {
        long[] wall = {1_000};
        SplittableRandom r = new SplittableRandom(206);
        for (int n = 0; n < PROPERTY_CASES; n++) {
            Person[] c = randomCopies(r, wall);
            Person a = c[0];
            Person b = c[1];
            Person d = c[2];
            samePerson("commutative", merge(a, b), merge(b, a));
            samePerson("associative", merge(merge(a, b), d), merge(a, merge(b, d)));
            Person ab = merge(a, b);
            samePerson("idempotent", merge(ab, ab), ab);
            samePerson("absorbing", merge(ab, a), ab);

            // Two different entries: the same survivor and loser, either order.
            Person other = d.toBuilder()
                    .setKey("k0")
                    .setCreated(Hlc.newBuilder().setWallMs(d.getCreated().getWallMs() + r.nextInt(3) - 1).setNode("c9"))
                    .build();
            Hlc at1 = Hlc.newBuilder().setWallMs(wall[0]).setNode("c0").build();
            Hlc at2 = Hlc.newBuilder().setWallMs(wall[0] + 1).setNode("c1").build();
            FlockEntries<Person> e1 = FlockHelpers.flockMergeEntriesPerson(ab, other, at1);
            FlockEntries<Person> e2 = FlockHelpers.flockMergeEntriesPerson(other, ab, at2);
            Person s1 = e1.survivor();
            Person l1 = e1.loser();
            Person s2 = e2.survivor();
            Person l2 = e2.loser();
            if (!s1.getKey().equals(s2.getKey()) || !l1.getKey().equals(l2.getKey())) {
                fail("merge_entries picked " + s1.getKey() + " then " + s2.getKey());
            }
            // The two merges' marks differ only in time; merging the results converges.
            samePerson("merge_entries survivor", s1, merge(s1, s2));
            samePerson("merge_entries survivor (other order)", merge(s1, s2), merge(s2, s1));
            samePerson("merge_entries loser", merge(l1, l2), merge(l2, l1));
            Person l = merge(l1, l2);
            if (l.getDeleted().getReason() != DeleteReason.DELETE_REASON_MERGED && l.getDeleted().getReason() != DeleteReason.DELETE_REASON_USER) {
                fail("loser not marked: " + l.getDeleted());
            }
            if (FlockHelpers.flockHlcCompare(s1.getCreated(), l1.getCreated()) > 0) {
                fail("survivor " + s1.getCreated() + " is younger than loser " + l1.getCreated());
            }
        }
        checkScenarios();
    }

    /** checkScenarios pins the cases the panel named. */
    static void checkScenarios() {
        long now = 5_000;
        Function<String, FlockClock> clock = node -> new FlockClock(node, () -> now);
        FlockClock ca = clock.apply("a");
        FlockClock cb = clock.apply("b");
        Person base = FlockHelpers.flockNewPerson("p", ca.now());
        Link link = Link.newBuilder().setIdp("google").setSubject("s").build();

        // flock creates keys: 128 random bits, hex, never repeated.
        String k1 = FlockHelpers.flockNewKey(), k2 = FlockHelpers.flockNewKey();
        if (k1.length() != 32 || k1.equals(k2)) {
            fail("flockNewKey: " + k1 + " " + k2);
        }

        // A re-add after a remove it saw survives remove-wins.
        Person.Builder xb = base.toBuilder();
        FlockHelpers.flockAddPersonLinks(xb, link, ca.now());
        FlockHelpers.flockRemovePersonLinks(xb, link, ca.now());
        FlockHelpers.flockAddPersonLinks(xb, link, ca.now());
        Person x = xb.build();
        if (x.getLinksCount() != 1) {
            fail("re-add lost: " + x.getLinksList());
        }
        // A concurrent add and remove: remove wins on links, add wins on tags.
        Person.Builder y = x.toBuilder();
        Person.Builder z = x.toBuilder();
        FlockHelpers.flockRemovePersonLinks(y, link, ca.now());
        FlockHelpers.flockAddPersonLinks(z, link, cb.now());
        FlockHelpers.flockAddPersonTags(z, "t", cb.now());
        FlockHelpers.flockAddPersonTags(y, "t", ca.now());
        FlockHelpers.flockRemovePersonTags(y, "t", ca.now());
        Person m = merge(y.build(), z.build());
        if (m.getLinksCount() != 0) {
            fail("remove-wins: concurrent add won: " + m.getLinksList());
        }
        if (m.getTagsCount() != 1) {
            fail("add-wins: concurrent remove won: " + m.getTagsList());
        }
        // A removed device stays removed when an older copy comes back.
        Person.Builder d = base.toBuilder();
        FlockHelpers.flockPutPersonDevices(d, "phone", Device.newBuilder().setName("phone").build(), ca.now());
        Person old = d.build();
        FlockHelpers.flockRemovePersonDevices(d, "phone", ca.now());
        Map<String, Device> got = merge(old, d.build()).getDevicesMap();
        if (!got.isEmpty()) {
            fail("revoked device came back: " + got);
        }
        // A mark wins over a later write, and a USER mark over a MERGED one.
        Person.Builder w = base.toBuilder();
        FlockHelpers.flockMarkPerson(w, DeleteReason.DELETE_REASON_EXPIRED, ca.now());
        Person.Builder v = base.toBuilder();
        FlockHelpers.flockSetPersonDisplay(v, "later", cb.now());
        if (merge(w.build(), v.build()).getDeleted().getReason() != DeleteReason.DELETE_REASON_EXPIRED) {
            fail("a write undid a mark");
        }
        if (!marked(w.build())) {
            fail("a marked entry is live");
        }
        // A conflicting IMMUTABLE write is recorded, not resolved silently.
        Person.Builder i1 = base.toBuilder();
        Person.Builder i2 = base.toBuilder();
        FlockHelpers.flockSetPersonHomeRegion(i1, "eu", ca.now());
        FlockHelpers.flockSetPersonHomeRegion(i2, "us", cb.now());
        List<String> conflicts = merge(i1.build(), i2.build()).getFlockMeta().getConflictsList();
        if (conflicts.size() != 1 || !conflicts.get(0).equals("home_region")) {
            fail("immutable conflict not recorded: " + conflicts);
        }
        boolean refused = false;
        try {
            FlockHelpers.flockSetPersonHomeRegion(i1, "us", ca.now());
        } catch (ProtoException ex) {
            refused = ex.getCode() == ProtoException.FAILED_PRECONDITION;
        }
        if (!refused) {
            fail("an immutable field was overwritten");
        }
        // A remote time far ahead never moves the clock.
        FlockClock c = clock.apply("h");
        if (c.observe(Hlc.newBuilder().setWallMs(now + FlockHelpers.FLOCK_MAX_SKEW_MS + 1).setNode("far").build())) {
            fail("a far-future time was accepted");
        }
        long wallNow = c.now().getWallMs();
        if (wallNow != now) {
            fail("clock moved to " + wallNow);
        }
        // The unique check refuses a local conflict; only the partition race merges.
        Person.Builder p1 = FlockHelpers.flockNewPerson("p1", ca.now()).toBuilder();
        FlockHelpers.flockAddPersonLinks(p1, link, p1.getCreated());
        Person.Builder p2 = FlockHelpers.flockNewPerson("p2", cb.now()).toBuilder();
        FlockHelpers.flockAddPersonLinks(p2, link, p2.getCreated());
        boolean conflict = false;
        try {
            FlockHelpers.flockCheckUniquePerson(p2.build(), k -> Optional.of("p1"));
        } catch (ProtoException ex) {
            conflict = ex.getCode() == ProtoException.ALREADY_EXISTS;
        }
        if (!conflict) {
            fail("a local unique conflict was accepted");
        }
        String k = FlockHelpers.flockUniqueKeysPerson(p1.build()).get(0);
        if (FlockHelpers.flockOverlapPerson(p1.build(), p2.build(), k, RACE_WINDOW_MS) != FlockOverlap.AUTO_MERGE) {
            fail("the partition race was not merged");
        }
        Person.Builder p3 = FlockHelpers.flockNewPerson("p3", cb.now()).toBuilder();
        FlockHelpers.flockAddPersonLinks(p3, link, cb.now());
        if (FlockHelpers.flockOverlapPerson(p1.build(), p3.build(), k, RACE_WINDOW_MS) != FlockOverlap.QUARANTINE) {
            fail("a link added later was merged, not quarantined");
        }
        // Session: the newer whole entry wins.
        Session s = FlockHelpers.flockNewSession("s", ca.now());
        Session.Builder s1 = s.toBuilder().setDeviceId("old");
        FlockHelpers.flockWriteSession(s1, ca.now());
        Session.Builder s2 = s.toBuilder().setDeviceId("new");
        cb.observe(s1.getWritten());
        FlockHelpers.flockWriteSession(s2, cb.now());
        String dev = FlockHelpers.flockMergeSession(s1.build(), s2.build()).getDeviceId();
        if (!dev.equals("new")) {
            fail("whole-entry LAST_WRITE kept \"" + dev + "\"");
        }
    }

    // ---------- simulation ----------

    static final class Message {
        final int from;
        final int to;
        final Hlc sent;
        final List<Person> people;

        Message(int from, int to, Hlc sent, List<Person> people) {
            this.from = from;
            this.to = to;
            this.sent = sent;
            this.people = people;
        }
    }

    static final class Replica {
        final String id;
        FlockClock clock;
        long skew;
        TreeMap<String, Person> people = new TreeMap<>();
        Set<String> seenMarked = new HashSet<>();
        long lastContact;
        boolean offline;
        // source says where each entry last came from, for a failure message.
        final Map<String, String> source = new HashMap<>();
        // maxLocal is the latest local time this copy has had.
        long maxLocal;

        Replica(String id) {
            this.id = id;
        }
    }

    /** simStats counts what the simulations exercised; each must happen. */
    static final TreeMap<String, Integer> SIM_STATS = new TreeMap<>();

    static void count(String what) {
        SIM_STATS.merge(what, 1, Integer::sum);
    }

    static final class Sim {
        final SplittableRandom r;
        long wall = 1_000_000;
        final List<Replica> copies = new ArrayList<>();
        final List<Message> net = new ArrayList<>();
        Set<Integer> cut = new HashSet<>();
        final List<String> members = new ArrayList<>();
        int nextKey;
        // longAbsence: a copy has already been away past the rebuild limit.
        boolean longAbsence;

        Sim(long seed) {
            r = new SplittableRandom(seed * 1_000_003L + 2026);
        }

        long now(Replica c) {
            return wall + c.skew;
        }

        static int edge(int i, int j) {
            return i * SIM_COPIES + j;
        }

        void localWrite(Replica c) {
            if (c.offline) {
                return;
            }
            // Create a person, born with a link from a small pool: duplicates across a
            // partition are the race the unique check merges.
            if (c.people.isEmpty() || r.nextInt(5) == 0) {
                nextKey++;
                Person.Builder e = FlockHelpers.flockNewPerson(String.format("%s-%04d", c.id, nextKey), c.clock.now()).toBuilder();
                String idp = IDPS[r.nextInt(2)];
                String subject = NAMES[r.nextInt(2)];
                FlockHelpers.flockAddPersonLinks(e, Link.newBuilder().setIdp(idp).setSubject(subject).setAddedMs(now(c)).build(), e.getCreated());
                try {
                    FlockHelpers.flockCheckUniquePerson(e.build(), owner(c));
                } catch (ProtoException ex) {
                    count("refused");
                    return; // refused: a local conflict
                }
                c.people.put(e.getKey(), e.build());
                return;
            }
            List<String> keys = new ArrayList<>(c.people.keySet());
            Person cur = c.people.get(keys.get(r.nextInt(keys.size())));
            if (marked(cur)) {
                return; // refused: marked
            }
            Person.Builder e = cur.toBuilder();
            randomWrite(r, e, c.clock);
            Person written = e.build();
            try {
                FlockHelpers.flockCheckUniquePerson(written, owner(c));
            } catch (ProtoException ex) {
                count("refused");
                return;
            }
            if (marked(written)) {
                count("mark");
            }
            c.people.put(written.getKey(), written);
        }

        /** owner is copy c's unique index. */
        Function<String, Optional<String>> owner(Replica c) {
            Map<String, String> idx = new HashMap<>();
            for (Map.Entry<String, Person> en : c.people.entrySet()) {
                for (String u : FlockHelpers.flockUniqueKeysPerson(en.getValue())) {
                    idx.putIfAbsent(u, en.getKey());
                }
            }
            return u -> Optional.ofNullable(idx.get(u));
        }

        void send(int i, int j) {
            Replica from = copies.get(i);
            if (from.offline || i == j) {
                return;
            }
            List<Person> ps = new ArrayList<>(from.people.values());
            net.add(new Message(i, j, from.clock.now(), ps));
        }

        void deliver() {
            if (net.isEmpty()) {
                return;
            }
            int n = r.nextInt(net.size());
            Message m = net.remove(n);
            Replica to = copies.get(m.to);
            if (to.offline || cut.contains(edge(m.from, m.to))) {
                return; // lost
            }
            if (FlockHelpers.flockMessageTooOld(m.sent, now(to))) {
                count("too-old");
                return;
            }
            // A message from a clock far ahead is held until local time catches up.
            if (!to.clock.observe(m.sent)) {
                count("held");
                net.add(m);
                return;
            }
            to.lastContact = wall;
            for (Person in : m.people) {
                if (!to.clock.observe(in.getWritten())) {
                    net.add(m);
                    return;
                }
            }
            for (Person in : m.people) {
                Person cur = to.people.get(in.getKey());
                if (cur == null) {
                    if (FlockHelpers.flockPurgeReadyPerson(in, members, now(to))) {
                        continue; // already purged here, or about to be
                    }
                    to.people.put(in.getKey(), in);
                    to.source.put(in.getKey(), "inserted from " + copies.get(m.from).id + " sent " + m.sent + " at wall " + wall);
                    continue;
                }
                to.people.put(in.getKey(), FlockHelpers.flockMergePerson(cur, in));
            }
            resolve(to);
        }

        /**
         * resolve runs the unique check over gossip on copy c: the partition race
         * merges, any other overlap stays (quarantined); every MERGED entry is
         * re-absorbed into its survivor.
         */
        void resolve(Replica c) {
            for (boolean changed = true; changed; ) {
                changed = false;
                Map<String, String> idx = new HashMap<>();
                for (String k : new ArrayList<>(c.people.keySet())) {
                    Person e = c.people.get(k);
                    for (String u : FlockHelpers.flockUniqueKeysPerson(e)) {
                        String o = idx.get(u);
                        if (o == null) {
                            idx.put(u, k);
                            continue;
                        }
                        if (FlockHelpers.flockOverlapPerson(c.people.get(o), e, u, RACE_WINDOW_MS) != FlockOverlap.AUTO_MERGE) {
                            count("quarantine");
                            continue; // quarantined
                        }
                        count("auto-merge");
                        FlockEntries<Person> res = FlockHelpers.flockMergeEntriesPerson(c.people.get(o), e, c.clock.now());
                        c.people.put(res.survivor().getKey(), res.survivor());
                        c.people.put(res.loser().getKey(), res.loser());
                        changed = true;
                        break;
                    }
                    if (changed) {
                        break;
                    }
                }
            }
            for (String k : new ArrayList<>(c.people.keySet())) {
                Person l = c.people.get(k);
                Optional<String> target = FlockHelpers.flockRedirectPerson(l);
                if (target.isEmpty()) {
                    continue;
                }
                Person sv = c.people.get(target.get());
                if (sv != null) {
                    count("redirect");
                    c.people.put(target.get(), FlockHelpers.flockMergeEntriesPerson(sv, l, c.clock.now()).survivor());
                }
            }
        }

        void partition() {
            switch (r.nextInt(4)) {
                case 0:
                    cut = new HashSet<>();
                    break;
                case 1: {
                    int i = r.nextInt(SIM_COPIES);
                    int j = r.nextInt(SIM_COPIES);
                    cut.add(edge(i, j));
                    cut.add(edge(j, i));
                    break;
                }
                case 2:
                    // Split the copies in two halves.
                    cut = new HashSet<>();
                    for (int i = 0; i < SIM_COPIES; i++) {
                        for (int j = 0; j < SIM_COPIES; j++) {
                            if ((i < SIM_COPIES / 2) != (j < SIM_COPIES / 2)) {
                                cut.add(edge(i, j));
                            }
                        }
                    }
                    break;
                default: {
                    Replica c = copies.get(r.nextInt(SIM_COPIES));
                    c.offline = !c.offline;
                    if (c.offline && !longAbsence && r.nextInt(3) == 0) {
                        // This copy stays away longer than the rebuild limit.
                        longAbsence = true;
                        wall += FlockHelpers.FLOCK_REBUILD_AFTER_MS + 24L * 60 * 60 * 1000;
                    }
                    if (!c.offline && FlockHelpers.flockMustRebuild(c.lastContact, wall)) {
                        count("rebuild");
                        // Wiped: a new copy, which serves nothing until it has caught up.
                        c.people = new TreeMap<>();
                        c.seenMarked = new HashSet<>();
                    }
                    break;
                }
            }
        }

        /** housekeep: a copy acks the marks it holds and purges the ready ones. */
        void housekeep(Replica c) {
            if (c.offline) {
                return;
            }
            for (String k : new ArrayList<>(c.people.keySet())) {
                Person e = c.people.get(k);
                if (!marked(e)) {
                    continue;
                }
                Person.Builder b = e.toBuilder();
                FlockHelpers.flockAckPerson(b, c.id, c.clock.now());
                Person acked = b.build();
                c.people.put(k, acked);
                if (FlockHelpers.flockPurgeReadyPerson(acked, members, now(c))) {
                    count("purge");
                    c.people.remove(k);
                }
            }
        }

        void checkInvariants() {
            for (Replica c : copies) {
                for (Map.Entry<String, Person> en : c.people.entrySet()) {
                    String k = en.getKey();
                    Person e = en.getValue();
                    boolean isMarked = marked(e);
                    if (!isMarked && c.seenMarked.contains(k)) {
                        fail(c.id + ": " + k + " came back to life (" + c.source.get(k) + ")");
                    }
                    if (isMarked) {
                        c.seenMarked.add(k);
                    }
                    if (e.getDeleted().getReason() == DeleteReason.DELETE_REASON_MERGED) {
                        if (FlockHelpers.flockHlcCompare(e.getDeleted().getMergedIntoCreated(), e.getCreated()) >= 0) {
                            fail(c.id + ": " + k + " redirects to " + e.getDeleted().getMergedInto() + ", which is not older");
                        }
                    }
                }
                // Redirect chains end.
                for (String k : c.people.keySet()) {
                    Set<String> seen = new HashSet<>();
                    String cur = k;
                    while (cur != null) {
                        if (!seen.add(cur)) {
                            fail(c.id + ": redirect cycle through " + cur);
                        }
                        Person e = c.people.get(cur);
                        if (e == null) {
                            break;
                        }
                        cur = FlockHelpers.flockRedirectPerson(e).orElse(null);
                    }
                }
                // A remote time never moved this clock past local time plus the bound.
                if (now(c) > c.maxLocal) {
                    c.maxLocal = now(c);
                }
                long got = c.clock.now().getWallMs();
                if (got > c.maxLocal + FlockHelpers.FLOCK_MAX_SKEW_MS) {
                    fail(c.id + ": clock at " + got + ", beyond local time " + c.maxLocal + " plus the skew bound");
                }
            }
        }

        void checkConverged(long seed) {
            Replica ref = copies.get(0);
            for (Replica c : copies.subList(1, copies.size())) {
                List<String> a = new ArrayList<>(ref.people.keySet());
                List<String> b = new ArrayList<>(c.people.keySet());
                if (!a.equals(b)) {
                    fail("seed " + seed + ": " + ref.id + " holds " + a + ", " + c.id + " holds " + b);
                }
                for (String k : a) {
                    if (!Arrays.equals(FlockHelpers.flockDigestPerson(ref.people.get(k)), FlockHelpers.flockDigestPerson(c.people.get(k)))) {
                        fail("seed " + seed + ": " + k + " differs:\n  " + ref.id + ": " + ref.people.get(k) + "\n  " + c.id + ": " + c.people.get(k));
                    }
                }
            }
            if (System.getenv("FLOCK_SIM_VERBOSE") != null) {
                int live = 0;
                int dead = 0;
                for (Person e : ref.people.values()) {
                    if (marked(e)) {
                        dead++;
                    } else {
                        live++;
                    }
                }
                System.out.println("seed " + seed + ": " + live + " live, " + dead + " marked");
            }
        }
    }

    static void simulation() {
        for (long seed = 0; seed < SIMULATIONS; seed++) {
            runSim(seed);
        }
        for (String what : new String[] {"auto-merge", "quarantine", "redirect", "mark", "purge", "held", "too-old", "refused", "rebuild"}) {
            if (SIM_STATS.getOrDefault(what, 0) == 0) {
                fail("the simulations never exercised " + what + ": " + SIM_STATS);
            }
        }
        System.out.println("flock java simulation: " + SIM_STATS);
    }

    static void runSim(long seed) {
        Sim s = new Sim(seed);
        for (int i = 0; i < SIM_COPIES; i++) {
            Replica c = new Replica("c" + i);
            c.clock = new FlockClock(c.id, () -> s.now(c));
            s.copies.add(c);
            s.members.add(c.id);
        }
        // One copy runs a clock far ahead for a while: its writes are held.
        s.copies.get(SIM_COPIES - 1).skew = FlockHelpers.FLOCK_MAX_SKEW_MS * 2;
        for (int step = 0; step < SIM_STEPS; step++) {
            s.wall += 1 + s.r.nextInt(400);
            if (step == SIM_STEPS / 3) {
                s.copies.get(SIM_COPIES - 1).skew = 0;
            }
            int k = s.r.nextInt(20);
            if (k < 7) {
                s.localWrite(s.copies.get(s.r.nextInt(SIM_COPIES)));
            } else if (k < 11) {
                s.send(s.r.nextInt(SIM_COPIES), s.r.nextInt(SIM_COPIES));
            } else if (k < 16) {
                s.deliver();
            } else if (k < 17) {
                s.partition();
            } else if (k < 18 && s.r.nextInt(8) == 0) {
                // A quiet spell: what is still in flight is now too old to apply.
                s.wall += FlockHelpers.FLOCK_MAX_MESSAGE_AGE_MS + 1;
            } else {
                s.housekeep(s.copies.get(s.r.nextInt(SIM_COPIES)));
            }
            s.checkInvariants();
        }
        // Heal: every copy online, no partition, rounds of full gossip until quiet.
        s.cut = new HashSet<>();
        for (Replica c : s.copies) {
            c.skew = 0;
            if (c.offline) {
                c.offline = false;
                if (FlockHelpers.flockMustRebuild(c.lastContact, s.wall)) {
                    c.people = new TreeMap<>();
                    c.seenMarked = new HashSet<>();
                }
            }
        }
        for (int round = 0; round < 40; round++) {
            s.wall += FlockHelpers.FLOCK_MAX_MESSAGE_AGE_MS / 4;
            for (int i = 0; i < s.copies.size(); i++) {
                for (int j = 0; j < s.copies.size(); j++) {
                    if (i != j) {
                        s.send(i, j);
                    }
                }
            }
            while (!s.net.isEmpty()) {
                s.deliver();
            }
            for (Replica c : s.copies) {
                s.housekeep(c);
            }
            s.checkInvariants();
        }
        s.checkConverged(seed);
    }

    // ---------- the cross-language vector ----------

    /**
     * vectorScenario is the same fixed sequence of writes, merges and marks in
     * every leg; its digests must equal cross-test/fixtures/flock_vectors.txt
     * byte for byte, which pins the canonical encoding and every merge rule
     * across languages.
     */
    static List<String> vectorScenario() {
        long now = 1_700_000_000_000L;
        FlockClock a = new FlockClock("a", () -> now);
        FlockClock b = new FlockClock("b", () -> now);
        Person.Builder p = FlockHelpers.flockNewPerson("person-1", a.now()).toBuilder();
        FlockHelpers.flockAddPersonLinks(p, Link.newBuilder().setIdp("google").setSubject("s1").setAddedMs(10).setAssurance(2).build(), p.getCreated());
        FlockHelpers.flockSetPersonDisplay(p, "Ann", a.now());
        Person.Builder q = p.build().toBuilder();
        FlockHelpers.flockSetPersonDisplay(q, "Annie", b.now());
        FlockHelpers.flockAddPersonTags(q, "x", b.now());
        FlockHelpers.flockPutPersonDevices(q, "phone", Device.newBuilder().setName("Pixel").setFirstSeenMs(5).setLastSeenMs(9).build(), b.now());
        FlockHelpers.flockRemovePersonLinks(p, Link.newBuilder().setIdp("google").setSubject("s1").build(), a.now());
        FlockHelpers.flockAddPersonLinks(q, Link.newBuilder().setIdp("apple").setSubject("s2").setAddedMs(7).setAssurance(1).build(), b.now());
        FlockHelpers.flockAddPersonMemberships(q, Membership.newBuilder().setTenant("alpha").setRole("admin").build(), b.now());
        FlockHelpers.flockSetPersonGeneration(p, 3, a.now());
        FlockHelpers.flockSetPersonGeneration(q, 5, b.now());
        FlockHelpers.flockSetPersonFirstSeenMs(p, 40, a.now());
        FlockHelpers.flockSetPersonFirstSeenMs(q, 30, b.now());
        FlockHelpers.flockSetPersonHomeRegion(p, "eu", a.now());
        FlockHelpers.flockSetPersonHomeRegion(q, "us", b.now());
        FlockHelpers.flockSetPersonProfileNickname(q, "nick", b.now());
        FlockHelpers.flockSetPersonProfileLastSignInDay(p, 12, a.now());
        FlockHelpers.flockAddPersonProfileEmails(p, "a@x", a.now());
        FlockHelpers.flockSetPersonAliases(p, Arrays.asList("x", "y"), a.now());
        Person m = merge(p.build(), q.build());
        Person.Builder o = FlockHelpers.flockNewPerson("person-0", Hlc.newBuilder().setWallMs(now - 1).setNode("b").build()).toBuilder();
        FlockHelpers.flockAddPersonTags(o, "y", b.now());
        FlockHelpers.flockPutPersonDevices(o, "laptop", Device.newBuilder().setName("Book").setFirstSeenMs(3).setLastSeenMs(4).build(), b.now());
        FlockEntries<Person> res = FlockHelpers.flockMergeEntriesPerson(m, o.build(), a.now());
        Person s = res.survivor();
        Person.Builder l = res.loser().toBuilder();
        FlockHelpers.flockAckPerson(l, "a", a.now());
        Person.Builder gone = s.toBuilder();
        FlockHelpers.flockMarkPerson(gone, DeleteReason.DELETE_REASON_USER, b.now());

        Session sess = FlockHelpers.flockNewSession("session-1", a.now());
        Session.Builder s1 = sess.toBuilder().setDeviceId("phone").setExpiresMs(100);
        FlockHelpers.flockWriteSession(s1, a.now());
        Session.Builder s2 = sess.toBuilder().setDeviceId("laptop").setExpiresMs(200);
        FlockHelpers.flockWriteSession(s2, b.now());
        Session sm = FlockHelpers.flockMergeSession(s1.build(), s2.build());
        HexFormat hex = HexFormat.of();
        return List.of(
                "merged " + hex.formatHex(FlockHelpers.flockDigestPerson(m)),
                "survivor " + hex.formatHex(FlockHelpers.flockDigestPerson(s)),
                "loser " + hex.formatHex(FlockHelpers.flockDigestPerson(l.build())),
                "marked " + hex.formatHex(FlockHelpers.flockDigestPerson(gone.build())),
                "session " + hex.formatHex(FlockHelpers.flockDigestSession(sm)));
    }

    static void vector() throws IOException {
        List<String> got = vectorScenario();
        for (String line : got) {
            System.out.println("flock vector: " + line);
        }
        String path = System.getenv("FLOCK_VECTOR_FIXTURE");
        if (path == null || path.isEmpty()) {
            return;
        }
        List<String> want = new ArrayList<>();
        for (String line : Files.readString(Path.of(path), StandardCharsets.UTF_8).strip().split("\n")) {
            if (!line.isEmpty() && line.charAt(0) != '#') {
                want.add(line);
            }
        }
        if (!want.equals(got)) {
            fail("vector differs from " + path + ":\n got " + got + "\nwant " + want);
        }
    }

    public static void main(String[] args) {
        try {
            properties();
            System.out.println("flock java: " + PROPERTY_CASES + " property cases, " + SIMULATIONS + " simulations");
            simulation();
            vector();
        } catch (AssertionError | RuntimeException | IOException e) {
            System.err.println("FAIL flock java: " + e.getMessage());
            e.printStackTrace();
            System.exit(1);
        }
    }
}
