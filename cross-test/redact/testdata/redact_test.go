// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The go leg of the redact gate (issue #274). Copied into the generated
// redactcheck package by cross-test/redact/run.sh, so it runs the GENERATED
// Redact<T> and validators against cross-test/fixtures/redact_vectors.json.
package redactcheck

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
)

type redactVectors struct {
	Redact []struct {
		Name      string `json:"name"`
		Type      string `json:"type"`
		InputHex  string `json:"input_hex"`
		ExpectHex string `json:"expect_hex"`
	} `json:"redact"`
	Verdicts []struct {
		Name     string `json:"name"`
		Field    string `json:"field"`
		ValueHex string `json:"value_hex"`
		Expect   string `json:"expect"`
	} `json:"verdicts"`
}

func redactByType(typ string, in []byte) (proto.Message, error) {
	switch typ {
	case "UniConfig":
		m := &UniConfig{}
		if err := proto.Unmarshal(in, m); err != nil {
			return nil, err
		}
		return RedactUniConfig(m), nil
	case "Secrets":
		m := &Secrets{}
		if err := proto.Unmarshal(in, m); err != nil {
			return nil, err
		}
		return RedactSecrets(m), nil
	case "Endpoint":
		m := &Endpoint{}
		if err := proto.Unmarshal(in, m); err != nil {
			return nil, err
		}
		return RedactEndpoint(m), nil
	case "SecretCheck":
		m := &SecretCheck{}
		if err := proto.Unmarshal(in, m); err != nil {
			return nil, err
		}
		return RedactSecretCheck(m), nil
	}
	return nil, fmt.Errorf("unknown type %q", typ)
}

func baseSecretCheck() *SecretCheck {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return &SecretCheck{Token: "Xk9#mP2$vL7@qR4!wT8&", Key: key}
}

var ruleText = map[string]string{
	"required":    "required and empty",
	"placeholder": "a known placeholder value",
	"entropy":     "estimated entropy below",
}

func TestRedactVectors(t *testing.T) {
	raw, err := os.ReadFile(os.Getenv("REDACT_VECTOR_FIXTURE"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var vec redactVectors
	if err := json.Unmarshal(raw, &vec); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	for _, c := range vec.Redact {
		in, _ := hex.DecodeString(c.InputHex)
		out, err := redactByType(c.Type, in)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		got, err := proto.MarshalOptions{Deterministic: true}.Marshal(out)
		if err != nil {
			t.Fatalf("%s: marshal: %v", c.Name, err)
		}
		if hex.EncodeToString(got) != c.ExpectHex {
			t.Errorf("%s: Redact%s bytes\n got  %x\n want %s", c.Name, c.Type, got, c.ExpectHex)
		}
		// The same input always gives the same output.
		again, _ := redactByType(c.Type, in)
		againBytes, _ := proto.MarshalOptions{Deterministic: true}.Marshal(again)
		if !bytes.Equal(againBytes, got) {
			t.Errorf("%s: Redact%s is not deterministic", c.Name, c.Type)
		}
	}
	for _, c := range vec.Verdicts {
		value, _ := hex.DecodeString(c.ValueHex)
		m := baseSecretCheck()
		switch c.Field {
		case "token":
			m.Token = string(value)
		case "key":
			m.Key = value
		case "optional_token":
			m.OptionalToken = string(value)
		default:
			t.Fatalf("%s: unknown field %q", c.Name, c.Field)
		}
		err := ValidateSecretCheck(m)
		if c.Expect == "ok" {
			if err != nil {
				t.Errorf("%s: want ok, got %v", c.Name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: want %s, got ok", c.Name, c.Expect)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "SecretCheck."+c.Field) || !strings.Contains(msg, ruleText[c.Expect]) {
			t.Errorf("%s: want field SecretCheck.%s and rule %q, got %q", c.Name, c.Field, ruleText[c.Expect], msg)
		}
		if len(value) >= 6 && strings.Contains(msg, string(value)) {
			t.Errorf("%s: the error echoes the value: %q", c.Name, msg)
		}
	}
	// A CSPRNG-generated 32-byte key passes.
	m := baseSecretCheck()
	m.Key = make([]byte, 32)
	if _, err := rand.Read(m.Key); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSecretCheck(m); err != nil {
		t.Errorf("csprng key: want ok, got %v", err)
	}
	fmt.Printf("redact go: %d redact cases, %d verdicts\n", len(vec.Redact), len(vec.Verdicts))
}
