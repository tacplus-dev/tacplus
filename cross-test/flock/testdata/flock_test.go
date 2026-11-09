// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The Go leg of the flock gate (tacplus#206), compiled into the generated
// package of cross-test/flock/proto. It checks the generated merge of the
// people table two ways, and the Rust and Java legs check theirs the same way:
//
//   - properties: over random copies of one entry (random writes on random
//     copies, with marks and acks), FlockMerge is commutative, associative and
//     idempotent over whole entries plus marks, and FlockMergeEntries picks the
//     same survivor and loser mark whatever the order;
//   - simulation: copies that write, gossip whole snapshots through a network
//     that delivers in random order, partitions, holds far-future writes, merges
//     partition duplicates, acks and purges marks, then heal and converge. It
//     checks convergence (equal digests everywhere), no resurrection (a copy
//     never again holds live an entry it saw marked) and no redirect cycles
//     (every MERGED mark points to a strictly older entry).
package flockcheck

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"testing"

	"google.golang.org/protobuf/proto"

	flock "gitlab.com/example/tacplus/flock/flockcheck/model/types/sys/flock"
)

const (
	propertyCases = 300
	simulations   = 40
	simSteps      = 400
	simCopies     = 4
	raceWindowMs  = 5_000
)

var (
	idps    = []string{"google", "microsoft", "apple"}
	tenants = []string{"alpha", "beta", "gamma"}
	names   = []string{"ann", "bob", "cy", "di"}
)

// ---------- properties ----------

// writer applies one random write to e on a copy whose clock is c.
func randomWrite(r *rand.Rand, e *Person, c *FlockClock) {
	at := c.Now()
	switch r.IntN(16) {
	case 0:
		FlockSetPersonDisplay(e, names[r.IntN(len(names))], at)
	case 1:
		FlockSetPersonGeneration(e, uint64(r.IntN(5)), at)
	case 2:
		FlockSetPersonFirstSeenMs(e, int64(1+r.IntN(1000)), at)
	case 3:
		_ = FlockSetPersonHomeRegion(e, []string{"eu", "us"}[r.IntN(2)], at)
	case 4:
		FlockSetPersonProfileNickname(e, names[r.IntN(len(names))], at)
	case 5:
		FlockSetPersonProfileLastSignInDay(e, uint64(r.IntN(30)), at)
	case 6:
		FlockAddPersonProfileEmails(e, names[r.IntN(len(names))]+"@x", at)
	case 7:
		FlockRemovePersonProfileEmails(e, names[r.IntN(len(names))]+"@x", at)
	case 8:
		FlockAddPersonLinks(e, randomLink(r), at)
	case 9:
		FlockRemovePersonLinks(e, randomLink(r), at)
	case 10:
		FlockAddPersonMemberships(e, &Membership{Tenant: tenants[r.IntN(len(tenants))], Role: names[r.IntN(len(names))]}, at)
	case 11:
		FlockRemovePersonMemberships(e, &Membership{Tenant: tenants[r.IntN(len(tenants))]}, at)
	case 12:
		FlockPutPersonDevices(e, names[r.IntN(len(names))], &Device{Name: names[r.IntN(len(names))], FirstSeenMs: int64(1 + r.IntN(100)), LastSeenMs: int64(1 + r.IntN(100))}, at)
	case 13:
		FlockRemovePersonDevices(e, names[r.IntN(len(names))], at)
	case 14:
		if r.IntN(2) == 0 {
			FlockAddPersonTags(e, names[r.IntN(len(names))], at)
		} else {
			FlockRemovePersonTags(e, names[r.IntN(len(names))], at)
		}
	case 15:
		switch r.IntN(4) {
		case 0:
			_ = FlockMarkPerson(e, flock.DeleteReason_DELETE_REASON_USER, at)
		case 1:
			_ = FlockMarkPerson(e, flock.DeleteReason_DELETE_REASON_EXPIRED, at)
		case 2:
			FlockAckPerson(e, fmt.Sprintf("c%d", r.IntN(3)), at)
		default:
			FlockSetPersonAliases(e, []string{names[r.IntN(len(names))], names[r.IntN(len(names))]}, at)
		}
	}
}

func randomLink(r *rand.Rand) *Link {
	return &Link{Idp: idps[r.IntN(len(idps))], Subject: names[r.IntN(len(names))], AddedMs: int64(1 + r.IntN(100)), Assurance: uint32(r.IntN(3))}
}

// randomCopies returns three copies of one entry that diverged from a common
// ancestor by random writes on three clocks, each also having merged in some
// of the others' states, so they share history.
func randomCopies(r *rand.Rand, wall *int64) [3]*Person {
	now := func() int64 { return *wall }
	clocks := [3]*FlockClock{NewFlockClock("c0", now), NewFlockClock("c1", now), NewFlockClock("c2", now)}
	base := FlockNewPerson("k1", clocks[0].Now())
	var out [3]*Person
	for i := range out {
		out[i] = proto.Clone(base).(*Person)
	}
	for step := 0; step < 30; step++ {
		*wall += int64(r.IntN(3))
		i := r.IntN(3)
		if r.IntN(6) == 0 {
			j := r.IntN(3)
			m, err := FlockMergePerson(out[i], out[j])
			if err != nil {
				panic(err)
			}
			out[i] = m
			continue
		}
		randomWrite(r, out[i], clocks[i])
	}
	return out
}

func mustMerge(t *testing.T, a, b *Person) *Person {
	t.Helper()
	m, err := FlockMergePerson(a, b)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func samePerson(t *testing.T, what string, a, b *Person) {
	t.Helper()
	if !bytes.Equal(FlockDigestPerson(a), FlockDigestPerson(b)) || !proto.Equal(a, b) {
		t.Fatalf("%s:\n  %v\n  %v", what, a, b)
	}
}

func TestFlockProperties(t *testing.T) {
	wall := int64(1_000)
	r := rand.New(rand.NewPCG(1, 206))
	for n := 0; n < propertyCases; n++ {
		c := randomCopies(r, &wall)
		a, b, d := c[0], c[1], c[2]
		samePerson(t, "commutative", mustMerge(t, a, b), mustMerge(t, b, a))
		samePerson(t, "associative", mustMerge(t, mustMerge(t, a, b), d), mustMerge(t, a, mustMerge(t, b, d)))
		ab := mustMerge(t, a, b)
		samePerson(t, "idempotent", mustMerge(t, ab, ab), ab)
		samePerson(t, "absorbing", mustMerge(t, ab, a), ab)

		// Two different entries: the same survivor and loser, either order.
		other := proto.Clone(d).(*Person)
		other.Key = "k0"
		other.Created = &flock.Hlc{WallMs: d.GetCreated().GetWallMs() + int64(r.IntN(3)) - 1, Node: "c9"}
		at1, at2 := &flock.Hlc{WallMs: wall, Node: "c0"}, &flock.Hlc{WallMs: wall + 1, Node: "c1"}
		s1, l1, err := FlockMergeEntriesPerson(ab, other, at1)
		if err != nil {
			t.Fatal(err)
		}
		s2, l2, err := FlockMergeEntriesPerson(other, ab, at2)
		if err != nil {
			t.Fatal(err)
		}
		if s1.GetKey() != s2.GetKey() || l1.GetKey() != l2.GetKey() {
			t.Fatalf("merge_entries picked %s then %s", s1.GetKey(), s2.GetKey())
		}
		// The two merges' marks differ only in time; merging the results converges.
		samePerson(t, "merge_entries survivor", s1, mustMerge(t, s1, s2))
		samePerson(t, "merge_entries survivor (other order)", mustMerge(t, s1, s2), mustMerge(t, s2, s1))
		samePerson(t, "merge_entries loser", mustMerge(t, l1, l2), mustMerge(t, l2, l1))
		if l := mustMerge(t, l1, l2); l.GetDeleted().GetReason() != flock.DeleteReason_DELETE_REASON_MERGED && l.GetDeleted().GetReason() != flock.DeleteReason_DELETE_REASON_USER {
			t.Fatalf("loser not marked: %v", l.GetDeleted())
		}
		if FlockHlcCompare(s1.GetCreated(), l1.GetCreated()) > 0 {
			t.Fatalf("survivor %v is younger than loser %v", s1.GetCreated(), l1.GetCreated())
		}
	}
	checkScenarios(t)
	fmt.Printf("flock go: %d property cases, %d simulations\n", propertyCases, simulations)
}

// checkScenarios pins the cases the panel named.
func checkScenarios(t *testing.T) {
	now := int64(5_000)
	clock := func(node string) *FlockClock { return NewFlockClock(node, func() int64 { return now }) }
	ca, cb := clock("a"), clock("b")
	base := FlockNewPerson("p", ca.Now())
	link := &Link{Idp: "google", Subject: "s"}

	// flock creates keys: 128 random bits, hex, never repeated.
	k1, err1 := FlockNewKey()
	k2, err2 := FlockNewKey()
	if err1 != nil || err2 != nil || len(k1) != 32 || k1 == k2 {
		t.Fatalf("FlockNewKey: %q %q %v %v", k1, k2, err1, err2)
	}

	// A re-add after a remove it saw survives remove-wins.
	x := proto.Clone(base).(*Person)
	FlockAddPersonLinks(x, link, ca.Now())
	FlockRemovePersonLinks(x, link, ca.Now())
	FlockAddPersonLinks(x, link, ca.Now())
	if len(x.GetLinks()) != 1 {
		t.Fatalf("re-add lost: %v", x.GetLinks())
	}
	// A concurrent add and remove: remove wins on links, add wins on tags.
	y, z := proto.Clone(x).(*Person), proto.Clone(x).(*Person)
	FlockRemovePersonLinks(y, link, ca.Now())
	FlockAddPersonLinks(z, link, cb.Now())
	FlockAddPersonTags(z, "t", cb.Now())
	FlockAddPersonTags(y, "t", ca.Now())
	FlockRemovePersonTags(y, "t", ca.Now())
	m := mustMerge(t, y, z)
	if len(m.GetLinks()) != 0 {
		t.Fatalf("remove-wins: concurrent add won: %v", m.GetLinks())
	}
	if len(m.GetTags()) != 1 {
		t.Fatalf("add-wins: concurrent remove won: %v", m.GetTags())
	}
	// A removed device stays removed when an older copy comes back.
	d := proto.Clone(base).(*Person)
	FlockPutPersonDevices(d, "phone", &Device{Name: "phone"}, ca.Now())
	old := proto.Clone(d).(*Person)
	FlockRemovePersonDevices(d, "phone", ca.Now())
	if got := mustMerge(t, old, d).GetDevices(); len(got) != 0 {
		t.Fatalf("revoked device came back: %v", got)
	}
	// A mark wins over a later write, and a USER mark over a MERGED one.
	w := proto.Clone(base).(*Person)
	_ = FlockMarkPerson(w, flock.DeleteReason_DELETE_REASON_EXPIRED, ca.Now())
	v := proto.Clone(base).(*Person)
	FlockSetPersonDisplay(v, "later", cb.Now())
	if mustMerge(t, w, v).GetDeleted().GetReason() != flock.DeleteReason_DELETE_REASON_EXPIRED {
		t.Fatal("a write undid a mark")
	}
	if FlockLivePerson(w) == nil {
		t.Fatal("a marked entry is live")
	}
	// A conflicting IMMUTABLE write is recorded, not resolved silently.
	i1, i2 := proto.Clone(base).(*Person), proto.Clone(base).(*Person)
	_ = FlockSetPersonHomeRegion(i1, "eu", ca.Now())
	_ = FlockSetPersonHomeRegion(i2, "us", cb.Now())
	if got := mustMerge(t, i1, i2).GetFlockMeta().GetConflicts(); len(got) != 1 || got[0] != "home_region" {
		t.Fatalf("immutable conflict not recorded: %v", got)
	}
	if FlockSetPersonHomeRegion(i1, "us", ca.Now()) == nil {
		t.Fatal("an immutable field was overwritten")
	}
	// A remote time far ahead never moves the clock.
	c := clock("h")
	if c.Observe(&flock.Hlc{WallMs: now + FlockMaxSkewMs + 1, Node: "far"}) {
		t.Fatal("a far-future time was accepted")
	}
	if got := c.Now().GetWallMs(); got != now {
		t.Fatalf("clock moved to %d", got)
	}
	// The unique check refuses a local conflict; only the partition race merges.
	p1 := FlockNewPerson("p1", ca.Now())
	FlockAddPersonLinks(p1, link, p1.GetCreated())
	p2 := FlockNewPerson("p2", cb.Now())
	FlockAddPersonLinks(p2, link, p2.GetCreated())
	owner := func(k string) (string, bool) { return "p1", true }
	if FlockCheckUniquePerson(p2, owner) == nil {
		t.Fatal("a local unique conflict was accepted")
	}
	k := FlockUniqueKeysPerson(p1)[0]
	if FlockOverlapPerson(p1, p2, k, raceWindowMs) != FlockOverlapAutoMerge {
		t.Fatal("the partition race was not merged")
	}
	p3 := FlockNewPerson("p3", cb.Now())
	FlockAddPersonLinks(p3, link, cb.Now())
	if FlockOverlapPerson(p1, p3, k, raceWindowMs) != FlockOverlapQuarantine {
		t.Fatal("a link added later was merged, not quarantined")
	}
	// Session: the newer whole entry wins.
	s := FlockNewSession("s", ca.Now())
	s1, s2 := proto.Clone(s).(*Session), proto.Clone(s).(*Session)
	s1.DeviceId = "old"
	FlockWriteSession(s1, ca.Now())
	s2.DeviceId = "new"
	cb.Observe(s1.GetWritten())
	FlockWriteSession(s2, cb.Now())
	if got, _ := FlockMergeSession(s1, s2); got.GetDeviceId() != "new" {
		t.Fatalf("whole-entry LAST_WRITE kept %q", got.GetDeviceId())
	}
}

// ---------- simulation ----------

type message struct {
	from, to int
	sent     *flock.Hlc
	people   []*Person
}

type replica struct {
	id          string
	clock       *FlockClock
	skew        int64
	people      map[string]*Person
	seenMarked  map[string]bool
	lastContact int64
	offline     bool
	// source says where each entry last came from, for a failure message.
	source map[string]string
	// maxLocal is the latest local time this copy has had.
	maxLocal int64
}

type sim struct {
	t       *testing.T
	r       *rand.Rand
	wall    int64
	copies  []*replica
	net     []message
	cut     map[[2]int]bool
	members []string
	nextKey int
	// longAbsence: a copy has already been away past the rebuild limit.
	longAbsence bool
}

func (s *sim) now(c *replica) int64 { return s.wall + c.skew }

// simStats counts what the simulations exercised; each must happen.
var simStats = map[string]int{}

func TestFlockSimulation(t *testing.T) {
	for seed := uint64(0); seed < simulations; seed++ {
		runSim(t, seed)
	}
	for _, what := range []string{"auto-merge", "quarantine", "redirect", "mark", "purge", "held", "too-old", "refused", "rebuild"} {
		if simStats[what] == 0 {
			t.Fatalf("the simulations never exercised %s: %v", what, simStats)
		}
	}
	fmt.Printf("flock go simulation: %v\n", simStats)
}

func runSim(t *testing.T, seed uint64) {
	s := &sim{t: t, r: rand.New(rand.NewPCG(seed, 2026)), wall: 1_000_000, cut: map[[2]int]bool{}}
	for i := 0; i < simCopies; i++ {
		c := &replica{id: fmt.Sprintf("c%d", i), people: map[string]*Person{}, seenMarked: map[string]bool{}, source: map[string]string{}}
		cc := c
		c.clock = NewFlockClock(c.id, func() int64 { return s.now(cc) })
		s.copies = append(s.copies, c)
		s.members = append(s.members, c.id)
	}
	// One copy runs a clock far ahead for a while: its writes are held.
	s.copies[simCopies-1].skew = FlockMaxSkewMs * 2
	for step := 0; step < simSteps; step++ {
		s.wall += int64(1 + s.r.IntN(400))
		if step == simSteps/3 {
			s.copies[simCopies-1].skew = 0
		}
		switch k := s.r.IntN(20); {
		case k < 7:
			s.localWrite(s.copies[s.r.IntN(simCopies)])
		case k < 11:
			s.send(s.r.IntN(simCopies), s.r.IntN(simCopies))
		case k < 16:
			s.deliver()
		case k < 17:
			s.partition()
		case k < 18 && s.r.IntN(8) == 0:
			// A quiet spell: what is still in flight is now too old to apply.
			s.wall += FlockMaxMessageAgeMs + 1
		default:
			s.housekeep(s.copies[s.r.IntN(simCopies)])
		}
		s.checkInvariants()
	}
	// Heal: every copy online, no partition, rounds of full gossip until quiet.
	s.cut = map[[2]int]bool{}
	for _, c := range s.copies {
		c.skew = 0
		if c.offline {
			c.offline = false
			if FlockMustRebuild(c.lastContact, s.wall) {
				c.people, c.seenMarked = map[string]*Person{}, map[string]bool{}
			}
		}
	}
	for round := 0; round < 40; round++ {
		s.wall += FlockMaxMessageAgeMs / 4
		for i := range s.copies {
			for j := range s.copies {
				if i != j {
					s.send(i, j)
				}
			}
		}
		for len(s.net) > 0 {
			s.deliver()
		}
		for _, c := range s.copies {
			s.housekeep(c)
		}
		s.checkInvariants()
	}
	s.checkConverged(seed)
}

func (s *sim) localWrite(c *replica) {
	if c.offline {
		return
	}
	// Create a person, born with a link from a small pool: duplicates across a
	// partition are the race the unique check merges.
	if len(c.people) == 0 || s.r.IntN(5) == 0 {
		s.nextKey++
		e := FlockNewPerson(fmt.Sprintf("%s-%04d", c.id, s.nextKey), c.clock.Now())
		FlockAddPersonLinks(e, &Link{Idp: idps[s.r.IntN(2)], Subject: names[s.r.IntN(2)], AddedMs: s.now(c)}, e.GetCreated())
		if FlockCheckUniquePerson(e, s.owner(c)) != nil {
			simStats["refused"]++
			return // refused: a local conflict
		}
		c.people[e.GetKey()] = e
		return
	}
	keys := sortedKeys(c.people)
	e := proto.Clone(c.people[keys[s.r.IntN(len(keys))]]).(*Person)
	if FlockLivePerson(e) != nil {
		return // refused: marked
	}
	randomWrite(s.r, e, c.clock)
	if FlockCheckUniquePerson(e, s.owner(c)) != nil {
		simStats["refused"]++
		return
	}
	if FlockLivePerson(e) != nil {
		simStats["mark"]++
	}
	c.people[e.GetKey()] = e
}

// owner is copy c's unique index.
func (s *sim) owner(c *replica) func(string) (string, bool) {
	idx := map[string]string{}
	for _, k := range sortedKeys(c.people) {
		for _, u := range FlockUniqueKeysPerson(c.people[k]) {
			if _, taken := idx[u]; !taken {
				idx[u] = k
			}
		}
	}
	return func(u string) (string, bool) { o, ok := idx[u]; return o, ok }
}

func (s *sim) send(i, j int) {
	from := s.copies[i]
	if from.offline || i == j {
		return
	}
	var ps []*Person
	for _, k := range sortedKeys(from.people) {
		ps = append(ps, proto.Clone(from.people[k]).(*Person))
	}
	s.net = append(s.net, message{from: i, to: j, sent: from.clock.Now(), people: ps})
}

func (s *sim) deliver() {
	if len(s.net) == 0 {
		return
	}
	n := s.r.IntN(len(s.net))
	m := s.net[n]
	s.net = append(s.net[:n], s.net[n+1:]...)
	to := s.copies[m.to]
	if to.offline || s.cut[[2]int{m.from, m.to}] {
		return // lost
	}
	if FlockMessageTooOld(m.sent, s.now(to)) {
		simStats["too-old"]++
		return
	}
	// A message from a clock far ahead is held until local time catches up.
	if !to.clock.Observe(m.sent) {
		simStats["held"]++
		s.net = append(s.net, m)
		return
	}
	to.lastContact = s.wall
	for _, in := range m.people {
		if !to.clock.Observe(in.GetWritten()) {
			s.net = append(s.net, m)
			return
		}
	}
	for _, in := range m.people {
		cur, ok := to.people[in.GetKey()]
		if !ok {
			if FlockPurgeReadyPerson(in, s.members, s.now(to)) {
				continue // already purged here, or about to be
			}
			to.people[in.GetKey()] = proto.Clone(in).(*Person)
			to.source[in.GetKey()] = fmt.Sprintf("inserted from %s sent %v at wall %d", s.copies[m.from].id, m.sent, s.wall)
			continue
		}
		merged, err := FlockMergePerson(cur, in)
		if err != nil {
			s.t.Fatal(err)
		}
		to.people[in.GetKey()] = merged
	}
	s.resolve(to)
}

// resolve runs the unique check over gossip on copy c: the partition race
// merges, any other overlap stays (quarantined); every MERGED entry is
// re-absorbed into its survivor.
func (s *sim) resolve(c *replica) {
	for changed := true; changed; {
		changed = false
		idx := map[string]string{}
		for _, k := range sortedKeys(c.people) {
			e := c.people[k]
			for _, u := range FlockUniqueKeysPerson(e) {
				o, taken := idx[u]
				if !taken {
					idx[u] = k
					continue
				}
				if FlockOverlapPerson(c.people[o], e, u, raceWindowMs) != FlockOverlapAutoMerge {
					simStats["quarantine"]++
					continue // quarantined
				}
				simStats["auto-merge"]++
				sv, l, err := FlockMergeEntriesPerson(c.people[o], e, c.clock.Now())
				if err != nil {
					s.t.Fatal(err)
				}
				c.people[sv.GetKey()], c.people[l.GetKey()] = sv, l
				changed = true
				break
			}
			if changed {
				break
			}
		}
	}
	for _, k := range sortedKeys(c.people) {
		l := c.people[k]
		target, ok := FlockRedirectPerson(l)
		if !ok {
			continue
		}
		if sv, ok := c.people[target]; ok {
			simStats["redirect"]++
			nsv, _, err := FlockMergeEntriesPerson(sv, l, c.clock.Now())
			if err != nil {
				s.t.Fatal(err)
			}
			c.people[target] = nsv
		}
	}
}

func (s *sim) partition() {
	switch s.r.IntN(4) {
	case 0:
		s.cut = map[[2]int]bool{}
	case 1:
		i, j := s.r.IntN(simCopies), s.r.IntN(simCopies)
		s.cut[[2]int{i, j}], s.cut[[2]int{j, i}] = true, true
	case 2:
		// Split the copies in two halves.
		s.cut = map[[2]int]bool{}
		for i := 0; i < simCopies; i++ {
			for j := 0; j < simCopies; j++ {
				if (i < simCopies/2) != (j < simCopies/2) {
					s.cut[[2]int{i, j}] = true
				}
			}
		}
	default:
		c := s.copies[s.r.IntN(simCopies)]
		c.offline = !c.offline
		if c.offline && !s.longAbsence && s.r.IntN(3) == 0 {
			// This copy stays away longer than the rebuild limit.
			s.longAbsence = true
			s.wall += FlockRebuildAfterMs + 24*60*60*1000
		}
		if !c.offline && FlockMustRebuild(c.lastContact, s.wall) {
			simStats["rebuild"]++
			// Wiped: a new copy, which serves nothing until it has caught up.
			c.people, c.seenMarked = map[string]*Person{}, map[string]bool{}
		}
	}
}

// housekeep: a copy acks the marks it holds and purges the ready ones.
func (s *sim) housekeep(c *replica) {
	if c.offline {
		return
	}
	for _, k := range sortedKeys(c.people) {
		e := c.people[k]
		if FlockLivePerson(e) == nil {
			continue
		}
		FlockAckPerson(e, c.id, c.clock.Now())
		if FlockPurgeReadyPerson(e, s.members, s.now(c)) {
			simStats["purge"]++
			delete(c.people, k)
		}
	}
}

func (s *sim) checkInvariants() {
	for _, c := range s.copies {
		for _, k := range sortedKeys(c.people) {
			e := c.people[k]
			marked := FlockLivePerson(e) != nil
			if !marked && c.seenMarked[k] {
				s.t.Fatalf("%s: %s came back to life (%s)", c.id, k, c.source[k])
			}
			if marked {
				c.seenMarked[k] = true
			}
			if e.GetDeleted().GetReason() == flock.DeleteReason_DELETE_REASON_MERGED {
				if FlockHlcCompare(e.GetDeleted().GetMergedIntoCreated(), e.GetCreated()) >= 0 {
					s.t.Fatalf("%s: %s redirects to %s, which is not older", c.id, k, e.GetDeleted().GetMergedInto())
				}
			}
		}
		// Redirect chains end.
		for _, k := range sortedKeys(c.people) {
			seen := map[string]bool{}
			for cur, ok := k, true; ok; {
				if seen[cur] {
					s.t.Fatalf("%s: redirect cycle through %s", c.id, cur)
				}
				seen[cur] = true
				e, present := c.people[cur]
				if !present {
					break
				}
				cur, ok = FlockRedirectPerson(e)
			}
		}
		// A remote time never moved this clock past local time plus the bound.
		if s.now(c) > c.maxLocal {
			c.maxLocal = s.now(c)
		}
		if got := c.clock.Now().GetWallMs(); got > c.maxLocal+FlockMaxSkewMs {
			s.t.Fatalf("%s: clock at %d, beyond local time %d plus the skew bound", c.id, got, c.maxLocal)
		}
	}
}

func (s *sim) checkConverged(seed uint64) {
	ref := s.copies[0]
	for _, c := range s.copies[1:] {
		a, b := sortedKeys(ref.people), sortedKeys(c.people)
		if fmt.Sprint(a) != fmt.Sprint(b) {
			s.t.Fatalf("seed %d: %s holds %v, %s holds %v", seed, ref.id, a, c.id, b)
		}
		for _, k := range a {
			if !bytes.Equal(FlockDigestPerson(ref.people[k]), FlockDigestPerson(c.people[k])) {
				s.t.Fatalf("seed %d: %s differs:\n  %s: %v\n  %s: %v", seed, k, ref.id, ref.people[k], c.id, c.people[k])
			}
		}
	}
	if os.Getenv("FLOCK_SIM_VERBOSE") != "" {
		live, marked := 0, 0
		for _, e := range ref.people {
			if FlockLivePerson(e) == nil {
				live++
			} else {
				marked++
			}
		}
		fmt.Printf("seed %d: %d live, %d marked\n", seed, live, marked)
	}
}

func sortedKeys(m map[string]*Person) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------- the cross-language vector ----------

// vectorScenario is the same fixed sequence of writes, merges and marks in
// every leg; its digests must equal cross-test/fixtures/flock_vectors.txt byte
// for byte, which pins the canonical encoding and every merge rule across
// languages.
func vectorScenario(t *testing.T) []string {
	now := int64(1_700_000_000_000)
	a := NewFlockClock("a", func() int64 { return now })
	b := NewFlockClock("b", func() int64 { return now })
	p := FlockNewPerson("person-1", a.Now())
	FlockAddPersonLinks(p, &Link{Idp: "google", Subject: "s1", AddedMs: 10, Assurance: 2}, p.GetCreated())
	FlockSetPersonDisplay(p, "Ann", a.Now())
	q := proto.Clone(p).(*Person)
	FlockSetPersonDisplay(q, "Annie", b.Now())
	FlockAddPersonTags(q, "x", b.Now())
	FlockPutPersonDevices(q, "phone", &Device{Name: "Pixel", FirstSeenMs: 5, LastSeenMs: 9}, b.Now())
	FlockRemovePersonLinks(p, &Link{Idp: "google", Subject: "s1"}, a.Now())
	FlockAddPersonLinks(q, &Link{Idp: "apple", Subject: "s2", AddedMs: 7, Assurance: 1}, b.Now())
	FlockAddPersonMemberships(q, &Membership{Tenant: "alpha", Role: "admin"}, b.Now())
	FlockSetPersonGeneration(p, 3, a.Now())
	FlockSetPersonGeneration(q, 5, b.Now())
	FlockSetPersonFirstSeenMs(p, 40, a.Now())
	FlockSetPersonFirstSeenMs(q, 30, b.Now())
	_ = FlockSetPersonHomeRegion(p, "eu", a.Now())
	_ = FlockSetPersonHomeRegion(q, "us", b.Now())
	FlockSetPersonProfileNickname(q, "nick", b.Now())
	FlockSetPersonProfileLastSignInDay(p, 12, a.Now())
	FlockAddPersonProfileEmails(p, "a@x", a.Now())
	FlockSetPersonAliases(p, []string{"x", "y"}, a.Now())
	m := mustMerge(t, p, q)
	o := FlockNewPerson("person-0", &flock.Hlc{WallMs: now - 1, Node: "b"})
	FlockAddPersonTags(o, "y", b.Now())
	FlockPutPersonDevices(o, "laptop", &Device{Name: "Book", FirstSeenMs: 3, LastSeenMs: 4}, b.Now())
	s, l, err := FlockMergeEntriesPerson(m, o, a.Now())
	if err != nil {
		t.Fatal(err)
	}
	FlockAckPerson(l, "a", a.Now())
	gone := proto.Clone(s).(*Person)
	_ = FlockMarkPerson(gone, flock.DeleteReason_DELETE_REASON_USER, b.Now())

	sess := FlockNewSession("session-1", a.Now())
	s1, s2 := proto.Clone(sess).(*Session), proto.Clone(sess).(*Session)
	s1.DeviceId, s1.ExpiresMs = "phone", 100
	FlockWriteSession(s1, a.Now())
	s2.DeviceId, s2.ExpiresMs = "laptop", 200
	FlockWriteSession(s2, b.Now())
	sm, err := FlockMergeSession(s1, s2)
	if err != nil {
		t.Fatal(err)
	}
	hex := func(d []byte) string { return fmt.Sprintf("%x", d) }
	return []string{
		"merged " + hex(FlockDigestPerson(m)),
		"survivor " + hex(FlockDigestPerson(s)),
		"loser " + hex(FlockDigestPerson(l)),
		"marked " + hex(FlockDigestPerson(gone)),
		"session " + hex(FlockDigestSession(sm)),
	}
}

func TestFlockVector(t *testing.T) {
	got := vectorScenario(t)
	for _, line := range got {
		fmt.Println("flock vector:", line)
	}
	path := os.Getenv("FLOCK_VECTOR_FIXTURE")
	if path == "" {
		t.Skip("FLOCK_VECTOR_FIXTURE not set")
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range bytes.Split(bytes.TrimSpace(want), []byte("\n")) {
		if len(l) > 0 && l[0] != '#' {
			lines = append(lines, string(l))
		}
	}
	if fmt.Sprint(lines) != fmt.Sprint(got) {
		t.Fatalf("vector differs from %s:\n got %v\nwant %v", path, got, lines)
	}
}
