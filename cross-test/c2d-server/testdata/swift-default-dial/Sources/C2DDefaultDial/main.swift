// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Dials the generated Go daemon (testdata/swift-daemon) with C2DClient() and no
// argument, a unary call and a server stream: the default socket must be the
// socket_path the daemon binds (issue #494). The old default,
// ~/.<package>/daemon.sock, reached no daemon.
//
// Then one client makes `sameConnectionCalls` unary calls on its one
// connection (issue #958). A Go server with grpc-go's dynamic HTTP/2 windows
// sent a BDP PING for each call's request, and swift-nio-http2 closes a
// connection past 200 inbound PINGs in 30 s: call 199 failed UNAVAILABLE
// "Stream unexpectedly closed.", as a stream of more than 200 messages did.

import C2dserverModel
import Foundation

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data("swift default C2D dial FAILED: \(message)\n".utf8))
    exit(1)
}

let client = C2DClient()
guard client.socketPath == "/tmp/tacplus-c2d-server-check/daemon.sock" else {
    fail("default socketPath = \(client.socketPath), want the declared socket_path")
}

var request = C2dserver_LocalRequest()
request.itemID = "swift-default"
request.count = 3

do {
    let response = try await client.withLocalDaemon { daemon in
        try await daemon.getLocal(request)
    }
    guard response.resultID == "result-swift-default" else {
        fail("result_id = \(response.resultID), want result-swift-default")
    }
    let seqs = try await client.withLocalDaemon { daemon in
        try await daemon.generateLocalEvents(request) { stream in
            var seqs: [Int32] = []
            for try await event in stream.messages {
                seqs.append(event.seq)
            }
            return seqs
        }
    }
    guard seqs == [0, 1, 2] else {
        fail("streamed seqs = \(seqs), want [0, 1, 2]")
    }
} catch {
    fail("call over the default socket: \(error)")
}

let sameConnectionCalls = 300
// On stderr, unbuffered: the log shows which part a run past its deadline hung in.
FileHandle.standardError.write(Data("swift default C2D dial: first calls passed; \(sameConnectionCalls) calls on one connection\n".utf8))
do {
    try await client.withLocalDaemon { daemon in
        for call in 1...sameConnectionCalls {
            do {
                let response = try await daemon.getLocal(request)
                guard response.resultID == "result-swift-default" else {
                    fail("call \(call) on one connection: result_id = \(response.resultID), want result-swift-default")
                }
            } catch {
                fail("call \(call) of \(sameConnectionCalls) on one connection: \(error)")
            }
        }
    }
} catch {
    fail("\(sameConnectionCalls) calls on one connection: \(error)")
}
print("swift default C2D dial passed (\(sameConnectionCalls) calls on one connection)")
