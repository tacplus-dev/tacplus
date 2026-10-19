// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

import com.example.contract.model.chassis.serviceconnection.ServiceConnection;
import com.example.contract.model.types.sys.grpcconfig.ClusterServiceGrpc;
import com.example.contract.model.types.sys.grpcconfig.Grpcconfig.PingResponse;
import com.example.contract.model.types.sys.host.HostOuterClass.Host;
import com.example.contract.model.types.sys.trace.Trace.TraceRequest;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Server;
import io.grpc.ServerBuilder;
import io.grpc.stub.StreamObserver;
import java.lang.reflect.Method;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;

// Two READY static endpoints, one answering Ping at once and one answering only
// after SLOW_PING_MS. Both are reachable, so only the measured latency can tell
// them apart. After one ping cycle and one rebalance the active endpoint must be
// the fast one, as it is in the Go and Rust chassis, which rank by Ping RPC
// round trip.
public final class EndpointLatencyTest {
    private static final long SLOW_PING_MS = 200;

    private static final class Peer extends ClusterServiceGrpc.ClusterServiceImplBase {
        private final long delayMs;

        Peer(long delayMs) {
            this.delayMs = delayMs;
        }

        @Override
        public void ping(TraceRequest request, StreamObserver<PingResponse> responseObserver) {
            try {
                Thread.sleep(delayMs);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                responseObserver.onError(e);
                return;
            }
            responseObserver.onNext(PingResponse.getDefaultInstance());
            responseObserver.onCompleted();
        }
    }

    public static void main(String[] args) throws Exception {
        Server slow = ServerBuilder.forPort(0).addService(new Peer(SLOW_PING_MS)).build().start();
        Server fast = ServerBuilder.forPort(0).addService(new Peer(0)).build().start();
        String slowTarget = "127.0.0.1:" + slow.getPort();
        String fastTarget = "127.0.0.1:" + fast.getPort();

        Map<String, ManagedChannel> channels = new ConcurrentHashMap<>();
        ServiceConnection.ChannelFactory factory = (serviceId, target) -> {
            ManagedChannel channel = ManagedChannelBuilder.forTarget(target).usePlaintext().build();
            channels.put(target, channel);
            return channel;
        };

        // The slow peer is listed first. No latency is measured before the first
        // ping, so the first routable endpoint is the one selected.
        List<Host> hosts = List.of(
                Host.newBuilder().setHost("127.0.0.1").setPort(slow.getPort()).build(),
                Host.newBuilder().setHost("127.0.0.1").setPort(fast.getPort()).build());
        ServiceConnection sc = ServiceConnection.newStatic("endpointlatency", hosts, factory);
        try {
            if (sc.conn() != channels.get(slowTarget)) {
                throw new AssertionError("initial active endpoint must be the first listed (slow) one");
            }

            // The static pool runs these two methods every 30 s. They are called
            // directly so the test does not wait for the schedule.
            invoke(sc, "pingAll");
            invoke(sc, "selectActive");

            if (sc.conn() != channels.get(fastTarget)) {
                throw new AssertionError("after a ping and a rebalance the active endpoint must be the fast "
                        + "peer (" + fastTarget + "), not the slow one (" + slowTarget + "): latency is not "
                        + "measured as a Ping round trip");
            }
            System.out.println("active endpoint after rebalance: " + fastTarget + " (fast peer)");
        } finally {
            sc.close();
            slow.shutdownNow();
            fast.shutdownNow();
        }
    }

    private static void invoke(ServiceConnection sc, String name) throws Exception {
        Method m = ServiceConnection.class.getDeclaredMethod(name);
        m.setAccessible(true);
        m.invoke(sc);
    }
}
