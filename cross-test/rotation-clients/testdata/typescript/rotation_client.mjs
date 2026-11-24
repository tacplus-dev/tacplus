// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// The TypeScript leg of cross-test/rotation-clients (tacplus#323): the
// generated TypeScript C2S client (c2sm) against the generated Go server,
// run by the Go fixture's TestRotationClients. The callers below are plain
// caller code: nothing in them mentions rotation. Each stream must outlive
// several token lifetimes with no loss, duplication or reordering, on one
// handler call with one context.
//
// Env: ROTATION_ADDR (the server, with the sibling), ROTATION_PLAIN_ADDR (the
// plain service only), ROTATION_CONTROL (tokens and the handler's ends),
// ROTATION_TOKEN_LIFETIME_MS, TS_C2SM and TS_TYPES (the generated modules).

import * as grpc from '@grpc/grpc-js';
import { create } from '@bufbuild/protobuf';
import { pathToFileURL } from 'node:url';

const { ServiceManager } = await import(pathToFileURL(process.env.TS_C2SM).href);
const { TickSchema } = await import(pathToFileURL(process.env.TS_TYPES).href);

const ADDR = process.env.ROTATION_ADDR;
const PLAIN_ADDR = process.env.ROTATION_PLAIN_ADDR;
const CONTROL = process.env.ROTATION_CONTROL;
const LIFETIME_MS = Number(process.env.ROTATION_TOKEN_LIFETIME_MS);
// Several token lifetimes.
const STREAM_FOR_MS = 3 * LIFETIME_MS;

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function checkSequence(what, got, n) {
  assert(got.length === n, `${what}: got ${got.length} messages, want ${n} (loss or duplication)`);
  got.forEach((seq, i) => {
    assert(seq === i + 1, `${what}: message ${i} is seq ${seq}, want ${i + 1} (reordering, loss or duplication)`);
  });
}

async function control(path) {
  const resp = await fetch(`${CONTROL}${path}`);
  assert(resp.ok, `control ${path}: HTTP ${resp.status}`);
  return resp.text();
}

// tokenSource is the caller's token provider: alice's tokens, until revoked
// (the mint fails) or impersonating (another subject's).
function tokenSource() {
  const src = { subject: 'alice@example.com', revoked: false };
  src.provide = async () => {
    if (src.revoked) throw new Error('identity: caller revoked');
    return control(`/token?sub=${encodeURIComponent(src.subject)}`);
  };
  return src;
}

function client(addr, src) {
  const sm = new ServiceManager(addr, grpc.credentials.createInsecure(), src.provide);
  return { sm, api: sm.grpc().rotationclients().viewer_service_client() };
}

// handlerEnd waits for the handler's one end since the last call.
async function handlerEnd(method, withinMs = 10_000) {
  const deadline = Date.now() + withinMs;
  for (;;) {
    const ends = JSON.parse(await control('/ends'));
    if (ends.length > 0) {
      assert(ends.length === 1, `${method}: ${ends.length} handler calls, want one: ${JSON.stringify(ends)}`);
      assert(ends[0].method === method, `handler end is ${ends[0].method}, want ${method}`);
      return ends[0];
    }
    assert(Date.now() < deadline, `${method}: the handler did not return within ${withinMs}ms`);
    await sleep(50);
  }
}

async function mints() {
  return Number(await control('/mints'));
}

const tick = (seq, m) => create(TickSchema, { seq: BigInt(seq), m });

// The bidi stream both ways at once across several lifetimes: every tick
// reaches the handler and comes back exactly once, in order, on one handler
// call with one context and one connect of the caller's stream.
async function bidiBothWays() {
  const src = tokenSource();
  const { sm, api } = client(ADDR, src);
  const before = await mints();
  const echoed = [];
  let connects = 0;
  const handle = api.assistTicks(() => { connects++; }, (m) => echoed.push(Number(m.seq)));
  const start = Date.now();
  let n = 0;
  while (Date.now() - start < STREAM_FOR_MS) {
    n++;
    await handle.send(tick(n, 'up'));
    await sleep(2);
  }
  const deadline = Date.now() + 5_000;
  while (echoed.length < n && Date.now() < deadline) await sleep(10);
  handle.close();
  checkSequence('bidi echoed', echoed, n);
  assert(connects === 1, `bidi: the caller's stream connected ${connects} times, want once`);
  const end = await handlerEnd('AssistTicks');
  assert(end.sameContext, 'bidi: the handler saw more than one context');
  assert(end.received === n && end.inOrder, `bidi: the handler received ${end.received} (in order: ${end.inOrder}), want 1..${n}`);
  assert(end.subject === 'alice@example.com', `bidi: the handler's caller is ${end.subject}`);
  const renewed = (await mints()) - before;
  assert(renewed >= 3, `bidi: ${renewed} tokens minted, want several: the stream did not rotate`);
  sm.close();
  console.log(`bidi: ${n} ticks both ways across ${renewed} tokens, one handler call`);
}

// A server stream across several lifetimes: every tick once, in order, and
// the stream ends OK with the handler's own end.
async function serverStream() {
  const src = tokenSource();
  const { sm, api } = client(ADDR, src);
  const want = Math.floor(STREAM_FOR_MS / 50);
  const stream = await api.generateTicks(tick(want, 'down'));
  const got = [];
  for await (const m of stream) got.push(Number(m.seq));
  checkSequence('server stream', got, want);
  const end = await handlerEnd('GenerateTicks');
  assert(end.code === 'OK' && end.sameContext, `server stream: handler end ${JSON.stringify(end)}`);
  sm.close();
  console.log(`server stream: ${want} ticks across lifetimes, one handler call`);
}

// A client stream across several lifetimes: the handler's one answer counts
// every tick, in order.
async function clientStream() {
  const src = tokenSource();
  const { sm, api } = client(ADDR, src);
  let n = 0;
  async function* ticks() {
    const start = Date.now();
    while (Date.now() - start < STREAM_FOR_MS) {
      n++;
      yield tick(n, 'up');
      await sleep(5);
    }
  }
  const resp = await api.sendTicks(ticks());
  assert(Number(resp.seq) === n && resp.m === 'in-order', `client stream: the handler answered ${resp.seq} ${resp.m}, want ${n} in-order`);
  const end = await handlerEnd('SendTicks');
  assert(end.sameContext, 'client stream: the handler saw more than one context');
  sm.close();
  console.log(`client stream: ${n} ticks across lifetimes, one handler call`);
}

// streamError reads a server stream until it ends; it resolves the error it
// ended with (undefined: OK) and how long after open.
async function streamError(stream) {
  const opened = Date.now();
  let ticks = 0;
  try {
    for await (const _ of stream) ticks++;
    return { err: undefined, ms: Date.now() - opened, ticks };
  } catch (err) {
    return { err, ms: Date.now() - opened, ticks };
  }
}

// A rotation with another subject's token (and the valid session id) is
// refused: the stream ends PERMISSION_DENIED, within about a lifetime.
async function anotherSubjectRefused() {
  const src = tokenSource();
  const { sm, api } = client(ADDR, src);
  const stream = await api.generateTicks(tick(0, 'down'));
  src.subject = 'mallory@example.com';
  const { err, ms, ticks } = await streamError(stream);
  assert(ticks > 0, 'another subject: no tick arrived before the refusal');
  assert(err?.code === grpc.status.PERMISSION_DENIED, `another subject: the stream ended with ${err?.code} (${err?.message}), want PERMISSION_DENIED`);
  assert(ms < 2 * LIFETIME_MS + 3_000, `another subject: the stream lasted ${ms}ms, want it ended within its token's lifetime`);
  const end = await handlerEnd('GenerateTicks');
  assert(end.cancelled && end.subject === 'alice@example.com', `another subject: handler end ${JSON.stringify(end)}`);
  sm.close();
  console.log(`another subject: refused PERMISSION_DENIED after ${ms}ms`);
}

// A caller that can no longer get a token (revoked) is cut off within one
// lifetime: UNAUTHENTICATED at its last token's exp.
async function revokedCutOff() {
  const src = tokenSource();
  const { sm, api } = client(ADDR, src);
  const stream = await api.generateTicks(tick(0, 'down'));
  src.revoked = true;
  const { err, ms } = await streamError(stream);
  assert(err?.code === grpc.status.UNAUTHENTICATED, `revoked: the stream ended with ${err?.code} (${err?.message}), want UNAUTHENTICATED`);
  assert(ms < LIFETIME_MS + 3_000, `revoked: the stream lasted ${ms}ms, want it cut off within one lifetime`);
  const end = await handlerEnd('GenerateTicks');
  assert(end.cancelled, `revoked: the handler's context was not cancelled: ${JSON.stringify(end)}`);
  sm.close();
  console.log(`revoked: cut off UNAUTHENTICATED after ${ms}ms`);
}

// Against a server without the sibling the client falls back to the plain
// method, which works and ends at its token's exp.
async function fallsBackToPlain() {
  const src = tokenSource();
  const { sm, api } = client(PLAIN_ADDR, src);
  const stream = await api.generateTicks(tick(0, 'down'));
  const { err, ms, ticks } = await streamError(stream);
  assert(ticks > 0, `fallback: no tick arrived on the plain method (${err?.message})`);
  assert(err?.code === grpc.status.UNAUTHENTICATED, `fallback: the plain stream ended with ${err?.code} (${err?.message}), want UNAUTHENTICATED at exp`);
  assert(ms < LIFETIME_MS + 3_000, `fallback: the plain stream lasted ${ms}ms, want it ended at its token's exp`);
  await handlerEnd('GenerateTicks');
  sm.close();
  console.log(`fallback: the plain method, ended at exp after ${ms}ms`);
}

for (const leg of [bidiBothWays, serverStream, clientStream, anotherSubjectRefused, revokedCutOff, fallsBackToPlain]) {
  console.log(`--- ${leg.name}`);
  await leg();
}
console.log('typescript rotation legs OK');
process.exit(0);
