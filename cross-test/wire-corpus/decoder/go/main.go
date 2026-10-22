// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// The go leg of the serialization-equivalence corpus (LED-2 phase 2), and the
// only writer of the golden bytes.
//
//	-emit   re-encode every vector from its `expect` block and rewrite `hex`
//	        in cross-test/fixtures/wire_corpus.json (deterministic marshalling,
//	        so map keys are emitted in sorted order and the blob is stable).
//	default DECODE the checked-in hex with the generated CorpusRecord type and
//	        assert every field equals `expect`; then re-encode `expect` and
//	        assert the bytes are byte-identical to the checked-in hex.
//
// The second assertion is what pins the golden: it turns any future change in
// go's encoding of these shapes into a failure here rather than a silent
// rewrite of the corpus every other language is measured against.
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"

	corpusclient "gitlab.com/example/tacplus/corpus/corpusclient/model/types/corpus/corpusclient"
	"google.golang.org/protobuf/proto"
)

type fixture struct {
	Comment       string   `json:"_comment"`
	SchemeVersion string   `json:"scheme_version"`
	Message       string   `json:"message"`
	Proto         string   `json:"proto"`
	Vectors       []vector `json:"vectors"`
}

type vector struct {
	Name    string `json:"name"`
	Comment string `json:"_comment"`
	Hex     string `json:"hex"`
	Expect  expect `json:"expect"`
}

type expect struct {
	BigNegative     string            `json:"big_negative"`
	BigUnsigned     string            `json:"big_unsigned"`
	Labels          map[string]string `json:"labels"`
	OptCountPresent bool              `json:"opt_count_present"`
	OptCount        int32             `json:"opt_count"`
	BlobHex         string            `json:"blob_hex"`
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
	os.Exit(1)
}

// build materialises a vector's expected values as a generated CorpusRecord.
func build(e expect) *corpusclient.CorpusRecord {
	neg, err := strconv.ParseInt(e.BigNegative, 10, 64)
	if err != nil {
		fail("big_negative %q is not an int64: %v", e.BigNegative, err)
	}
	unsigned, err := strconv.ParseUint(e.BigUnsigned, 10, 64)
	if err != nil {
		fail("big_unsigned %q is not a uint64: %v", e.BigUnsigned, err)
	}
	blob, err := hex.DecodeString(e.BlobHex)
	if err != nil {
		fail("blob_hex %q is not hex: %v", e.BlobHex, err)
	}
	record := &corpusclient.CorpusRecord{
		BigNegative: neg,
		BigUnsigned: unsigned,
		Labels:      e.Labels,
		Blob:        blob,
	}
	if len(blob) == 0 {
		record.Blob = nil
	}
	if len(e.Labels) == 0 {
		record.Labels = nil
	}
	if e.OptCountPresent {
		record.OptCount = proto.Int32(e.OptCount)
	}
	return record
}

func encode(e expect) []byte {
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(build(e))
	if err != nil {
		fail("marshal: %v", err)
	}
	return raw
}

func check(name string, e expect, golden []byte) {
	record := &corpusclient.CorpusRecord{}
	if err := proto.Unmarshal(golden, record); err != nil {
		fail("%s: decode: %v", name, err)
	}

	if got := strconv.FormatInt(record.GetBigNegative(), 10); got != e.BigNegative {
		fail("%s: big_negative: got %s want %s", name, got, e.BigNegative)
	}
	if got := strconv.FormatUint(record.GetBigUnsigned(), 10); got != e.BigUnsigned {
		fail("%s: big_unsigned: got %s want %s", name, got, e.BigUnsigned)
	}
	labels := record.GetLabels()
	if len(labels) != len(e.Labels) {
		fail("%s: labels: got %d entries want %d", name, len(labels), len(e.Labels))
	}
	for key, want := range e.Labels {
		got, ok := labels[key]
		if !ok {
			fail("%s: labels[%q]: missing", name, key)
		}
		if got != want {
			fail("%s: labels[%q]: got %q want %q", name, key, got, want)
		}
	}
	if present := record.OptCount != nil; present != e.OptCountPresent {
		fail("%s: opt_count presence: got %v want %v", name, present, e.OptCountPresent)
	}
	if e.OptCountPresent && record.GetOptCount() != e.OptCount {
		fail("%s: opt_count: got %d want %d", name, record.GetOptCount(), e.OptCount)
	}
	if got := hex.EncodeToString(record.GetBlob()); got != e.BlobHex {
		fail("%s: blob: got %s want %s", name, got, e.BlobHex)
	}

	if reencoded := encode(e); !bytes.Equal(reencoded, golden) {
		fail("%s: re-encode: got %s want %s", name, hex.EncodeToString(reencoded), hex.EncodeToString(golden))
	}
	fmt.Printf("go: %s OK (%d bytes)\n", name, len(golden))
}

func main() {
	path := flag.String("fixture", "", "path to wire_corpus.json")
	emit := flag.Bool("emit", false, "rewrite each vector's hex from its expect block")
	flag.Parse()
	if *path == "" {
		fail("-fixture is required")
	}

	raw, err := os.ReadFile(*path)
	if err != nil {
		fail("read fixture: %v", err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		fail("parse fixture: %v", err)
	}
	if len(f.Vectors) == 0 {
		fail("fixture has no vectors")
	}

	if *emit {
		for i := range f.Vectors {
			f.Vectors[i].Hex = hex.EncodeToString(encode(f.Vectors[i].Expect))
		}
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(&f); err != nil {
			fail("render fixture: %v", err)
		}
		if err := os.WriteFile(*path, out.Bytes(), 0o644); err != nil {
			fail("write fixture: %v", err)
		}
		fmt.Printf("go: emitted %d vectors to %s\n", len(f.Vectors), *path)
		return
	}

	for _, v := range f.Vectors {
		golden, err := hex.DecodeString(v.Hex)
		if err != nil {
			fail("%s: hex: %v", v.Name, err)
		}
		if len(golden) == 0 && v.Name != "" {
			fail("%s: golden hex is empty — run run.sh --emit", v.Name)
		}
		check(v.Name, v.Expect, golden)
	}
	fmt.Printf("go: %d vectors OK\n", len(f.Vectors))
}
