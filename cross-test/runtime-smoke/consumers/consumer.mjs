// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Runtime smoke: the generated typescript CLIENT surfaces against the live go
// fixture server (cross-test/runtime-smoke/fixture). Ports arrive via env:
//   TACPLUS_FIXTURE_GRPC          host:port of the auth-wired gRPC server
//   TACPLUS_FIXTURE_GRPC_UNWIRED  host:port of the fail-closed (no auth funcs) server
//   TACPLUS_FIXTURE_HTTP          base URL of the W2S HTTP/SSE/WebSocket server
//
// This is the client half of the pre-PAR-3 typescript `both` runtime smoke
// (C2S gRPC, W2S HTTP/SSE, W2S upload, W2S chat, audit accounting + trace).
// Handler-reached assertions read the fixture's /fixture/stats endpoint. The
// WatchHub client-lib contract from the old smoke is dead for typescript
// (full-client builds emit no server-side collection runtime); its go
// equivalent lives in the consumer-contract go tests.

import * as grpc from '@grpc/grpc-js';
import { create, fromBinary, toBinary } from '@bufbuild/protobuf';

import { ServiceManager as C2SServiceManager } from '../model/dist/c2sm/index.js';
import { ServiceManager as W2SServiceManager } from '../model/dist/w2sm/index.js';
import {
  ContractEventSchema,
  ContractMode,
  ContractPageRequestSchema,
  ContractRequestSchema,
  ContractResponseSchema,
  ContractWatchRequestSchema,
  validateContractRequest,
  validateContractResponse,
} from '../model/dist/types/contract/contract/index.js';
import {
  AuditEventSchema,
  AuditRequestSchema,
  AuditResponseSchema,
} from '../model/dist/types/services/contractaudit/index.js';

// Node < 21 has no global WebSocket; the W2S client uses globalThis.WebSocket.
if (!globalThis.WebSocket) {
  const { WebSocket } = await import('ws');
  globalThis.WebSocket = WebSocket;
}

const GRPC_ADDR = process.env.TACPLUS_FIXTURE_GRPC;
const GRPC_UNWIRED_ADDR = process.env.TACPLUS_FIXTURE_GRPC_UNWIRED;
const HTTP_URL = process.env.TACPLUS_FIXTURE_HTTP.replace(/\/$/, '');

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}

function validRequest() {
  return create(ContractRequestSchema, {
    tenantNamespace: 'tenant.alpha',
    itemId: 'item-123',
    mode: ContractMode.READ,
  });
}

function invalidRequest() {
  return create(ContractRequestSchema, {
    tenantNamespace: 'tenant.alpha',
    itemId: 'no',
    mode: ContractMode.READ,
  });
}

function forbiddenRequest() {
  return create(ContractRequestSchema, {
    tenantNamespace: 'tenant.beta',
    itemId: 'item-123',
    mode: ContractMode.READ,
  });
}

function requestWithItem(itemId) {
  return create(ContractRequestSchema, {
    tenantNamespace: 'tenant.alpha',
    itemId,
    mode: ContractMode.READ,
  });
}

function validWatchRequest() {
  return create(ContractWatchRequestSchema, {
    tenantNamespace: 'tenant.alpha',
    itemId: 'item-123',
    mode: ContractMode.READ,
    fromRevision: 0n,
  });
}

function watchRequestWithItem(itemId) {
  return create(ContractWatchRequestSchema, {
    tenantNamespace: 'tenant.alpha',
    itemId,
    mode: ContractMode.READ,
  });
}

async function fetchStats() {
  const resp = await fetch(`${HTTP_URL}/fixture/stats`);
  assert(resp.ok, `fixture stats fetch failed: ${resp.status}`);
  return await resp.json();
}

async function expectCounter(name, want) {
  const stats = await fetchStats();
  assert(stats[name] === want, `fixture stats ${name} = ${stats[name]}, want ${want}`);
}

async function expectRejects(fn, message, needle) {
  try {
    await fn();
  } catch (err) {
    const text = String(err?.message ?? err);
    const matches = needle instanceof RegExp ? needle.test(text) : !needle || text.includes(needle);
    if (!matches) {
      throw new Error(`${message}: unexpected failure: ${text}`);
    }
    return;
  }
  throw new Error(message);
}

async function expectRejectsCode(fn, code, message) {
  try {
    await fn();
  } catch (err) {
    if (err?.code !== code) {
      throw new Error(`${message}: expected code ${code}, got ${err?.code}: ${err?.message}`);
    }
    return;
  }
  throw new Error(message);
}

async function collectAsync(streamOrIterable) {
  const items = [];
  for await (const item of streamOrIterable) {
    items.push(item);
  }
  return items;
}

function watchEventCase(event) {
  return event?.event?.case;
}

async function runC2SRuntimeSmoke() {
  const c2sNoToken = new C2SServiceManager(GRPC_ADDR, grpc.credentials.createInsecure());
  const c2sAuth = new C2SServiceManager(GRPC_ADDR, grpc.credentials.createInsecure(), async () => 'token');
  const c2sFailingToken = new C2SServiceManager(
    GRPC_ADDR,
    grpc.credentials.createInsecure(),
    async () => { throw new Error('identity unavailable'); },
  );
  try {
    const api = c2sNoToken.grpc().contractclient().contract_api_client();
    const apiAuth = c2sAuth.grpc().contractclient().contract_api_client();
    const apiFailing = c2sFailingToken.grpc().contractclient().contract_api_client();

    await expectRejects(
      () => api.getContract(validRequest()),
      'protected TypeScript C2S Read without token provider was accepted',
      'auth token provider is required',
    );
    await expectCounter('readCalls', 0);
    await expectCounter('authorizeCalls', 0);

    const protectedResp = await apiAuth.getContract(validRequest());
    assert(protectedResp.resultId === 'result-123', 'unexpected C2S Read result');
    assert(protectedResp.accepted === true, 'unexpected C2S Read accepted flag');
    await expectCounter('readCalls', 1);

    await expectRejectsCode(
      () => apiAuth.getContract(forbiddenRequest()),
      grpc.status.PERMISSION_DENIED,
      'namespace-mismatched TypeScript C2S Read was accepted',
    );
    await expectCounter('readCalls', 1);

    await expectRejects(
      () => apiFailing.getContract(validRequest()),
      'protected TypeScript C2S Read with failing token provider was accepted',
      'identity unavailable',
    );
    await expectCounter('readCalls', 1);

    const publicResp = await api.getPublicContract(validRequest());
    assert(publicResp.resultId === 'result-123', 'unexpected C2S GetPublicContract result');
    await expectCounter('publicReadCalls', 1);

    const failingPublicResp = await apiFailing.getPublicContract(validRequest());
    assert(failingPublicResp.resultId === 'result-123', 'unexpected failing-token GetPublicContract result');
    await expectCounter('publicReadCalls', 2);

    await expectRejects(
      () => api.getPublicContract(invalidRequest()),
      'invalid TypeScript C2S request was accepted',
      /item_?id/i,
    );
    await expectCounter('publicReadCalls', 2);

    await expectRejectsCode(
      () => api.getPublicContract(requestWithItem('bad-response')),
      grpc.status.INTERNAL,
      'invalid TypeScript C2S response was accepted',
    );
    await expectCounter('publicReadCalls', 3);

    await expectRejects(
      async () => collectAsync(await api.watch(validWatchRequest())),
      'auth-any-user TypeScript C2S Watch without token was accepted',
      'auth token provider is required',
    );
    await expectCounter('watchCalls', 0);
    await expectCounter('authenticateCalls', 0);

    const watchEvents = await collectAsync(await apiAuth.watch(validWatchRequest()));
    assert(watchEvents.length === 2, `expected snapshot_item + snapshot_done Watch events, got ${watchEvents.length}`);
    assert(watchEventCase(watchEvents[0]) === 'snapshotItem', 'unexpected Watch event case');
    assert(watchEvents[0].event.value.eventId === 'event-1', 'unexpected Watch event');
    assert(watchEventCase(watchEvents[1]) === 'snapshotDone', 'unexpected Watch completion case');
    assert(Number(watchEvents[1].event.value) === 1, 'unexpected Watch snapshot revision');
    await expectCounter('watchCalls', 1);

    await expectRejects(
      async () => collectAsync(await apiAuth.watch(watchRequestWithItem('no'))),
      'invalid TypeScript C2S Watch request was accepted',
      /item_?id/i,
    );
    await expectCounter('watchCalls', 1);

    await expectRejectsCode(
      async () => collectAsync(await apiAuth.watch(watchRequestWithItem('bad-event'))),
      grpc.status.INTERNAL,
      'invalid TypeScript C2S Watch response was accepted',
    );
    await expectCounter('watchCalls', 2);

    await expectRejects(
      async () => collectAsync(await api.listEvents(validRequest())),
      'auth-any-user TypeScript C2S ListEvents without token was accepted',
      'auth token provider is required',
    );
    await expectCounter('listEventsCalls', 0);

    const listed = await collectAsync(await apiAuth.listEvents(validRequest()));
    assert(listed.length === 1, `expected one ListEvents event, got ${listed.length}`);
    assert(listed[0].eventId === 'event-1', 'unexpected ListEvents event');
    await expectCounter('listEventsCalls', 1);

    await expectRejects(
      async () => collectAsync(await apiAuth.listEvents(invalidRequest())),
      'invalid TypeScript C2S ListEvents request was accepted',
      /item_?id/i,
    );
    await expectCounter('listEventsCalls', 1);

    await expectRejectsCode(
      async () => collectAsync(await apiAuth.listEvents(requestWithItem('bad-event'))),
      grpc.status.INTERNAL,
      'invalid TypeScript C2S ListEvents response was accepted',
    );
    await expectCounter('listEventsCalls', 2);

    // PAGE auto-pager: follows next_page_token until exhausted. The fixture
    // returns TWO keyset pages, so the pager must make a second fetch with the
    // NON-EMPTY continuation token before terminating (PC-C-3).
    const pageReq = create(ContractPageRequestSchema, {
      tenantNamespace: 'tenant.alpha',
      pageSize: 50,
    });
    const pagedItems = await collectAsync(await apiAuth.listEventsPaged(pageReq));
    assert(pagedItems.length === 2, `expected two paged items across two pages, got ${pagedItems.length}`);
    assert(pagedItems[0].eventId === 'event-1', 'unexpected first paged item');
    assert(pagedItems[1].eventId === 'event-2', 'unexpected second paged item');
    await expectCounter('pagedCalls', 2);
  } finally {
    c2sNoToken.close();
    c2sAuth.close();
    c2sFailingToken.close();
  }
}

async function runUnwiredAuthSmoke() {
  // The go wrapper is fail-closed: a mandatory-auth method with no wired
  // authorizer rejects with a plain "authorizer not configured" error, which
  // gRPC surfaces as UNKNOWN. The handler must never be reached.
  const c2s = new C2SServiceManager(GRPC_UNWIRED_ADDR, grpc.credentials.createInsecure(), async () => 'token');
  try {
    const api = c2s.grpc().contractclient().contract_api_client();
    try {
      await api.getContract(validRequest());
      throw new Error('unwired TypeScript mandatory-auth wrapper did not fail closed');
    } catch (err) {
      if (err?.code !== grpc.status.UNKNOWN || !String(err?.message).includes('authorizer not configured')) {
        throw new Error(`unexpected unwired-auth failure: code=${err?.code} ${err?.message}`);
      }
    }
    await expectCounter('unwiredHandlerCalls', 0);
  } finally {
    c2s.close();
  }
}

async function runW2SHttpSseSmoke() {
  let tokenCalls = 0;
  const sm = new W2SServiceManager({ gatewayUrl: HTTP_URL }, async () => {
    tokenCalls += 1;
    return 'token';
  });
  const api = sm.w2sClients().contractclient().contract_api_client();

  const publicResp = await api.getPublicContract(validRequest());
  assert(publicResp.resultId === 'result-123', 'unexpected W2S GetPublicContract result');
  assert(publicResp.accepted === true, 'unexpected W2S GetPublicContract accepted flag');
  assert(tokenCalls === 0, 'auth-disabled W2S request asked for a token');
  await expectCounter('w2sPublicReadRequests', 1);

  await expectRejects(
    () => api.getPublicContract(invalidRequest()),
    'invalid TypeScript W2S request was accepted',
    /item_?id/i,
  );
  await expectCounter('w2sPublicReadRequests', 1);

  await expectRejects(
    () => api.getPublicContract(requestWithItem('bad-response')),
    'invalid TypeScript W2S response was accepted',
  );
  await expectCounter('w2sPublicReadRequests', 2);

  const readResp = await api.getContract(validRequest());
  assert(readResp.resultId === 'result-123', 'unexpected W2S GetContract result');
  assert(tokenCalls === 1, `expected one token call for W2S Read, got ${tokenCalls}`);
  await expectCounter('w2sReadRequests', 1);

  const seen = await collectAsync(await api.watch(validWatchRequest()));
  assert(seen.length === 2, `expected snapshot_item + snapshot_done over SSE, got ${seen.length}`);
  assert(watchEventCase(seen[0]) === 'snapshotItem', 'unexpected W2S Watch event case');
  assert(seen[0].event.value.eventId === 'event-1', 'unexpected W2S Watch event');
  assert(watchEventCase(seen[1]) === 'snapshotDone', 'unexpected W2S Watch completion case');
  assert(tokenCalls === 2, `expected a token call for W2S Watch, got ${tokenCalls}`);
  await expectCounter('w2sWatchRequests', 1);

  await expectRejects(
    async () => collectAsync(await api.watch(watchRequestWithItem('no'))),
    'invalid TypeScript W2S SSE request was accepted',
    /item_?id/i,
  );
  await expectCounter('w2sWatchRequests', 1);

  await expectRejects(
    async () => collectAsync(await api.watch(watchRequestWithItem('bad-event'))),
    'invalid TypeScript W2S SSE response was accepted',
  );
  await expectCounter('w2sWatchRequests', 2);

  const listed = await collectAsync(await api.listEvents(validRequest()));
  assert(listed.length === 1, `expected one W2S ListEvents event, got ${listed.length}`);
  assert(listed[0].eventId === 'event-1', 'unexpected W2S ListEvents event');
  await expectCounter('w2sListEventsRequests', 1);

  const noToken = new W2SServiceManager({ gatewayUrl: HTTP_URL });
  const noTokenApi = noToken.w2sClients().contractclient().contract_api_client();
  await expectRejects(
    () => noTokenApi.getContract(validRequest()),
    'auth-required TypeScript W2S Read without token provider succeeded',
    'token provider',
  );
  await expectCounter('w2sReadRequests', 1);

  const failingToken = new W2SServiceManager({ gatewayUrl: HTTP_URL }, async () => {
    throw new Error('identity unavailable');
  });
  const failingApi = failingToken.w2sClients().contractclient().contract_api_client();
  await expectRejects(
    () => failingApi.getContract(validRequest()),
    'auth-required TypeScript W2S Read with failing token provider succeeded',
    'identity unavailable',
  );
  await expectCounter('w2sReadRequests', 1);
}

async function runW2SUploadAndChatSmoke() {
  const validEvent = create(ContractEventSchema, {
    eventId: 'event-1',
    tenantNamespace: 'tenant.alpha',
    payload: 'ok',
  });

  const sm = new W2SServiceManager({ gatewayUrl: HTTP_URL }, async () => 'token');
  const api = sm.w2sClients().contractclient().contract_api_client();

  const uploadResp = await api.sendEvents([validEvent]);
  assert(uploadResp.resultId === 'sendEvents-1', 'unexpected W2S sendEvents result');
  assert(uploadResp.accepted === true, 'unexpected W2S sendEvents accepted flag');
  await expectCounter('w2sUploadConnections', 1);

  let opened;
  const openedPromise = new Promise((resolve) => { opened = resolve; });
  const chatMessages = [];
  let gotMessage;
  const messagePromise = new Promise((resolve) => { gotMessage = resolve; });
  const handle = api.assistEvents(
    (msg) => {
      chatMessages.push(msg);
      gotMessage();
    },
    { reconnect: false, onOpen: () => opened() },
  );
  await Promise.race([
    openedPromise,
    new Promise((_, reject) => setTimeout(() => reject(new Error('W2S assistEvents did not connect')), 10000)),
  ]);
  await handle.send(validEvent);
  await Promise.race([
    messagePromise,
    new Promise((_, reject) => setTimeout(() => reject(new Error('W2S assistEvents reply timed out')), 10000)),
  ]);
  handle.close();
  assert(chatMessages[0].eventId === 'event-echo', 'unexpected W2S assistEvents reply');
  await expectCounter('w2sChatConnections', 1);
}

// Audit: drive the fixture's ContractAuditApi (emitter + trace publisher wired
// server-side) through every gRPC method kind — success and failure each —
// then assert the recorded accounting and trace events via /fixture/stats.
// A hand-built generic grpc-js client keeps this leg focused on the SERVER
// seams; the generated client surfaces are covered by the C2S smoke above.
function auditServiceDefinition() {
  return {
    ExecuteAudit: {
      path: '/contractclient.ContractAuditApi/ExecuteAudit',
      requestStream: false,
      responseStream: false,
      requestSerialize: (msg) => Buffer.from(toBinary(AuditRequestSchema, msg)),
      requestDeserialize: (buf) => fromBinary(AuditRequestSchema, new Uint8Array(buf)),
      responseSerialize: (msg) => Buffer.from(toBinary(AuditResponseSchema, msg)),
      responseDeserialize: (buf) => fromBinary(AuditResponseSchema, new Uint8Array(buf)),
    },
    GenerateAuditEvents: {
      path: '/contractclient.ContractAuditApi/GenerateAuditEvents',
      requestStream: false,
      responseStream: true,
      requestSerialize: (msg) => Buffer.from(toBinary(AuditRequestSchema, msg)),
      requestDeserialize: (buf) => fromBinary(AuditRequestSchema, new Uint8Array(buf)),
      responseSerialize: (msg) => Buffer.from(toBinary(AuditEventSchema, msg)),
      responseDeserialize: (buf) => fromBinary(AuditEventSchema, new Uint8Array(buf)),
    },
    SendAuditEvents: {
      path: '/contractclient.ContractAuditApi/SendAuditEvents',
      requestStream: true,
      responseStream: false,
      requestSerialize: (msg) => Buffer.from(toBinary(AuditEventSchema, msg)),
      requestDeserialize: (buf) => fromBinary(AuditEventSchema, new Uint8Array(buf)),
      responseSerialize: (msg) => Buffer.from(toBinary(AuditResponseSchema, msg)),
      responseDeserialize: (buf) => fromBinary(AuditResponseSchema, new Uint8Array(buf)),
    },
    AssistAuditEvents: {
      path: '/contractclient.ContractAuditApi/AssistAuditEvents',
      requestStream: true,
      responseStream: true,
      requestSerialize: (msg) => Buffer.from(toBinary(AuditEventSchema, msg)),
      requestDeserialize: (buf) => fromBinary(AuditEventSchema, new Uint8Array(buf)),
      responseSerialize: (msg) => Buffer.from(toBinary(AuditEventSchema, msg)),
      responseDeserialize: (buf) => fromBinary(AuditEventSchema, new Uint8Array(buf)),
    },
  };
}

function callUnary(client, method, request) {
  return new Promise((resolve, reject) => {
    client[method](request, (err, response) => (err ? reject(err) : resolve(response)));
  });
}

function collectStream(stream) {
  return new Promise((resolve, reject) => {
    const items = [];
    const timer = setTimeout(() => reject(new Error('stream did not finish within timeout')), 10000);
    stream.on('data', (item) => items.push(item));
    stream.on('error', (err) => { clearTimeout(timer); reject(err); });
    stream.on('end', () => { clearTimeout(timer); resolve(items); });
  });
}

function callUpload(client, items) {
  return new Promise((resolve, reject) => {
    const call = client.SendAuditEvents((err, response) => (err ? reject(err) : resolve(response)));
    for (const item of items) {
      call.write(item);
    }
    call.end();
  });
}

async function callChat(client, items) {
  const call = client.AssistAuditEvents();
  const result = collectStream(call);
  for (const item of items) {
    call.write(item);
  }
  call.end();
  return await result;
}

async function runAuditAccountingTraceSmoke() {
  const AuditClient = grpc.makeGenericClientConstructor(auditServiceDefinition(), 'ContractAuditApi');
  const client = new AuditClient(GRPC_ADDR, grpc.credentials.createInsecure());
  try {
    const tracedSuccess = create(AuditRequestSchema, { itemId: 'audit-123', traceId: 'trace-success' });
    const unaryResp = await callUnary(client, 'ExecuteAudit', tracedSuccess);
    assert(unaryResp.resultId === 'audit-result', 'unexpected TypeScript audit unary result');
    await expectRejectsCode(
      () => callUnary(client, 'ExecuteAudit', create(AuditRequestSchema, { itemId: 'err', traceId: 'trace-error' })),
      grpc.status.INTERNAL,
      'TypeScript audit unary error was accepted',
    );

    const streamed = await collectStream(client.GenerateAuditEvents(create(AuditRequestSchema, { itemId: 'audit-123' })));
    assert(streamed.map((item) => item.eventId).join(',') === 'audit-event', 'unexpected TypeScript audit stream result');
    await expectRejectsCode(
      () => collectStream(client.GenerateAuditEvents(create(AuditRequestSchema, { itemId: 'err' }))),
      grpc.status.INTERNAL,
      'TypeScript audit stream error was accepted',
    );

    const uploadResp = await callUpload(client, [create(AuditEventSchema, { eventId: 'audit-event' })]);
    assert(uploadResp.resultId === 'audit-sendEvents', 'unexpected TypeScript audit sendEvents result');
    await expectRejectsCode(
      () => callUpload(client, [create(AuditEventSchema, { eventId: 'err' })]),
      grpc.status.INTERNAL,
      'TypeScript audit sendEvents error was accepted',
    );

    const chatted = await callChat(client, [create(AuditEventSchema, { eventId: 'audit-event' })]);
    assert(chatted.map((item) => item.eventId).join(',') === 'audit-reply', 'unexpected TypeScript audit assistEvents result');
    await expectRejectsCode(
      () => callChat(client, [create(AuditEventSchema, { eventId: 'err' })]),
      grpc.status.INTERNAL,
      'TypeScript audit assistEvents error was accepted',
    );
  } finally {
    client.close();
  }
}

async function assertAuditStats() {
  const stats = await fetchStats();
  const completed = stats.accountingCompleted ?? {};
  const errors = stats.accountingErrors ?? {};
  for (const method of [
    'CONTRACTCLIENT_RPCS_EXECUTE_AUDIT',
    'CONTRACTCLIENT_RPCS_GENERATE_AUDIT_EVENTS',
    'CONTRACTCLIENT_RPCS_SEND_AUDIT_EVENTS',
    'CONTRACTCLIENT_RPCS_ASSIST_AUDIT_EVENTS',
  ]) {
    assert(completed[method] === 1, `accounting completed[${method}] = ${completed[method]}, want 1`);
    assert(errors[method] === 1, `accounting errors[${method}] = ${errors[method]}, want 1`);
  }

  const traces = stats.traces ?? {};
  for (const [traceId, wantError] of [
    ['trace-success', ''],
    ['trace-error', 'audit unary failed'],
  ]) {
    const entry = traces[traceId];
    assert(entry, `missing trace ${traceId}`);
    const kinds = entry.kinds ?? [];
    assert(kinds.includes('TRACE_EVENT_KIND_INTERNAL_REQUEST'), `missing request trace for ${traceId}`);
    assert(kinds.includes('TRACE_EVENT_KIND_INTERNAL_RESPONSE'), `missing response trace for ${traceId}`);
    assert(entry.responseRpc === 'CONTRACTCLIENT_RPCS_EXECUTE_AUDIT', `trace rpc for ${traceId}: ${entry.responseRpc}`);
    if (wantError) {
      assert((entry.responseError ?? '').includes(wantError), `trace error for ${traceId}: ${entry.responseError}`);
    } else {
      assert((entry.responseError ?? '') === '', `trace error for ${traceId}: ${entry.responseError}`);
    }
  }
}

async function assertNoViolations() {
  const stats = await fetchStats();
  const violations = stats.violations ?? [];
  assert(violations.length === 0, `fixture observed wire-protocol violations: ${JSON.stringify(violations)}`);
}

const req = validRequest();
validateContractRequest(req);
validateContractResponse(create(ContractResponseSchema, { resultId: 'result-123', accepted: true }));

await runC2SRuntimeSmoke();
await runUnwiredAuthSmoke();
await runW2SHttpSseSmoke();
await runW2SUploadAndChatSmoke();
await runAuditAccountingTraceSmoke();
await assertAuditStats();
await assertNoViolations();
console.log('typescript runtime smoke ok');
process.exit(0);
