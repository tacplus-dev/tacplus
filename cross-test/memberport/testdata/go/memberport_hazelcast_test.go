// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package memberport

// The Go member ports of the Hazelcast leg (tacplus#322):
// cross-test/memberport/run.sh --language go --interop runs it beside
// HazelcastMemberPortInteropTest.java, both pointed at
// MEMBERPORT_HAZELCAST_DIR. The Java side runs HazelcastMemberPortTest's
// cases with Hazelcast members (which must be Java) whose member traffic goes
// through Go member ports, all of them here: this side owns the fleet, each
// member's token minter and certificate, and serves the Java side's numbered
// command files (cmd.N, answered by ack.N; the verbs are documented on
// HazelcastMemberPortInteropTest).

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMemberPortHazelcastWithJava(t *testing.T) {
	dir := os.Getenv("MEMBERPORT_HAZELCAST_DIR")
	if dir == "" {
		t.Skip("MEMBERPORT_HAZELCAST_DIR runs the Hazelcast leg against Go member ports (run.sh --interop)")
	}
	fx := newFixture(t)
	type member struct {
		port   *MemberPort
		minter *minter
	}
	members := map[string]*member{}
	run := func(words []string) (string, error) {
		arg := func(i int) string {
			if i < len(words) {
				return words[i]
			}
			return ""
		}
		num := func(i int) int64 {
			n, err := strconv.ParseInt(arg(i), 10, 64)
			if err != nil {
				panic(fmt.Sprintf("%q: %v", words, err))
			}
			return n
		}
		get := func() (*member, error) {
			m, ok := members[arg(1)]
			if !ok {
				return nil, fmt.Errorf("no member %q", arg(1))
			}
			return m, nil
		}
		switch arg(0) {
		case "member":
			m := &member{minter: newMinter(fx.fleet, arg(2), num(3), num(4))}
			m.port = fx.memberWith(m.minter.token, certRef(fx.fleet.leaf(t, arg(2), testSvc, "Ed25519")), fx.fleet.lookup)
			members[arg(1)] = m
			addr, err := m.port.Listen("127.0.0.1:0", arg(5))
			if err != nil {
				return "", err
			}
			return addr.String(), nil
		case "forward":
			m, err := get()
			if err != nil {
				return "", err
			}
			addr, err := m.port.Forward(arg(2))
			if err != nil {
				return "", err
			}
			return addr.String(), nil
		case "mints":
			m, err := get()
			if err != nil {
				return "", err
			}
			return strconv.Itoa(int(m.minter.mints.Load())), nil
		case "never-renew":
			m, err := get()
			if err != nil {
				return "", err
			}
			m.minter.neverRenew.Store(true)
			return "", nil
		case "mint":
			return fx.fleet.mint(testSvc, arg(1), testAud, num(2)), nil
		case "reset", "end":
			for id, m := range members {
				m.port.Close()
				delete(members, id)
			}
			return "", nil
		}
		return "", fmt.Errorf("unknown command %q", words)
	}

	// The first command waits for the Java side's build.
	wait := 10 * time.Minute
	for n := 0; ; n++ {
		line := waitInterop(t, dir, fmt.Sprintf("cmd.%d", n), wait)
		wait = 5 * time.Minute
		words := strings.Fields(line)
		value, err := run(words)
		answer := "ok"
		if err != nil {
			t.Errorf("%s: %v", line, err)
			answer = "err " + err.Error()
		} else if value != "" {
			answer += " " + value
		}
		writeInterop(t, dir, fmt.Sprintf("ack.%d", n), []byte(answer+"\n"))
		if len(words) > 0 && words[0] == "end" {
			return
		}
	}
}
