// Copyright (c) 2026 Second Star Cavalier LLC
//
// The typescript leg of the serialization-equivalence corpus (LED-2 phase 2).
// Decodes the checked-in golden bytes with the GENERATED CorpusRecord schema
// and asserts every field equals the fixture's `expect` block. It never
// encodes: go owns the bytes.
//
// int64/uint64 arrive as bigint from @bufbuild/protobuf, which is exactly what
// the `big_unsigned` vector is here to prove: a runtime that decoded it into a
// double would round 9007199254740993 to 9007199254740992.

import { readFileSync } from 'node:fs';

import { fromBinary } from '@bufbuild/protobuf';

import { CorpusRecordSchema } from '../model/dist/types/corpus/corpus/index.js';

function fail(message) {
  console.error(`FAIL: ${message}`);
  process.exit(1);
}

function unhex(text) {
  if (text.length % 2 !== 0) {
    fail(`hex string has odd length: ${text}`);
  }
  const bytes = new Uint8Array(text.length / 2);
  for (let i = 0; i < bytes.length; i += 1) {
    bytes[i] = Number.parseInt(text.slice(i * 2, i * 2 + 2), 16);
  }
  return bytes;
}

function hex(bytes) {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
}

function check(name, expect, golden) {
  const record = fromBinary(CorpusRecordSchema, golden);

  if (String(record.bigNegative) !== expect.big_negative) {
    fail(`${name}: big_negative: got ${record.bigNegative} want ${expect.big_negative}`);
  }
  if (String(record.bigUnsigned) !== expect.big_unsigned) {
    fail(`${name}: big_unsigned: got ${record.bigUnsigned} want ${expect.big_unsigned}`);
  }

  const labels = record.labels ?? {};
  const wantLabels = expect.labels;
  if (Object.keys(labels).length !== Object.keys(wantLabels).length) {
    fail(
      `${name}: labels: got ${Object.keys(labels).length} entries want ${Object.keys(wantLabels).length}`,
    );
  }
  for (const [key, want] of Object.entries(wantLabels)) {
    if (!Object.prototype.hasOwnProperty.call(labels, key)) {
      fail(`${name}: labels[${key}]: missing`);
    }
    if (labels[key] !== want) {
      fail(`${name}: labels[${key}]: got ${JSON.stringify(labels[key])} want ${JSON.stringify(want)}`);
    }
  }

  const present = record.optCount !== undefined;
  if (present !== expect.opt_count_present) {
    fail(`${name}: opt_count presence: got ${present} want ${expect.opt_count_present}`);
  }
  if (expect.opt_count_present && record.optCount !== expect.opt_count) {
    fail(`${name}: opt_count: got ${record.optCount} want ${expect.opt_count}`);
  }

  const blobHex = hex(record.blob ?? new Uint8Array());
  if (blobHex !== expect.blob_hex) {
    fail(`${name}: blob: got ${blobHex} want ${expect.blob_hex}`);
  }

  console.log(`typescript: ${name} OK (${golden.length} bytes)`);
}

const path = process.argv[2];
if (!path) {
  fail('usage: decode.mjs <wire_corpus.json>');
}
const fixture = JSON.parse(readFileSync(path, 'utf8'));
if (!fixture.vectors || fixture.vectors.length === 0) {
  fail('fixture has no vectors');
}
for (const vector of fixture.vectors) {
  check(vector.name, vector.expect, unhex(vector.hex));
}
console.log(`typescript: ${fixture.vectors.length} vectors OK`);
