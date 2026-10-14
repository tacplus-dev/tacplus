# Copyright (c) 2026 Seabeam LLC
# SPDX-License-Identifier: Apache-2.0
#
# Runtime smoke: the generated python CLIENT surfaces against the live go
# fixture server (cross-test/runtime-smoke/fixture). Ports arrive via env:
#   TACPLUS_FIXTURE_GRPC          host:port of the auth-wired gRPC server
#   TACPLUS_FIXTURE_GRPC_UNWIRED  host:port of the fail-closed (no auth funcs) server
#   TACPLUS_FIXTURE_HTTP          base URL of the W2S HTTP/SSE/WebSocket server
#
# This is the client half of the pre-PAR-3 python `both` consumer runtime
# smokes (C2S gRPC, W2S HTTP/SSE, W2S upload, W2S chat, audit accounting +
# trace). Handler-reached / handler-not-reached assertions that used to read
# in-process counters now read the fixture's /fixture/stats endpoint. The
# WatchHub client-lib contract from the old consumer is dead for python
# (full-client builds emit no server-side collection runtime); its go
# equivalent lives in the consumer-contract go tests.

import asyncio
import os

import grpc
import grpc.aio
import httpx

from contractclient_model.c2sm.service_manager import ServiceManager as C2SServiceManager
from contractclient_model.types.contract.contractclient import contract_pb2, helpers
from contractclient_model.types.services.contractclient import contractaudit_pb2, contractaudit_pb2_grpc
from contractclient_model.w2sm.service_manager import ServiceManager as W2SServiceManager

GRPC_ADDR = os.environ["TACPLUS_FIXTURE_GRPC"]
GRPC_UNWIRED_ADDR = os.environ["TACPLUS_FIXTURE_GRPC_UNWIRED"]
HTTP_URL = os.environ["TACPLUS_FIXTURE_HTTP"].rstrip("/")


def valid_request():
    return contract_pb2.ContractRequest(
        tenant_namespace="tenant.alpha",
        item_id="item-123",
        mode=contract_pb2.CONTRACT_MODE_READ,
    )


def invalid_request():
    return contract_pb2.ContractRequest(
        tenant_namespace="tenant.alpha",
        item_id="no",
        mode=contract_pb2.CONTRACT_MODE_READ,
    )


def forbidden_request():
    return contract_pb2.ContractRequest(
        tenant_namespace="tenant.beta",
        item_id="item-123",
        mode=contract_pb2.CONTRACT_MODE_READ,
    )


def valid_watch_request():
    return contract_pb2.ContractWatchRequest(
        tenant_namespace="tenant.alpha",
        item_id="item-123",
        mode=contract_pb2.CONTRACT_MODE_READ,
        from_revision=0,
    )


def invalid_watch_request():
    return contract_pb2.ContractWatchRequest(
        tenant_namespace="tenant.alpha",
        item_id="no",
        mode=contract_pb2.CONTRACT_MODE_READ,
    )


def valid_event():
    return contract_pb2.ContractEvent(
        event_id="event-1",
        tenant_namespace="tenant.alpha",
        payload="ok",
    )


async def fetch_stats():
    async with httpx.AsyncClient() as client:
        resp = await client.get(f"{HTTP_URL}/fixture/stats", timeout=10)
        resp.raise_for_status()
        return resp.json()


async def expect_counter(name, want):
    stats = await fetch_stats()
    got = stats.get(name)
    assert got == want, f"fixture stats {name} = {got}, want {want}"


async def expect_grpc_code(awaitable, code, message):
    try:
        await awaitable
        raise AssertionError(message)
    except grpc.aio.AioRpcError as exc:
        if exc.code() != code:
            raise AssertionError(f"{message}: expected {code}, got {exc.code()}: {exc}") from exc


async def expect_grpc_stream_code(awaitable, code, message):
    try:
        stream = await awaitable
        async for _ in stream:
            pass
        raise AssertionError(message)
    except grpc.aio.AioRpcError as exc:
        if exc.code() != code:
            raise AssertionError(f"{message}: expected {code}, got {exc.code()}: {exc}") from exc


async def expect_local_failure(awaitable, message, needle):
    """A client-LOCAL failure: not an AioRpcError (never hit the wire)."""
    try:
        await awaitable
        raise AssertionError(message)
    except AssertionError:
        raise
    except grpc.aio.AioRpcError as exc:
        raise AssertionError(f"{message}: reached the wire: {exc}") from exc
    except Exception as exc:
        if needle not in str(exc):
            raise AssertionError(f"{message}: unexpected local failure: {exc!r}") from exc


async def expect_local_stream_failure(fn, message, needle):
    async def drain():
        stream = await fn()
        async for _ in stream:
            pass

    await expect_local_failure(drain(), message, needle)


async def run_c2s_runtime_smoke() -> None:
    channel = grpc.aio.insecure_channel(GRPC_ADDR)
    try:
        await asyncio.wait_for(channel.channel_ready(), timeout=10)
        c2s = C2SServiceManager(channel)
        c2s_auth = C2SServiceManager(channel, token_provider=lambda: "token")
        c2s_failing_token = C2SServiceManager(
            channel,
            token_provider=lambda: (_ for _ in ()).throw(RuntimeError("identity unavailable")),
        )
        client = c2s.grpc_clients.contractclient.contract_api_client
        client_auth = c2s_auth.grpc_clients.contractclient.contract_api_client
        client_failing = c2s_failing_token.grpc_clients.contractclient.contract_api_client

        await expect_local_failure(
            client.get_contract(valid_request()),
            "protected python C2S Read without token provider was accepted",
            "auth token provider is required",
        )
        await expect_counter("readCalls", 0)
        await expect_counter("authorizeCalls", 0)

        resp = await client_auth.get_contract(valid_request())
        assert resp.result_id == "result-123", f"unexpected C2S Read result: {resp}"
        assert resp.accepted
        await expect_counter("readCalls", 1)

        await expect_grpc_code(
            client_auth.get_contract(forbidden_request()),
            grpc.StatusCode.PERMISSION_DENIED,
            "namespace-mismatched python C2S Read was accepted",
        )
        await expect_counter("readCalls", 1)

        await expect_local_failure(
            client_failing.get_contract(valid_request()),
            "protected python C2S Read with failing token provider was accepted",
            "identity unavailable",
        )
        await expect_counter("readCalls", 1)

        resp = await client.get_public_contract(valid_request())
        assert resp.result_id == "result-123"
        assert resp.accepted
        await expect_counter("publicReadCalls", 1)

        resp = await client_failing.get_public_contract(valid_request())
        assert resp.result_id == "result-123"
        assert resp.accepted
        await expect_counter("publicReadCalls", 2)

        await expect_local_failure(
            client.get_public_contract(invalid_request()),
            "invalid python C2S request was accepted",
            "item_id",
        )
        await expect_counter("publicReadCalls", 2)

        bad_resp_req = valid_request()
        bad_resp_req.item_id = "bad-response"
        await expect_grpc_code(
            client.get_public_contract(bad_resp_req),
            grpc.StatusCode.INTERNAL,
            "invalid python C2S response was accepted",
        )
        await expect_counter("publicReadCalls", 3)

        await expect_local_stream_failure(
            lambda: client.watch(valid_watch_request()),
            "auth-any-user python C2S Watch without token was accepted",
            "auth token provider is required",
        )
        await expect_counter("watchCalls", 0)
        await expect_counter("authenticateCalls", 0)

        watch = await client_auth.watch(valid_watch_request())
        events = [event async for event in watch]
        assert len(events) == 2, f"expected snapshot_item + snapshot_done, got {events}"
        assert events[0].snapshot_item.event_id == "event-1", f"unexpected watch event: {events[0]}"
        assert events[1].snapshot_done == 1, f"unexpected watch completion: {events[1]}"
        await expect_counter("watchCalls", 1)

        await expect_local_stream_failure(
            lambda: client_auth.watch(invalid_watch_request()),
            "invalid python C2S Watch request was accepted",
            "item_id",
        )
        await expect_counter("watchCalls", 1)

        bad_watch_req = valid_watch_request()
        bad_watch_req.item_id = "bad-event"
        await expect_grpc_stream_code(
            client_auth.watch(bad_watch_req),
            grpc.StatusCode.INTERNAL,
            "invalid python C2S Watch response was accepted",
        )
        await expect_counter("watchCalls", 2)

        await expect_local_stream_failure(
            lambda: client.list_events(valid_request()),
            "auth-any-user python C2S ListEvents without token was accepted",
            "auth token provider is required",
        )
        await expect_counter("listEventsCalls", 0)

        listed = await client_auth.list_events(valid_request())
        list_items = [event async for event in listed]
        assert len(list_items) == 1, f"expected one ListEvents item, got {list_items}"
        assert list_items[0].event_id == "event-1"
        assert list_items[0].tenant_namespace == "tenant.alpha"
        await expect_counter("listEventsCalls", 1)

        await expect_local_stream_failure(
            lambda: client_auth.list_events(invalid_request()),
            "invalid python C2S ListEvents request was accepted",
            "item_id",
        )
        await expect_counter("listEventsCalls", 1)

        bad_event_req = valid_request()
        bad_event_req.item_id = "bad-event"
        await expect_grpc_stream_code(
            client_auth.list_events(bad_event_req),
            grpc.StatusCode.INTERNAL,
            "invalid python C2S ListEvents response was accepted",
        )
        await expect_counter("listEventsCalls", 2)

        # PAGE auto-pager: follows next_page_token until exhausted. The fixture
        # returns TWO keyset pages, so the pager must make a second fetch with
        # the NON-EMPTY continuation token before terminating (PC-C-3).
        page_req = contract_pb2.ContractPageRequest(tenant_namespace="tenant.alpha", page_size=50)
        paged_items = [item async for item in client_auth.list_events_paged(page_req)]
        assert len(paged_items) == 2, f"expected two paged items across two pages, got {paged_items}"
        assert paged_items[0].event_id == "event-1"
        assert paged_items[1].event_id == "event-2"
        await expect_counter("pagedCalls", 2)
    finally:
        await channel.close()


async def run_unwired_auth_smoke() -> None:
    # The go wrapper is fail-closed: a mandatory-auth method with no wired
    # authorizer rejects with a plain "authorizer not configured" error, which
    # gRPC surfaces as UNKNOWN. The handler must never be reached.
    channel = grpc.aio.insecure_channel(GRPC_UNWIRED_ADDR)
    try:
        await asyncio.wait_for(channel.channel_ready(), timeout=10)
        c2s = C2SServiceManager(channel, token_provider=lambda: "token")
        client = c2s.grpc_clients.contractclient.contract_api_client
        try:
            await client.get_contract(valid_request())
            raise AssertionError("unwired python mandatory-auth wrapper did not fail closed")
        except grpc.aio.AioRpcError as exc:
            if exc.code() != grpc.StatusCode.UNKNOWN or "authorizer not configured" not in exc.details():
                raise AssertionError(f"unexpected unwired-auth failure: {exc}") from exc
        await expect_counter("unwiredHandlerCalls", 0)
    finally:
        await channel.close()


async def run_w2s_http_sse_smoke() -> None:
    token_calls = 0

    def token_provider():
        nonlocal token_calls
        token_calls += 1
        return "token"

    sm = W2SServiceManager.with_token_provider(HTTP_URL, token_provider)
    try:
        client = sm.w2s_clients.contractclient.contract_api_client

        public_resp = await client.get_public_contract(valid_request())
        assert public_resp.result_id == "result-123"
        assert public_resp.accepted
        assert token_calls == 0, "auth-disabled W2S request asked for a token"
        await expect_counter("w2sPublicReadRequests", 1)

        await expect_local_failure(
            client.get_public_contract(invalid_request()),
            "invalid python W2S request was accepted",
            "item_id",
        )
        await expect_counter("w2sPublicReadRequests", 1)

        bad_resp_req = valid_request()
        bad_resp_req.item_id = "bad-response"
        try:
            await client.get_public_contract(bad_resp_req)
            raise AssertionError("invalid python W2S response was accepted")
        except AssertionError:
            raise
        except Exception as exc:
            if "result_id" not in str(exc):
                raise AssertionError(f"unexpected bad-response failure: {exc!r}") from exc
        await expect_counter("w2sPublicReadRequests", 2)

        resp = await client.get_contract(valid_request())
        assert resp.result_id == "result-123"
        assert token_calls == 1, f"expected one token call for W2S Read, got {token_calls}"
        await expect_counter("w2sReadRequests", 1)

        seen = [event async for event in client.watch(valid_watch_request())]
        assert len(seen) == 2, f"expected snapshot_item + snapshot_done over SSE, got {seen}"
        assert seen[0].snapshot_item.event_id == "event-1"
        assert seen[1].snapshot_done == 1
        assert token_calls == 2, f"expected a token call for W2S Watch, got {token_calls}"
        await expect_counter("w2sWatchRequests", 1)

        async def drain_watch(req):
            async for _ in client.watch(req):
                pass

        await expect_local_failure(
            drain_watch(invalid_watch_request()),
            "invalid python W2S SSE request was accepted",
            "item_id",
        )
        await expect_counter("w2sWatchRequests", 1)

        bad_event_req = valid_watch_request()
        bad_event_req.item_id = "bad-event"
        try:
            await drain_watch(bad_event_req)
            raise AssertionError("invalid python W2S SSE response was accepted")
        except AssertionError:
            raise
        except Exception as exc:
            if "event_id" not in str(exc):
                raise AssertionError(f"unexpected bad-event SSE failure: {exc!r}") from exc
        await expect_counter("w2sWatchRequests", 2)

        listed = [event async for event in client.list_events(valid_request())]
        assert len(listed) == 1, f"expected one W2S ListEvents event, got {listed}"
        assert listed[0].event_id == "event-1"
        await expect_counter("w2sListEventsRequests", 1)

        no_token = W2SServiceManager.with_token_provider(HTTP_URL, None)
        try:
            try:
                await no_token.w2s_clients.contractclient.contract_api_client.get_contract(valid_request())
                raise AssertionError("auth-required python W2S Read without token provider succeeded")
            except PermissionError:
                pass
            await expect_counter("w2sReadRequests", 1)
        finally:
            await no_token.close()

        failing_token = W2SServiceManager.with_token_provider(
            HTTP_URL,
            lambda: (_ for _ in ()).throw(RuntimeError("identity unavailable")),
        )
        try:
            await expect_local_failure(
                failing_token.w2s_clients.contractclient.contract_api_client.get_contract(valid_request()),
                "auth-required python W2S Read with failing token provider succeeded",
                "identity unavailable",
            )
            await expect_counter("w2sReadRequests", 1)
        finally:
            await failing_token.close()
    finally:
        await sm.close()


async def run_w2s_upload_smoke() -> None:
    sm = W2SServiceManager.with_token_provider(HTTP_URL, lambda: "token")
    try:
        client = sm.w2s_clients.contractclient.contract_api_client
        resp = await client.send_events([valid_event()])
        assert resp.result_id == "sendEvents-1", f"unexpected W2S sendEvents response: {resp}"
        assert resp.accepted
        await expect_counter("w2sUploadConnections", 1)
    finally:
        await sm.close()


async def run_w2s_chat_smoke() -> None:
    sm = W2SServiceManager.with_token_provider(HTTP_URL, lambda: "token")
    received = []
    connected = asyncio.Event()
    got_message = asyncio.Event()

    async def on_connect(handle):
        connected.set()

    async def on_message(event):
        received.append(event)
        got_message.set()

    try:
        client = sm.w2s_clients.contractclient.contract_api_client
        handle = client.assist_events(on_connect, on_message)
        await asyncio.wait_for(connected.wait(), timeout=10)
        await handle.send(valid_event())
        await asyncio.wait_for(got_message.wait(), timeout=10)
        await handle.finish()
        assert received[0].event_id == "event-echo", f"unexpected W2S assistEvents reply: {received[0]}"
        await expect_counter("w2sChatConnections", 1)
    finally:
        await sm.close()


def run_audit_accounting_trace_smoke() -> None:
    # Drive the fixture's ContractAuditApi (emitter + trace publisher wired
    # server-side) through every gRPC method kind — success and failure each —
    # then assert the recorded accounting and trace events via /fixture/stats.
    # Raw generated stubs (not the c2sm client) keep this leg focused on the
    # SERVER seams; the client surfaces are covered by the C2S smoke above.
    channel = grpc.insecure_channel(GRPC_ADDR)
    try:
        grpc.channel_ready_future(channel).result(timeout=10)
        client = contractaudit_pb2_grpc.ContractAuditApiStub(channel)

        resp = client.ExecuteAudit(
            contractaudit_pb2.AuditRequest(item_id="audit-123", trace_id="trace-success"), timeout=10
        )
        assert resp.result_id == "audit-result", f"unexpected audit unary result: {resp}"
        try:
            client.ExecuteAudit(
                contractaudit_pb2.AuditRequest(item_id="err", trace_id="trace-error"), timeout=10
            )
            raise AssertionError("python audit unary error was accepted")
        except grpc.RpcError as exc:
            assert exc.code() == grpc.StatusCode.INTERNAL, f"audit unary error code: {exc}"

        events = list(client.GenerateAuditEvents(contractaudit_pb2.AuditRequest(item_id="audit-123"), timeout=10))
        assert [event.event_id for event in events] == ["audit-event"]
        try:
            list(client.GenerateAuditEvents(contractaudit_pb2.AuditRequest(item_id="err"), timeout=10))
            raise AssertionError("python audit stream error was accepted")
        except grpc.RpcError as exc:
            assert exc.code() == grpc.StatusCode.INTERNAL, f"audit stream error code: {exc}"

        resp = client.SendAuditEvents(iter([contractaudit_pb2.AuditEvent(event_id="audit-event")]), timeout=10)
        assert resp.result_id == "audit-sendEvents"
        try:
            client.SendAuditEvents(iter([contractaudit_pb2.AuditEvent(event_id="err")]), timeout=10)
            raise AssertionError("python audit sendEvents error was accepted")
        except grpc.RpcError as exc:
            assert exc.code() == grpc.StatusCode.INTERNAL, f"audit sendEvents error code: {exc}"

        replies = list(client.AssistAuditEvents(iter([contractaudit_pb2.AuditEvent(event_id="audit-event")]), timeout=10))
        assert [event.event_id for event in replies] == ["audit-reply"]
        try:
            list(client.AssistAuditEvents(iter([contractaudit_pb2.AuditEvent(event_id="err")]), timeout=10))
            raise AssertionError("python audit assistEvents error was accepted")
        except grpc.RpcError as exc:
            assert exc.code() == grpc.StatusCode.INTERNAL, f"audit assistEvents error code: {exc}"
    finally:
        channel.close()


async def assert_audit_stats() -> None:
    stats = await fetch_stats()
    completed = stats.get("accountingCompleted") or {}
    errors = stats.get("accountingErrors") or {}
    for method in (
        "CONTRACTCLIENT_RPCS_EXECUTE_AUDIT",
        "CONTRACTCLIENT_RPCS_GENERATE_AUDIT_EVENTS",
        "CONTRACTCLIENT_RPCS_SEND_AUDIT_EVENTS",
        "CONTRACTCLIENT_RPCS_ASSIST_AUDIT_EVENTS",
    ):
        assert completed.get(method) == 1, f"accounting completed[{method}] = {completed.get(method)}, want 1; all={completed}"
        assert errors.get(method) == 1, f"accounting errors[{method}] = {errors.get(method)}, want 1; all={errors}"

    traces = stats.get("traces") or {}
    for trace_id, want_error in (("trace-success", ""), ("trace-error", "audit unary failed")):
        entry = traces.get(trace_id)
        assert entry is not None, f"missing trace {trace_id}: {traces}"
        kinds = entry.get("kinds") or []
        assert "TRACE_EVENT_KIND_INTERNAL_REQUEST" in kinds, f"missing request trace for {trace_id}: {entry}"
        assert "TRACE_EVENT_KIND_INTERNAL_RESPONSE" in kinds, f"missing response trace for {trace_id}: {entry}"
        assert entry.get("responseRpc") == "CONTRACTCLIENT_RPCS_EXECUTE_AUDIT", f"trace rpc for {trace_id}: {entry}"
        if want_error:
            assert want_error in (entry.get("responseError") or ""), f"trace error for {trace_id}: {entry}"
        else:
            assert (entry.get("responseError") or "") == "", f"trace error for {trace_id}: {entry}"


async def assert_no_violations() -> None:
    stats = await fetch_stats()
    violations = stats.get("violations") or []
    assert not violations, f"fixture observed wire-protocol violations: {violations}"


async def main() -> None:
    helpers.validate_contract_request(valid_request())
    helpers.validate_contract_response(
        contract_pb2.ContractResponse(result_id="result-123", accepted=True)
    )

    await run_c2s_runtime_smoke()
    await run_unwired_auth_smoke()
    await run_w2s_http_sse_smoke()
    await run_w2s_upload_smoke()
    await run_w2s_chat_smoke()
    run_audit_accounting_trace_smoke()
    await assert_audit_stats()
    await assert_no_violations()
    print("python runtime smoke ok")


asyncio.run(main())
