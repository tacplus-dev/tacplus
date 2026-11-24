# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# The Python leg of cross-test/rotation-clients (tacplus#323): the generated
# Python C2S client (c2sm, grpc.aio) against the generated Go server, run by
# the Go fixture's TestRotationClients. The callers below are plain caller
# code: nothing in them mentions rotation. Each stream must outlive several
# token lifetimes with no loss, duplication or reordering, on one handler call
# with one context.
#
# Env: ROTATION_ADDR (the server, with the sibling), ROTATION_PLAIN_ADDR (the
# plain service only), ROTATION_CONTROL (tokens and the handler's ends),
# ROTATION_TOKEN_LIFETIME_MS, PY_C2SM and PY_PB (the generated modules).

import asyncio
import importlib
import json
import os
import time
import traceback
import urllib.parse
import urllib.request

import grpc
import grpc.aio

ServiceManager = importlib.import_module(os.environ["PY_C2SM"]).ServiceManager
pb = importlib.import_module(os.environ["PY_PB"])

ADDR = os.environ["ROTATION_ADDR"]
PLAIN_ADDR = os.environ["ROTATION_PLAIN_ADDR"]
CONTROL = os.environ["ROTATION_CONTROL"]
LIFETIME = int(os.environ["ROTATION_TOKEN_LIFETIME_MS"]) / 1000
# Several token lifetimes.
STREAM_FOR = 3 * LIFETIME


def check_sequence(what, got, n):
    assert len(got) == n, f"{what}: got {len(got)} messages, want {n} (loss or duplication)"
    for i, seq in enumerate(got):
        assert seq == i + 1, f"{what}: message {i} is seq {seq}, want {i + 1} (reordering, loss or duplication)"


def _get(path):
    with urllib.request.urlopen(f"{CONTROL}{path}", timeout=10) as resp:
        return resp.read().decode("utf-8")


async def control(path):
    return await asyncio.to_thread(_get, path)


class TokenSource:
    """The caller's token provider: alice's tokens, until revoked (the mint
    fails) or impersonating (another subject's)."""

    def __init__(self):
        self.subject = "alice@example.com"
        self.revoked = False

    async def __call__(self):
        if self.revoked:
            raise RuntimeError("identity: caller revoked")
        return await control("/token?sub=" + urllib.parse.quote(self.subject))


class Client:
    def __init__(self, addr, src):
        self.channel = grpc.aio.insecure_channel(addr)
        self.sm = ServiceManager(self.channel, token_provider=src)
        self.api = self.sm.grpc_clients.rotationclients.viewer_service_client

    async def close(self):
        await self.channel.close()


async def handler_end(method, within=10.0):
    deadline = time.monotonic() + within
    while True:
        ends = json.loads(await control("/ends"))
        if ends:
            assert len(ends) == 1, f"{method}: {len(ends)} handler calls, want one: {ends}"
            assert ends[0]["method"] == method, f"handler end is {ends[0]['method']}, want {method}"
            return ends[0]
        assert time.monotonic() < deadline, f"{method}: the handler did not return within {within}s"
        await asyncio.sleep(0.05)


async def mints():
    return int(await control("/mints"))


def tick(seq, m):
    return pb.Tick(seq=seq, m=m)


async def bidi_both_ways():
    """The bidi stream both ways at once across several lifetimes."""
    c = Client(ADDR, TokenSource())
    before = await mints()
    sent = 0

    async def ticks():
        nonlocal sent
        start = time.monotonic()
        while time.monotonic() - start < STREAM_FOR:
            sent += 1
            yield tick(sent, "up")
            await asyncio.sleep(0.002)

    echoed = [m.seq async for m in await c.api.assist_ticks(ticks())]
    check_sequence("bidi echoed", echoed, sent)
    end = await handler_end("AssistTicks")
    assert end["sameContext"], "bidi: the handler saw more than one context"
    assert end["received"] == sent and end["inOrder"], f"bidi: the handler received {end}"
    assert end["subject"] == "alice@example.com", f"bidi: the handler's caller is {end['subject']}"
    renewed = await mints() - before
    assert renewed >= 3, f"bidi: {renewed} tokens minted, want several: the stream did not rotate"
    await c.close()
    print(f"bidi: {sent} ticks both ways across {renewed} tokens, one handler call")


async def server_stream():
    """A server stream across several lifetimes, ended OK by the handler."""
    c = Client(ADDR, TokenSource())
    want = int(STREAM_FOR / 0.05)
    got = [m.seq async for m in await c.api.generate_ticks(tick(want, "down"))]
    check_sequence("server stream", got, want)
    end = await handler_end("GenerateTicks")
    assert end["code"] == "OK" and end["sameContext"], f"server stream: handler end {end}"
    await c.close()
    print(f"server stream: {want} ticks across lifetimes, one handler call")


async def client_stream():
    """A client stream across several lifetimes: one answer counting every tick."""
    c = Client(ADDR, TokenSource())
    sent = 0

    async def ticks():
        nonlocal sent
        start = time.monotonic()
        while time.monotonic() - start < STREAM_FOR:
            sent += 1
            yield tick(sent, "up")
            await asyncio.sleep(0.005)

    resp = await c.api.send_ticks(ticks())
    assert resp.seq == sent and resp.m == "in-order", f"client stream: the handler answered {resp.seq} {resp.m}, want {sent} in-order"
    end = await handler_end("SendTicks")
    assert end["sameContext"], "client stream: the handler saw more than one context"
    await c.close()
    print(f"client stream: {sent} ticks across lifetimes, one handler call")


async def stream_error(stream):
    opened = time.monotonic()
    ticks = 0
    try:
        async for _ in stream:
            ticks += 1
    except grpc.RpcError as err:
        return err.code(), time.monotonic() - opened, ticks
    return grpc.StatusCode.OK, time.monotonic() - opened, ticks


async def another_subject_refused():
    """A rotation with another subject's token is refused: PERMISSION_DENIED."""
    src = TokenSource()
    c = Client(ADDR, src)
    stream = await c.api.generate_ticks(tick(0, "down"))
    src.subject = "mallory@example.com"
    code, took, ticks = await stream_error(stream)
    assert ticks > 0, "another subject: no tick arrived before the refusal"
    assert code == grpc.StatusCode.PERMISSION_DENIED, f"another subject: the stream ended with {code}, want PERMISSION_DENIED"
    assert took < 2 * LIFETIME + 3, f"another subject: the stream lasted {took:.1f}s"
    end = await handler_end("GenerateTicks")
    assert end["cancelled"] and end["subject"] == "alice@example.com", f"another subject: handler end {end}"
    await c.close()
    print(f"another subject: refused PERMISSION_DENIED after {took:.1f}s")


async def revoked_cut_off():
    """A revoked caller is cut off within one lifetime: UNAUTHENTICATED."""
    src = TokenSource()
    c = Client(ADDR, src)
    stream = await c.api.generate_ticks(tick(0, "down"))
    src.revoked = True
    code, took, _ = await stream_error(stream)
    assert code == grpc.StatusCode.UNAUTHENTICATED, f"revoked: the stream ended with {code}, want UNAUTHENTICATED"
    assert took < LIFETIME + 3, f"revoked: the stream lasted {took:.1f}s, want it cut off within one lifetime"
    end = await handler_end("GenerateTicks")
    assert end["cancelled"], f"revoked: the handler's context was not cancelled: {end}"
    await c.close()
    print(f"revoked: cut off UNAUTHENTICATED after {took:.1f}s")


def step(name):
    """Mark a step of a leg (`--- <leg>/<step>`): run.sh names the last one reached."""
    print(f"--- {name}", flush=True)


async def falls_back_to_plain():
    """Against a server without the sibling: the plain method, ended at exp."""
    c = Client(PLAIN_ADDR, TokenSource())
    step("falls_back_to_plain/open")
    stream = await c.api.generate_ticks(tick(0, "down"))
    step("falls_back_to_plain/read")
    code, took, ticks = await stream_error(stream)
    assert ticks > 0, f"fallback: no tick arrived on the plain method ({code})"
    assert code == grpc.StatusCode.UNAUTHENTICATED, f"fallback: the plain stream ended with {code}, want UNAUTHENTICATED at exp"
    assert took < LIFETIME + 3, f"fallback: the plain stream lasted {took:.1f}s"
    step("falls_back_to_plain/end")
    await handler_end("GenerateTicks")
    step("falls_back_to_plain/close")
    await c.close()
    print(f"fallback: the plain method, ended at exp after {took:.1f}s", flush=True)


# A leg lasts a few token lifetimes; one that outlives this hangs.
LEG_TIMEOUT = 60.0


def leg_error(err):
    """One short line for run.sh, which quotes it in the build's verdict."""
    if isinstance(err, grpc.RpcError):
        return f"LegError: {err.code().name} {err.details()}"
    if isinstance(err, asyncio.TimeoutError):
        return f"LegError: hung for {LEG_TIMEOUT:.0f}s"
    return f"LegError: {type(err).__name__} {err}"


async def main():
    for leg in (bidi_both_ways, server_stream, client_stream, another_subject_refused, revoked_cut_off, falls_back_to_plain):
        step(leg.__name__)
        try:
            await asyncio.wait_for(leg(), LEG_TIMEOUT)
        except Exception as err:  # noqa: BLE001 - every failure ends the leg with one line
            traceback.print_exc()
            raise SystemExit(leg_error(err)) from None
    step("done")
    print("python rotation legs OK", flush=True)


asyncio.run(main())
