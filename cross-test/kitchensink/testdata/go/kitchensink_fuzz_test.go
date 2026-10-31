// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The kitchensink's Go fuzz targets (tacplus#196). cross-test/kitchensink/run.sh
// copies this file into the generated kitchensink package
// (model/types/services/kitchensink) and runs each target with go test -fuzz.
// It lives under testdata/ so the go tool of this repository does not build it.
//
// Every target decodes arbitrary bytes into a kitchensink message; a message
// that decodes must validate without panicking, survive a round trip, and be
// accepted by its generated validator exactly when the declared bounds hold.

package kitchensink

import (
	"math"
	"testing"

	"google.golang.org/protobuf/proto"
)

func roundTrip(t *testing.T, m proto.Message, fresh proto.Message) {
	t.Helper()
	out, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := proto.Unmarshal(out, fresh); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if !proto.Equal(m, fresh) {
		t.Fatalf("round trip changed the message:\n%v\n%v", m, fresh)
	}
}

func boundsHold(m *Bounds) bool {
	oneOf := m.OneOf == 1 || m.OneOf == 2 || m.OneOf == 4 || m.OneOf == 8
	return m.Port >= 0 && m.Port <= 65535 &&
		m.GtZero > 0 &&
		m.GeMinusTen >= -10 &&
		m.LtHundred < 100 &&
		m.LeThousand <= 1000 &&
		m.Window >= 1 && m.Window <= 64 &&
		m.NotSeven != 7 &&
		oneOf &&
		m.Even%2 == 0 &&
		(m.Mode == Mode_MODE_ON || m.Mode == Mode_MODE_OFF) &&
		len(m.TraceId) <= 32768
}

func FuzzBounds(f *testing.F) {
	valid, _ := proto.Marshal(&Bounds{Port: 443, GtZero: 1, LtHundred: 99, LeThousand: 1000, Window: 64, OneOf: 8, Mode: Mode_MODE_ON})
	f.Add(valid)
	f.Add([]byte{})
	f.Add([]byte{0x20, 0x80, 0x80, 0x80, 0x80, 0x0f}) // lt_hundred = 4026531840
	f.Fuzz(func(t *testing.T, data []byte) {
		var m Bounds
		if proto.Unmarshal(data, &m) != nil {
			return
		}
		ok := ValidateBounds(&m) == nil
		if want := boundsHold(&m); ok != want {
			t.Fatalf("ValidateBounds accepted=%v, want %v for %v", ok, want, &m)
		}
		roundTrip(t, &m, &Bounds{})
	})
}

func FuzzSizes(f *testing.F) {
	valid, _ := proto.Marshal(&Sizes{Name: "n", Code: "ab", Counts: []int32{1, 2, 3, 4}})
	f.Add(valid)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		var m Sizes
		if proto.Unmarshal(data, &m) != nil {
			return
		}
		if ValidateSizes(&m) != nil {
			return
		}
		// Accepted: every declared size bound holds.
		if len(m.Name) < 1 || len(m.Name) > 8 || len(m.Blob) > 16 || len(m.Counts) > 4 ||
			len(m.Tags) > 3 || len(m.Labels) > 2 || len(m.Code) < 2 || len(m.Code) > 4 ||
			(m.Nickname != nil && len(*m.Nickname) > 6) {
			t.Fatalf("ValidateSizes accepted out-of-bounds sizes: %v", &m)
		}
		for _, tag := range m.Tags {
			if len(tag) > 5 {
				t.Fatalf("ValidateSizes accepted a tag over 5 bytes: %q", tag)
			}
		}
		roundTrip(t, &m, &Sizes{})
	})
}

func FuzzFloats(f *testing.F) {
	valid, _ := proto.Marshal(&Floats{Ratio: 0.5, Positive: 1})
	f.Add(valid)
	f.Fuzz(func(t *testing.T, data []byte) {
		var m Floats
		if proto.Unmarshal(data, &m) != nil {
			return
		}
		// NaN is not ordered: assert_float does not refuse it yet (#256).
		if math.IsNaN(m.Ratio) || math.IsNaN(float64(m.Positive)) {
			return
		}
		ok := ValidateFloats(&m) == nil
		if want := m.Ratio >= 0 && m.Ratio <= 1 && m.Positive > 0 && len(m.TraceId) <= 32768; ok != want {
			t.Fatalf("ValidateFloats accepted=%v, want %v for %v", ok, want, &m)
		}
	})
}

// Formats, conditionals and the deep tree: the validators parse and recurse;
// they must refuse or accept, never panic, on anything that decodes.
func FuzzGetBoundsRequest(f *testing.F) {
	valid, _ := proto.Marshal(&GetBoundsRequest{Deep: &Deep{Root: &Level1{Inner: &Level2{Leaf: &Level3{Value: 1}}}}})
	f.Add(valid)
	f.Fuzz(func(t *testing.T, data []byte) {
		var m GetBoundsRequest
		if proto.Unmarshal(data, &m) != nil {
			return
		}
		_ = ValidateGetBoundsRequest(&m)
		roundTrip(t, &m, &GetBoundsRequest{})
	})
}

func FuzzFormats(f *testing.F) {
	valid, _ := proto.Marshal(&Formats{Email: "a@b.io", Fqdn: "example.com", Host: "10.0.0.1", Version: "1.2.3", Listen: ":8080", Encoded: "YQ=="})
	f.Add(valid)
	f.Fuzz(func(t *testing.T, data []byte) {
		var m Formats
		if proto.Unmarshal(data, &m) != nil {
			return
		}
		_ = ValidateFormats(&m)
		var c Conditional
		if proto.Unmarshal(data, &c) == nil {
			_ = ValidateConditional(&c)
		}
	})
}

func FuzzScalars(f *testing.F) {
	valid, _ := proto.Marshal(&Scalars{FDouble: 1, FSint32: -1, FFixed64: math.MaxUint64, FString: "s", FMessage: &Empty{}})
	f.Add(valid)
	f.Fuzz(func(t *testing.T, data []byte) {
		var s Scalars
		if proto.Unmarshal(data, &s) == nil && !hasNaN(s.FDouble, float64(s.FFloat)) {
			roundTrip(t, &s, &Scalars{})
		}
		var r RepeatedScalars
		if proto.Unmarshal(data, &r) == nil {
			_ = ValidateRepeatedScalars(&r)
		}
		var o OptionalScalars
		if proto.Unmarshal(data, &o) == nil {
			_ = ValidateOptionalScalars(&o)
		}
		var mp Maps
		if proto.Unmarshal(data, &mp) == nil {
			_ = ValidateMaps(&mp)
		}
	})
}

func hasNaN(v ...float64) bool {
	for _, x := range v {
		if math.IsNaN(x) {
			return true
		}
	}
	return false
}
