// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Runtime coverage for the generated Java standalone C2D host, in the
// main()-style the other generated Java self-tests use. It lives OUTSIDE the
// generated model packages and reaches the daemon only the way a cluster-less
// daemon's main does: it implements the generated s2sm Handler and calls the
// public ServiceManager.serveC2DStandalone, with no config, no identity and no
// state store anywhere in the process. Calls go over the real Unix socket,
// through the generated c2dm client.
//
// What each check would catch:
//   - a valid call succeeds with NO token and NO authorizer: a c2d wrapper
//     that demanded token auth could not serve it;
//   - an invalid request sent on the raw stub (the c2dm client validates
//     before sending, so it would never reach the server) is INVALID_ARGUMENT
//     and never reaches the handler: the standalone host serves the generated
//     wrapper, not the bare handler;
//   - a handler response that breaks its constraints is INTERNAL: the full
//     handler pipeline runs, not just the request half;
//   - the socket file is mode 0600 and replaced a stale file left at the
//     path: the access boundary and the stale-socket removal;
//   - completing the stop future ends serveC2DStandalone normally, the socket
//     file is gone, and Handler.shutdown ran exactly once, after the drain.

package com.example.c2dserver.daemon;

import com.example.c2dserver.model.chassis.servicemanager.Caller;
import com.example.c2dserver.model.s2sm.Handler;
import com.example.c2dserver.model.s2sm.ServiceManager;
import com.example.c2dserver.model.s2sm.internal.grpc.servers.svc.c2dserver.LocalDaemon;
import com.example.c2dserver.model.types.svc.c2dserver.C2dserver;
import com.example.c2dserver.model.types.svc.c2dserver.LocalDaemonGrpc;
import io.grpc.ManagedChannel;
import io.grpc.Status;
import io.grpc.StatusRuntimeException;
import io.grpc.netty.shaded.io.grpc.netty.NettyChannelBuilder;
import io.grpc.netty.shaded.io.netty.channel.EventLoopGroup;
import io.grpc.netty.shaded.io.netty.channel.nio.NioEventLoopGroup;
import io.grpc.netty.shaded.io.netty.channel.socket.nio.NioDomainSocketChannel;
import io.grpc.stub.StreamObserver;
import java.net.UnixDomainSocketAddress;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.nio.file.attribute.PosixFilePermissions;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;

public class C2DStandaloneSelfTest {
    private static final String SOCKET_PATH = "/tmp/tacplus-c2d-server-check/daemon.sock";
    private static int failures = 0;

    private static void check(boolean ok, String message) {
        if (!ok) {
            System.err.println("FAIL: " + message);
            failures++;
        }
    }

    static final class RecordingDaemon implements Handler, LocalDaemon.LocalDaemonHandler {
        final AtomicInteger unaryHits = new AtomicInteger();
        final AtomicInteger streamHits = new AtomicInteger();
        final AtomicInteger shutdownCalls = new AtomicInteger();

        // The generated Handler: the c2d getter and shutdown, nothing cluster-bound.
        @Override
        public LocalDaemon.LocalDaemonHandler getLocalDaemonGrpcImpl() {
            return this;
        }

        @Override
        public void shutdown() {
            shutdownCalls.incrementAndGet();
        }

        @Override
        public C2dserver.LocalResponse getLocal(Caller caller, C2dserver.LocalRequest request) {
            unaryHits.incrementAndGet();
            if (request.getItemId().equals("bad-response")) {
                return C2dserver.LocalResponse.getDefaultInstance(); // result_id is assert_required
            }
            return C2dserver.LocalResponse.newBuilder().setResultId("result-" + request.getItemId()).build();
        }

        @Override
        public void generateLocalEvents(Caller caller, C2dserver.LocalRequest request,
                StreamObserver<C2dserver.LocalEvent> responseObserver) {
            streamHits.incrementAndGet();
            for (int i = 0; i < request.getCount(); i++) {
                responseObserver.onNext(C2dserver.LocalEvent.newBuilder()
                        .setItemId(request.getItemId()).setSeq(i).build());
            }
            responseObserver.onCompleted();
        }
    }

    private static Status.Code codeOf(Runnable call) {
        try {
            call.run();
            return Status.Code.OK;
        } catch (StatusRuntimeException e) {
            return e.getStatus().getCode();
        }
    }

    public static void main(String[] args) throws Exception {
        Path socket = Paths.get(SOCKET_PATH);
        // A stale file at the socket path, as a crashed daemon leaves behind.
        Files.createDirectories(socket.getParent());
        Files.deleteIfExists(socket);
        Files.writeString(socket, "stale");

        // A daemon passes the generated constant for the declared socket_path.
        check(ServiceManager.C2D_SOCKET_PATH.equals(SOCKET_PATH),
                "ServiceManager.C2D_SOCKET_PATH = " + ServiceManager.C2D_SOCKET_PATH + ", want the declared socket_path " + SOCKET_PATH);
        RecordingDaemon daemon = new RecordingDaemon();
        CompletableFuture<Void> stop = new CompletableFuture<>();
        AtomicReference<Throwable> serveErr = new AtomicReference<>();
        Thread host = new Thread(() -> {
            try {
                ServiceManager.serveC2DStandalone(ServiceManager.C2D_SOCKET_PATH, daemon, stop);
            } catch (Throwable t) {
                serveErr.set(t);
            }
        }, "c2d-standalone-host");
        host.start();
        // Ready once the stale regular file has been replaced by the socket AND
        // the socket is locked down: the chassis binds first and chmods after,
        // so the mode is only final a moment after the file appears. A host
        // that never restricts it fails here at the deadline.
        long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(10);
        while (!(Files.exists(socket) && !Files.isRegularFile(socket)
                && PosixFilePermissions.toString(Files.getPosixFilePermissions(socket)).equals("rw-------"))) {
            if (!host.isAlive()) {
                System.err.println("FAIL: standalone c2d host exited before listening: " + serveErr.get());
                System.exit(1);
            }
            if (System.nanoTime() > deadline) {
                System.err.println("FAIL: no owner-only (rw-------) c2d socket at " + SOCKET_PATH
                        + (Files.exists(socket) ? " (mode " + PosixFilePermissions.toString(Files.getPosixFilePermissions(socket)) + ")" : ""));
                System.exit(1);
            }
            Thread.sleep(20);
        }

        String perms = PosixFilePermissions.toString(Files.getPosixFilePermissions(socket));
        check(perms.equals("rw-------"), "c2d socket mode = " + perms + ", want rw------- (the socket ACL is the c2d auth boundary)");
        check(!Files.isRegularFile(socket), "stale regular file was not replaced by the socket");

        EventLoopGroup clientGroup = new NioEventLoopGroup(1);
        ManagedChannel channel = NettyChannelBuilder.forAddress(UnixDomainSocketAddress.of(socket))
                .channelType(NioDomainSocketChannel.class)
                .eventLoopGroup(clientGroup)
                .usePlaintext()
                .build();
        try (com.example.c2dserver.model.c2dm.ServiceManager client =
                new com.example.c2dserver.model.c2dm.ServiceManager(channel)) {
            var daemonClient = client.grpcClients().svc().localDaemon();

            // Valid unary call: no token anywhere, and it must still be served.
            C2dserver.LocalResponse resp = daemonClient.getLocal(
                    C2dserver.LocalRequest.newBuilder().setItemId("abc").build());
            check(resp.getResultId().equals("result-abc"), "result_id = " + resp.getResultId() + ", want result-abc");
            check(daemon.unaryHits.get() == 1, "handler hits after valid call = " + daemon.unaryHits.get() + ", want 1");

            // Invalid request on the raw stub: the wrapper must reject it before
            // the handler (item_id has assert_min_size 3).
            var raw = LocalDaemonGrpc.newBlockingStub(channel);
            Status.Code invalid = codeOf(() -> raw.getLocal(C2dserver.LocalRequest.newBuilder().setItemId("ab").build()));
            check(invalid == Status.Code.INVALID_ARGUMENT, "invalid request: got " + invalid + ", want INVALID_ARGUMENT");
            check(daemon.unaryHits.get() == 1, "invalid request reached the handler (hits = " + daemon.unaryHits.get() + ")");

            // Response validation still runs on the c2d surface.
            Status.Code badResponse = codeOf(() -> raw.getLocal(C2dserver.LocalRequest.newBuilder().setItemId("bad-response").build()));
            check(badResponse == Status.Code.INTERNAL, "constraint-breaking response: got " + badResponse + ", want INTERNAL");

            // Server streaming over the socket.
            List<Integer> seqs = new ArrayList<>();
            AtomicReference<Throwable> streamErr = new AtomicReference<>();
            CountDownLatch done = new CountDownLatch(1);
            daemonClient.generateLocalEvents(C2dserver.LocalRequest.newBuilder().setItemId("abc").setCount(3).build(),
                    new StreamObserver<C2dserver.LocalEvent>() {
                        @Override public void onNext(C2dserver.LocalEvent ev) { seqs.add(ev.getSeq()); }
                        @Override public void onError(Throwable t) { streamErr.set(t); done.countDown(); }
                        @Override public void onCompleted() { done.countDown(); }
                    });
            check(done.await(10, TimeUnit.SECONDS), "server stream did not complete");
            check(streamErr.get() == null, "server stream failed: " + streamErr.get());
            check(seqs.equals(List.of(0, 1, 2)), "streamed seqs = " + seqs + ", want [0, 1, 2]");

            // Invalid streaming request: rejected before the handler.
            Status.Code badStream = codeOf(() -> raw.generateLocalEvents(
                    C2dserver.LocalRequest.newBuilder().setItemId("ab").setCount(1).build()).hasNext());
            check(badStream == Status.Code.INVALID_ARGUMENT, "invalid streaming request: got " + badStream + ", want INVALID_ARGUMENT");
            check(daemon.streamHits.get() == 1, "stream handler hits = " + daemon.streamHits.get() + ", want 1");
        } finally {
            channel.awaitTermination(5, TimeUnit.SECONDS);
            clientGroup.shutdownGracefully();
        }

        check(daemon.shutdownCalls.get() == 0, "Handler.shutdown ran while serving (calls = " + daemon.shutdownCalls.get() + ")");

        // Shutdown: the entry returns normally, removes the socket file and
        // calls Handler.shutdown once.
        stop.complete(null);
        host.join(TimeUnit.SECONDS.toMillis(40));
        check(!host.isAlive(), "standalone c2d host did not stop after its stop future completed");
        check(serveErr.get() == null, "standalone c2d host failed: " + serveErr.get());
        check(!Files.exists(socket), "socket file still present after shutdown");
        check(daemon.shutdownCalls.get() == 1, "Handler.shutdown calls after shutdown = " + daemon.shutdownCalls.get() + ", want 1");

        if (failures > 0) {
            System.err.println(failures + " C2D standalone host check(s) failed");
            System.exit(1);
        }
        System.out.println("C2D standalone host self-test passed");
        System.exit(0);
    }
}
