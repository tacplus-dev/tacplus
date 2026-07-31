package keepaliveconst

import "testing"

// The invariant is documented on the constants, but a comment cannot stop an
// edit. Violating it is not a subtle degradation: the server answers a
// too-frequent client ping with GOAWAY "too_many_pings" and tears down a
// working connection, so a bad edit here converts a healthy fleet into a
// flapping one. Pin it.
func TestPermitMinTimeNotGreaterThanClientPingInterval(t *testing.T) {
	if PermitMinTimeSecs > TimeSecs {
		t.Fatalf("PermitMinTimeSecs (%d) > TimeSecs (%d): the server would reject "+
			"pings the generated clients are configured to send, and GOAWAY "+
			"too_many_pings would tear down healthy connections",
			PermitMinTimeSecs, TimeSecs)
	}
}

// A keepalive whose timeout exceeds its ping interval can have a second ping
// fall due before the first is declared failed, which delays partition
// detection past the point of the whole mechanism.
func TestTimeoutShorterThanPingInterval(t *testing.T) {
	if TimeoutSecs >= TimeSecs {
		t.Fatalf("TimeoutSecs (%d) >= TimeSecs (%d): a dead connection would not be "+
			"detected within one ping cycle", TimeoutSecs, TimeSecs)
	}
}

// Zero or negative values disable keepalive entirely in every language's
// client builder, which is the exact defect drill 7 exposed (an 80-second
// partition that nothing logged). Fail loudly rather than silently reverting
// to no detection.
func TestValuesArePositive(t *testing.T) {
	for _, c := range []struct {
		name string
		v    int
	}{
		{"TimeSecs", TimeSecs},
		{"TimeoutSecs", TimeoutSecs},
		{"PermitMinTimeSecs", PermitMinTimeSecs},
	} {
		if c.v <= 0 {
			t.Fatalf("%s must be positive, got %d: a non-positive value disables "+
				"keepalive and restores the silent-partition defect", c.name, c.v)
		}
	}
}
