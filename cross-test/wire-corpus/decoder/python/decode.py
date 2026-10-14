# Copyright (c) 2026 Seabeam LLC
# SPDX-License-Identifier: Apache-2.0
#
# The python leg of the serialization-equivalence corpus (LED-2 phase 2).
# Decodes the checked-in golden bytes with the GENERATED CorpusRecord type and
# asserts every field equals the fixture's `expect` block. It never encodes:
# go owns the bytes.

import binascii
import json
import sys

from corpusclient_model.types.corpus.corpusclient import corpus_pb2


def fail(message):
    print("FAIL: " + message, file=sys.stderr)
    sys.exit(1)


def check(name, expect, golden):
    record = corpus_pb2.CorpusRecord()
    parsed = record.ParseFromString(golden)
    if parsed != len(golden):
        fail("%s: decode consumed %d of %d bytes" % (name, parsed, len(golden)))

    if str(record.big_negative) != expect["big_negative"]:
        fail("%s: big_negative: got %s want %s" % (name, record.big_negative, expect["big_negative"]))
    if str(record.big_unsigned) != expect["big_unsigned"]:
        fail("%s: big_unsigned: got %s want %s" % (name, record.big_unsigned, expect["big_unsigned"]))

    labels = dict(record.labels)
    want_labels = expect["labels"]
    if len(labels) != len(want_labels):
        fail("%s: labels: got %d entries want %d" % (name, len(labels), len(want_labels)))
    for key, want in want_labels.items():
        if key not in labels:
            fail("%s: labels[%r]: missing" % (name, key))
        if labels[key] != want:
            fail("%s: labels[%r]: got %r want %r" % (name, key, labels[key], want))

    present = record.HasField("opt_count")
    if present != expect["opt_count_present"]:
        fail("%s: opt_count presence: got %s want %s" % (name, present, expect["opt_count_present"]))
    if expect["opt_count_present"] and record.opt_count != expect["opt_count"]:
        fail("%s: opt_count: got %d want %d" % (name, record.opt_count, expect["opt_count"]))

    blob_hex = binascii.hexlify(record.blob).decode("ascii")
    if blob_hex != expect["blob_hex"]:
        fail("%s: blob: got %s want %s" % (name, blob_hex, expect["blob_hex"]))

    print("python: %s OK (%d bytes)" % (name, len(golden)))


def main():
    if len(sys.argv) != 2:
        fail("usage: decode.py <wire_corpus.json>")
    with open(sys.argv[1], "r", encoding="utf-8") as handle:
        fixture = json.load(handle)
    vectors = fixture["vectors"]
    if not vectors:
        fail("fixture has no vectors")
    for vector in vectors:
        check(vector["name"], vector["expect"], binascii.unhexlify(vector["hex"]))
    print("python: %d vectors OK" % len(vectors))


main()
