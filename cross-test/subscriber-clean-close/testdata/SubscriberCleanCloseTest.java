// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

import com.example.contract.model.s2sm.subscribe.ContractfixturesFixtureStateEventsSubscriber;
import io.grpc.CallCredentials;
import io.grpc.CallOptions;
import io.grpc.Channel;
import io.grpc.ClientCall;
import io.grpc.Metadata;
import io.grpc.MethodDescriptor;
import io.grpc.Status;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.Supplier;

// A peer that ends every subscribe stream with OK and no messages, the way a
// graceful drain or a load-balancer idle close does. Each subscribe attempt is
// one clean close, so the number of channel resolves is the number of
// reconnect attempts the subscriber made.
public final class SubscriberCleanCloseTest {
    // How long the subscriber runs against the always-closing peer.
    private static final long WINDOW_MS = 10_000;
    // Mirrors Subscriber.MIN_BACKOFF_MS. A clean close must sleep at least this
    // long before the next attempt, so the attempts in the window are bounded by
    // WINDOW_MS / MIN_BACKOFF_MS plus the first one.
    private static final long MIN_BACKOFF_MS = 500;
    private static final int MAX_RESOLVES = (int) (WINDOW_MS / MIN_BACKOFF_MS) + 1;

    private static final class CleanCloseChannel extends Channel {
        @Override
        public String authority() {
            return "clean-close";
        }

        @Override
        public <ReqT, RespT> ClientCall<ReqT, RespT> newCall(MethodDescriptor<ReqT, RespT> method, CallOptions options) {
            return new ClientCall<ReqT, RespT>() {
                @Override
                public void start(Listener<RespT> responseListener, Metadata headers) {
                    responseListener.onClose(Status.OK, new Metadata());
                }

                @Override
                public void request(int numMessages) {
                }

                @Override
                public void cancel(String message, Throwable cause) {
                }

                @Override
                public void halfClose() {
                }

                @Override
                public void sendMessage(ReqT message) {
                }
            };
        }
    }

    public static void main(String[] args) throws Exception {
        AtomicInteger resolves = new AtomicInteger();
        CleanCloseChannel channel = new CleanCloseChannel();
        Supplier<Channel> channelSupplier = () -> {
            resolves.incrementAndGet();
            return channel;
        };
        Supplier<CallCredentials> noCredentials = () -> null;
        ContractfixturesFixtureStateEventsSubscriber subscriber =
                new ContractfixturesFixtureStateEventsSubscriber(channelSupplier, noCredentials, "clean-close");

        Thread worker = new Thread(() -> {
            try {
                subscriber.subscribe((event, meta) -> {
                });
            } catch (InterruptedException expected) {
                // The window closed; the subscriber exits on interrupt.
            }
        }, "subscriber");
        worker.start();
        Thread.sleep(WINDOW_MS);
        worker.interrupt();
        worker.join(5_000);
        if (worker.isAlive()) {
            throw new AssertionError("subscriber did not exit after interrupt");
        }

        int count = resolves.get();
        System.out.println("resolves in " + WINDOW_MS + " ms against a clean-closing peer: " + count
                + " (ceiling " + MAX_RESOLVES + ")");
        if (count > MAX_RESOLVES) {
            throw new AssertionError("subscriber re-resolved " + count + " times in " + WINDOW_MS
                    + " ms; a clean close must wait at least the minimum backoff before the next attempt");
        }
    }
}
