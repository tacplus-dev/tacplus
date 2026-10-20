import com.example.unaryretry.model.s2sm.internal.grpc.clients.contract.unaryretry.RetryServiceGrpcClient;
import com.example.unaryretry.model.types.contract.unaryretry.RetryServiceGrpc;
import com.example.unaryretry.model.types.contract.unaryretry.Unaryretry;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Server;
import io.grpc.ServerBuilder;
import io.grpc.Status;
import io.grpc.stub.StreamObserver;
import java.lang.reflect.Method;
import java.util.HashSet;
import java.util.Set;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;

// Fault-injection behavior of the generated Java unary client retry path (issue
// #57): a peer in outage must not multiply load (retry budget), and the retry
// backoff must be jittered so callers that failed together do not retry in lockstep.
public class UnaryRetryBehaviorTest {

    // DownPeer fails every listPings with UNAVAILABLE and counts the attempts that reach it.
    static final class DownPeer extends RetryServiceGrpc.RetryServiceImplBase {
        final AtomicInteger attempts = new AtomicInteger();

        @Override
        public void listPings(Unaryretry.Ping request, StreamObserver<Unaryretry.PingList> responseObserver) {
            attempts.incrementAndGet();
            responseObserver.onError(Status.UNAVAILABLE.withDescription("peer down").asRuntimeException());
        }
    }

    static void check(boolean ok, String message) {
        if (!ok) {
            throw new AssertionError(message);
        }
    }

    public static void main(String[] args) throws Exception {
        testBudgetCapsRetriesDuringOutage();
        testBackoffIsJittered();
        System.out.println("UnaryRetryBehaviorTest PASSED");
    }

    // Fifty callers each allow two retries, so unbounded retry sends 150 attempts.
    // The budget admits a burst of ten retries and refills only on success, so 50
    // first attempts plus at most 10 retries reach the peer.
    static void testBudgetCapsRetriesDuringOutage() throws Exception {
        DownPeer peer = new DownPeer();
        Server server = ServerBuilder.forPort(0).addService(peer).build().start();
        ManagedChannel channel = ManagedChannelBuilder.forAddress("127.0.0.1", server.getPort())
            .usePlaintext().build();
        try {
            RetryServiceGrpcClient client = new RetryServiceGrpcClient(channel, () -> "test-token")
                .withRetry(2, 1, TimeUnit.MILLISECONDS);
            Unaryretry.Ping ping = Unaryretry.Ping.newBuilder().setM("x").build();

            final int calls = 50;
            ExecutorService pool = Executors.newFixedThreadPool(calls);
            Set<Future<?>> futures = new HashSet<>();
            for (int i = 0; i < calls; i++) {
                futures.add(pool.submit(() -> {
                    try {
                        client.listPings(ping);
                    } catch (RuntimeException expected) {
                        // Every call is expected to fail with UNAVAILABLE.
                    }
                    return null;
                }));
            }
            for (Future<?> f : futures) {
                f.get(60, TimeUnit.SECONDS);
            }
            pool.shutdownNow();

            int got = peer.attempts.get();
            check(got <= calls + 10,
                "outage saw " + got + " attempts for " + calls + " calls; want at most " + (calls + 10)
                    + " (first attempts plus the 10-retry budget)");
        } finally {
            channel.shutdownNow();
            server.shutdownNow();
        }
    }

    // The wait for a given attempt is jittered, so repeated draws for the same
    // attempt differ.
    static void testBackoffIsJittered() throws Exception {
        ManagedChannel channel = ManagedChannelBuilder.forAddress("127.0.0.1", 1).usePlaintext().build();
        try {
            RetryServiceGrpcClient client = new RetryServiceGrpcClient(channel, () -> "test-token")
                .withRetry(2, 100, TimeUnit.MILLISECONDS);
            Method backoff = RetryServiceGrpcClient.class.getDeclaredMethod("outboundBackoffMs", int.class);
            backoff.setAccessible(true);

            Set<Long> seen = new HashSet<>();
            for (int i = 0; i < 50; i++) {
                seen.add((Long) backoff.invoke(client, 0));
            }
            check(seen.size() >= 2, "first-retry backoff is the fixed " + backoff.invoke(client, 0)
                + "ms for every caller; want jitter");
        } finally {
            channel.shutdownNow();
        }
    }
}
