// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// The C# leg of cross-test/rotation-clients (tacplus#323): the generated C#
// C2SClient (Grpc.Net) against the generated Go server, run by the Go
// fixture's TestRotationClients. The callers below are plain caller code:
// nothing in them mentions rotation. Each stream must outlive several token
// lifetimes with no loss, duplication or reordering, on one handler call with
// one context.
//
// Env: ROTATION_ADDR (the server, with the sibling), ROTATION_PLAIN_ADDR (the
// plain service only), ROTATION_CONTROL (tokens and the handler's ends),
// ROTATION_TOKEN_LIFETIME_MS.

using System.Diagnostics;
using System.Text.Json;
using Grpc.Core;
using Rotationclients;
using RotationclientsModel;

var addr = Environment.GetEnvironmentVariable("ROTATION_ADDR")!;
var plainAddr = Environment.GetEnvironmentVariable("ROTATION_PLAIN_ADDR")!;
var control = Environment.GetEnvironmentVariable("ROTATION_CONTROL")!;
var lifetime = TimeSpan.FromMilliseconds(int.Parse(Environment.GetEnvironmentVariable("ROTATION_TOKEN_LIFETIME_MS")!));
// Several token lifetimes.
var streamFor = lifetime * 3;
var http = new HttpClient { Timeout = TimeSpan.FromSeconds(10) };

void Assert(bool condition, string message)
{
    if (!condition)
    {
        throw new Exception(message);
    }
}

void CheckSequence(string what, List<long> got, long n)
{
    Assert(got.Count == n, $"{what}: got {got.Count} messages, want {n} (loss or duplication)");
    for (var i = 0; i < got.Count; i++)
    {
        Assert(got[i] == i + 1, $"{what}: message {i} is seq {got[i]}, want {i + 1} (reordering, loss or duplication)");
    }
}

async Task<string> Control(string path) => await http.GetStringAsync(control + path);

async Task<JsonElement> HandlerEnd(string method)
{
    var deadline = DateTime.UtcNow + TimeSpan.FromSeconds(10);
    while (true)
    {
        using var doc = JsonDocument.Parse(await Control("/ends"));
        var ends = doc.RootElement;
        if (ends.GetArrayLength() > 0)
        {
            Assert(ends.GetArrayLength() == 1, $"{method}: {ends.GetArrayLength()} handler calls, want one: {ends}");
            var end = ends[0].Clone();
            Assert(end.GetProperty("method").GetString() == method, $"handler end is {end.GetProperty("method")}, want {method}");
            return end;
        }
        Assert(DateTime.UtcNow < deadline, $"{method}: the handler did not return within 10s");
        await Task.Delay(50);
    }
}

async Task<int> Mints() => int.Parse(await Control("/mints"));

// The caller's token provider: alice's tokens, until revoked (the mint
// fails) or impersonating (another subject's).
var subject = "alice@example.com";
var revoked = false;
async Task<string> Tokens(CancellationToken ct)
{
    if (revoked)
    {
        throw new InvalidOperationException("identity: caller revoked");
    }
    return await http.GetStringAsync($"{control}/token?sub={Uri.EscapeDataString(subject)}", ct);
}

C2SClient Client(string target)
{
    var colon = target.LastIndexOf(':');
    return new C2SClient(target[..colon], int.Parse(target[(colon + 1)..]), useTls: false, tokenProvider: Tokens);
}

Tick NewTick(long seq, string m) => new Tick { Seq = seq, M = m };

async Task<(StatusCode Code, TimeSpan Took, int Ticks)> StreamError(IAsyncStreamReader<Tick> stream)
{
    var opened = Stopwatch.StartNew();
    var ticks = 0;
    try
    {
        while (await stream.MoveNext(CancellationToken.None))
        {
            ticks++;
        }
        return (StatusCode.OK, opened.Elapsed, ticks);
    }
    catch (RpcException err)
    {
        return (err.StatusCode, opened.Elapsed, ticks);
    }
}

// The bidi stream both ways at once across several lifetimes.
async Task BidiBothWays()
{
    subject = "alice@example.com";
    using var client = Client(addr);
    var before = await Mints();
    using var call = client.WithViewerService().AssistTicks();
    var echoed = new List<long>();
    var reader = Task.Run(async () =>
    {
        while (await call.ResponseStream.MoveNext(CancellationToken.None))
        {
            echoed.Add(call.ResponseStream.Current.Seq);
        }
    });
    long n = 0;
    var start = Stopwatch.StartNew();
    while (start.Elapsed < streamFor)
    {
        n++;
        await call.RequestStream.WriteAsync(NewTick(n, "up"));
        await Task.Delay(2);
    }
    await call.RequestStream.CompleteAsync();
    await reader;
    CheckSequence("bidi echoed", echoed, n);
    var end = await HandlerEnd("AssistTicks");
    Assert(end.GetProperty("sameContext").GetBoolean(), "bidi: the handler saw more than one context");
    Assert(end.GetProperty("received").GetInt64() == n && end.GetProperty("inOrder").GetBoolean(), $"bidi: the handler received {end}");
    Assert(end.GetProperty("subject").GetString() == "alice@example.com", $"bidi: the handler's caller is {end.GetProperty("subject")}");
    var renewed = await Mints() - before;
    Assert(renewed >= 3, $"bidi: {renewed} tokens minted, want several: the stream did not rotate");
    Console.WriteLine($"bidi: {n} ticks both ways across {renewed} tokens, one handler call");
}

// A server stream across several lifetimes, ended OK by the handler.
async Task ServerStream()
{
    using var client = Client(addr);
    var want = (long)(streamFor / TimeSpan.FromMilliseconds(50));
    using var call = client.WithViewerService().GenerateTicks(NewTick(want, "down"));
    var got = new List<long>();
    while (await call.ResponseStream.MoveNext(CancellationToken.None))
    {
        got.Add(call.ResponseStream.Current.Seq);
    }
    CheckSequence("server stream", got, want);
    var end = await HandlerEnd("GenerateTicks");
    Assert(end.GetProperty("code").GetString() == "OK" && end.GetProperty("sameContext").GetBoolean(), $"server stream: handler end {end}");
    Console.WriteLine($"server stream: {want} ticks across lifetimes, one handler call");
}

// A client stream across several lifetimes: one answer counting every tick.
async Task ClientStream()
{
    using var client = Client(addr);
    using var call = client.WithViewerService().SendTicks();
    long n = 0;
    var start = Stopwatch.StartNew();
    while (start.Elapsed < streamFor)
    {
        n++;
        await call.RequestStream.WriteAsync(NewTick(n, "up"));
        await Task.Delay(5);
    }
    await call.RequestStream.CompleteAsync();
    var resp = await call.ResponseAsync;
    Assert(resp.Seq == n && resp.M == "in-order", $"client stream: the handler answered {resp.Seq} {resp.M}, want {n} in-order");
    var end = await HandlerEnd("SendTicks");
    Assert(end.GetProperty("sameContext").GetBoolean(), "client stream: the handler saw more than one context");
    Console.WriteLine($"client stream: {n} ticks across lifetimes, one handler call");
}

// A rotation with another subject's token is refused: PERMISSION_DENIED.
async Task AnotherSubjectRefused()
{
    using var client = Client(addr);
    using var call = client.WithViewerService().GenerateTicks(NewTick(0, "down"));
    Assert(await call.ResponseStream.MoveNext(CancellationToken.None), "another subject: no tick arrived before the refusal");
    subject = "mallory@example.com";
    var (code, took, _) = await StreamError(call.ResponseStream);
    subject = "alice@example.com";
    Assert(code == StatusCode.PermissionDenied, $"another subject: the stream ended with {code}, want PermissionDenied");
    Assert(took < lifetime * 2 + TimeSpan.FromSeconds(3), $"another subject: the stream lasted {took}");
    var end = await HandlerEnd("GenerateTicks");
    Assert(end.GetProperty("cancelled").GetBoolean() && end.GetProperty("subject").GetString() == "alice@example.com", $"another subject: handler end {end}");
    Console.WriteLine($"another subject: refused PermissionDenied after {took.TotalSeconds:F1}s");
}

// A revoked caller is cut off within one lifetime: UNAUTHENTICATED.
async Task RevokedCutOff()
{
    using var client = Client(addr);
    using var call = client.WithViewerService().GenerateTicks(NewTick(0, "down"));
    Assert(await call.ResponseStream.MoveNext(CancellationToken.None), "revoked: no tick arrived");
    revoked = true;
    var (code, took, _) = await StreamError(call.ResponseStream);
    revoked = false;
    Assert(code == StatusCode.Unauthenticated, $"revoked: the stream ended with {code}, want Unauthenticated");
    Assert(took < lifetime + TimeSpan.FromSeconds(3), $"revoked: the stream lasted {took}, want it cut off within one lifetime");
    var end = await HandlerEnd("GenerateTicks");
    Assert(end.GetProperty("cancelled").GetBoolean(), $"revoked: the handler's context was not cancelled: {end}");
    Console.WriteLine($"revoked: cut off Unauthenticated after {took.TotalSeconds:F1}s");
}

// Against a server without the sibling: the plain method, ended at exp.
async Task FallsBackToPlain()
{
    using var client = Client(plainAddr);
    using var call = client.WithViewerService().GenerateTicks(NewTick(0, "down"));
    var (code, took, ticks) = await StreamError(call.ResponseStream);
    Assert(ticks > 0, $"fallback: no tick arrived on the plain method ({code})");
    Assert(code == StatusCode.Unauthenticated, $"fallback: the plain stream ended with {code}, want Unauthenticated at exp");
    Assert(took < lifetime + TimeSpan.FromSeconds(3), $"fallback: the plain stream lasted {took}");
    await HandlerEnd("GenerateTicks");
    Console.WriteLine($"fallback: the plain method, ended at exp after {took.TotalSeconds:F1}s");
}

foreach (var (name, leg) in new (string, Func<Task>)[]
{
    ("bidi", BidiBothWays),
    ("server stream", ServerStream),
    ("client stream", ClientStream),
    ("another subject", AnotherSubjectRefused),
    ("revoked", RevokedCutOff),
    ("fallback", FallsBackToPlain),
})
{
    Console.WriteLine($"--- {name}");
    await leg();
}
Console.WriteLine("csharp rotation legs OK");
