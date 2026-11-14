// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Dials the generated Go daemon (testdata/swift-daemon) with C2DClient() and no
// argument, a unary call and a server stream: the default socket must be the
// socket_path the daemon binds (issue #494). The old default,
// ~/.<package>/daemon.sock, reached no daemon.

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
print("swift default C2D dial passed")
