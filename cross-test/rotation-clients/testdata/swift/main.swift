// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// The Swift leg of cross-test/rotation-clients (tacplus#323): the generated
// Swift C2SClient (grpc-swift 2) against the generated Go server, run by the
// Go fixture's TestRotationClients. The callers below are plain caller code:
// nothing in them mentions rotation. Each stream must outlive several token
// lifetimes with no loss, duplication or reordering, on one handler call with
// one context.
//
// Env: ROTATION_ADDR (the server, with the sibling), ROTATION_PLAIN_ADDR (the
// plain service only), ROTATION_CONTROL (tokens and the handler's ends),
// ROTATION_TOKEN_LIFETIME_MS.

import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
import GRPCCore
import RotationclientsModel
import Synchronization

let env = ProcessInfo.processInfo.environment
let addr = env["ROTATION_ADDR"]!
let plainAddr = env["ROTATION_PLAIN_ADDR"]!
let control = env["ROTATION_CONTROL"]!
let lifetime = Duration.milliseconds(Int(env["ROTATION_TOKEN_LIFETIME_MS"]!)!)
// Several token lifetimes.
let streamFor = lifetime * 3

struct Failure: Error, CustomStringConvertible {
    let description: String
}

func check(_ condition: Bool, _ message: @autoclosure () -> String) throws {
    if !condition {
        throw Failure(description: message())
    }
}

func checkSequence(_ what: String, _ got: [Int64], _ n: Int64) throws {
    try check(Int64(got.count) == n, "\(what): got \(got.count) messages, want \(n) (loss or duplication)")
    for (i, seq) in got.enumerated() {
        try check(seq == Int64(i + 1), "\(what): message \(i) is seq \(seq), want \(i + 1) (reordering, loss or duplication)")
    }
}

func get(_ path: String) async throws -> String {
    let (data, response) = try await URLSession.shared.data(from: URL(string: control + path)!)
    try check((response as? HTTPURLResponse)?.statusCode == 200, "control \(path): \(response)")
    return String(decoding: data, as: UTF8.self)
}

func handlerEnd(_ method: String) async throws -> [String: Any] {
    let deadline = ContinuousClock.now + .seconds(10)
    while true {
        let ends = try JSONSerialization.jsonObject(with: Data(try await get("/ends").utf8)) as? [[String: Any]] ?? []
        if !ends.isEmpty {
            try check(ends.count == 1, "\(method): \(ends.count) handler calls, want one: \(ends)")
            try check(ends[0]["method"] as? String == method, "handler end is \(ends[0]["method"] ?? ""), want \(method)")
            return ends[0]
        }
        try check(ContinuousClock.now < deadline, "\(method): the handler did not return within 10s")
        try await Task.sleep(for: .milliseconds(50))
    }
}

func mints() async throws -> Int {
    Int(try await get("/mints"))!
}

/// The caller's token provider: alice's tokens, until revoked (the mint
/// fails) or impersonating (another subject's).
final class TokenSource: Sendable {
    private let state = Mutex<(subject: String, revoked: Bool)>(("alice@example.com", false))

    func impersonate(_ subject: String) { self.state.withLock { $0.subject = subject } }
    func revoke() { self.state.withLock { $0.revoked = true } }

    func provide() async throws -> String {
        let (subject, revoked) = self.state.withLock { ($0.subject, $0.revoked) }
        if revoked {
            throw Failure(description: "identity: caller revoked")
        }
        return try await get("/token?sub=" + subject.addingPercentEncoding(withAllowedCharacters: .alphanumerics)!)
    }
}

final class Counter: Sendable {
    private let value = Mutex<Int64>(0)
    func add() -> Int64 { self.value.withLock { $0 += 1; return $0 } }
    var current: Int64 { self.value.withLock { $0 } }
}

func client(_ target: String, _ src: TokenSource) throws -> C2SClient {
    let colon = target.lastIndex(of: ":")!
    return try C2SClient(
        host: String(target[..<colon]),
        port: Int(target[target.index(after: colon)...])!,
        useTLS: false,
        tokenProvider: { try await src.provide() }
    )
}

func tick(_ seq: Int64, _ m: String) -> Rotationclients_Tick {
    var t = Rotationclients_Tick()
    t.seq = seq
    t.m = m
    return t
}

// The bidi stream both ways at once across several lifetimes.
func bidiBothWays() async throws {
    let src = TokenSource()
    let c2s = try client(addr, src)
    let before = try await mints()
    let sent = Counter()
    let echoed = try await c2s.withViewerService { svc in
        try await svc.assistTicks(requestProducer: { writer in
            let start = ContinuousClock.now
            while ContinuousClock.now - start < streamFor {
                try await writer.write(tick(sent.add(), "up"))
                try await Task.sleep(for: .milliseconds(2))
            }
        }, onResponse: { response in
            var got: [Int64] = []
            for try await m in response.messages {
                got.append(m.seq)
            }
            return got
        })
    }
    try checkSequence("bidi echoed", echoed, sent.current)
    let end = try await handlerEnd("AssistTicks")
    try check(end["sameContext"] as? Bool == true, "bidi: the handler saw more than one context")
    try check((end["received"] as? NSNumber)?.int64Value == sent.current && end["inOrder"] as? Bool == true, "bidi: the handler received \(end)")
    try check(end["subject"] as? String == "alice@example.com", "bidi: the handler's caller is \(end["subject"] ?? "")")
    let renewed = try await mints() - before
    try check(renewed >= 3, "bidi: \(renewed) tokens minted, want several: the stream did not rotate")
    print("bidi: \(sent.current) ticks both ways across \(renewed) tokens, one handler call")
}

// A server stream across several lifetimes, ended OK by the handler.
func serverStream() async throws {
    let c2s = try client(addr, TokenSource())
    let want = Int64(streamFor / .milliseconds(50))
    let got = try await c2s.withViewerService { svc in
        try await svc.generateTicks(tick(want, "down")) { response in
            var got: [Int64] = []
            for try await m in response.messages {
                got.append(m.seq)
            }
            return got
        }
    }
    try checkSequence("server stream", got, want)
    let end = try await handlerEnd("GenerateTicks")
    try check(end["code"] as? String == "OK" && end["sameContext"] as? Bool == true, "server stream: handler end \(end)")
    print("server stream: \(want) ticks across lifetimes, one handler call")
}

// A client stream across several lifetimes: one answer counting every tick.
func clientStream() async throws {
    let c2s = try client(addr, TokenSource())
    let sent = Counter()
    let resp = try await c2s.withViewerService { svc in
        try await svc.sendTicks(requestProducer: { writer in
            let start = ContinuousClock.now
            while ContinuousClock.now - start < streamFor {
                try await writer.write(tick(sent.add(), "up"))
                try await Task.sleep(for: .milliseconds(5))
            }
        })
    }
    try check(resp.seq == sent.current && resp.m == "in-order", "client stream: the handler answered \(resp.seq) \(resp.m), want \(sent.current) in-order")
    let end = try await handlerEnd("SendTicks")
    try check(end["sameContext"] as? Bool == true, "client stream: the handler saw more than one context")
    print("client stream: \(sent.current) ticks across lifetimes, one handler call")
}

// streamError reads a server stream of ticks forever until it ends: its code
// (.ok for none), how long it lasted and how many ticks arrived. first runs
// once the first tick has arrived.
func streamError(_ c2s: C2SClient, first: @escaping @Sendable () -> Void) async throws -> (RPCError.Code?, Duration, Int64) {
    let ticks = Counter()
    let opened = ContinuousClock.now
    do {
        try await c2s.withViewerService { svc in
            try await svc.generateTicks(tick(0, "down")) { response in
                for try await _ in response.messages {
                    if ticks.add() == 1 {
                        first()
                    }
                }
            }
        }
        return (nil, opened.duration(to: .now), ticks.current)
    } catch let error as RPCError {
        return (error.code, opened.duration(to: .now), ticks.current)
    }
}

// A rotation with another subject's token is refused: PERMISSION_DENIED.
func anotherSubjectRefused() async throws {
    let src = TokenSource()
    let (code, took, ticks) = try await streamError(try client(addr, src)) { src.impersonate("mallory@example.com") }
    try check(ticks > 0, "another subject: no tick arrived before the refusal")
    try check(code == .permissionDenied, "another subject: the stream ended with \(String(describing: code)), want permissionDenied")
    try check(took < lifetime * 2 + .seconds(3), "another subject: the stream lasted \(took)")
    let end = try await handlerEnd("GenerateTicks")
    try check(end["cancelled"] as? Bool == true && end["subject"] as? String == "alice@example.com", "another subject: handler end \(end)")
    print("another subject: refused permissionDenied after \(took)")
}

// A revoked caller is cut off within one lifetime: UNAUTHENTICATED.
func revokedCutOff() async throws {
    let src = TokenSource()
    let (code, took, _) = try await streamError(try client(addr, src)) { src.revoke() }
    try check(code == .unauthenticated, "revoked: the stream ended with \(String(describing: code)), want unauthenticated")
    try check(took < lifetime + .seconds(3), "revoked: the stream lasted \(took), want it cut off within one lifetime")
    let end = try await handlerEnd("GenerateTicks")
    try check(end["cancelled"] as? Bool == true, "revoked: the handler's context was not cancelled: \(end)")
    print("revoked: cut off unauthenticated after \(took)")
}

// Against a server without the sibling: the plain method, ended at exp.
func fallsBackToPlain() async throws {
    let (code, took, ticks) = try await streamError(try client(plainAddr, TokenSource())) {}
    try check(ticks > 0, "fallback: no tick arrived on the plain method (\(String(describing: code)))")
    try check(code == .unauthenticated, "fallback: the plain stream ended with \(String(describing: code)), want unauthenticated at exp")
    try check(took < lifetime + .seconds(3), "fallback: the plain stream lasted \(took)")
    _ = try await handlerEnd("GenerateTicks")
    print("fallback: the plain method, ended at exp after \(took)")
}

let legs: [(String, () async throws -> Void)] = [
    ("bidi", bidiBothWays),
    ("server stream", serverStream),
    ("client stream", clientStream),
    ("another subject", anotherSubjectRefused),
    ("revoked", revokedCutOff),
    ("fallback", fallsBackToPlain),
]
// The leg marker and a leg's failure go to stderr, unbuffered: stdout is a
// pipe here, block-buffered, and lost when the process exits on a failure.
func report(_ line: String) {
    FileHandle.standardError.write(Data((line + "\n").utf8))
}
for (name, leg) in legs {
    report("--- \(name)")
    do {
        try await leg()
    } catch let error as RPCError {
        // One short line for run.sh, which quotes it in the build's verdict:
        // the message, then what closed the stream (a reset, a GOAWAY).
        report("Fatal error: \(name): \(error)")
        report("LegError: \(error.code): \(error.message)\(error.cause.map { " (\($0))" } ?? "")")
        exit(1)
    } catch {
        report("Fatal error: \(name): \(error)")
        exit(1)
    }
}
print("swift rotation legs OK")
