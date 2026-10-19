// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the event-bus subscribe iterator. NOT generated: run.sh
// copies this file into the generated AgentState StateClient package and runs
// it as a main class.
//
// What it pins: a fake broker streams topic records at full rate while the
// consumer holds the iterator without reading. The generated iterator must pace
// the broker to the consumer, so the records pushed while the consumer is held
// stay near the transport window instead of the whole stream. Every record must
// still arrive, in order, once the consumer reads.

package com.gitlab.example.dummy.agentsensor.model.s2sm.internal.state.services.agentsensor.agentstate;

import com.google.protobuf.ByteString;
import com.gitlab.example.dummy.agentsensor.model.types.generated.eventbus_support.EventbusSupport;
import com.gitlab.example.dummy.agentsensor.model.types.services.agentsensor.AgentStateServiceGrpc;
import com.gitlab.example.dummy.agentsensor.model.types.services.agentsensor.Agentsensor;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Server;
import io.grpc.ServerBuilder;
import io.grpc.stub.ServerCallStreamObserver;
import io.grpc.stub.StreamObserver;
import java.util.Iterator;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.locks.LockSupport;

public final class SubscribeBackpressureTest {
    private static final int TOTAL = 50_000;
    private static final int BODY_BYTES = 1024;
    private static final long HOLD_MILLIS = 3_000;
    private static final long BOUND = 5_000;

    private static final AtomicLong sent = new AtomicLong();

    static final class Broker extends AgentStateServiceGrpc.AgentStateServiceImplBase {
        @Override
        public StreamObserver<EventbusSupport.SubscribeRequest> subscribeToEvents(
                StreamObserver<Agentsensor.AgentStateEventsSubscribeRecord> responseObserver) {
            ServerCallStreamObserver<Agentsensor.AgentStateEventsSubscribeRecord> out =
                    (ServerCallStreamObserver<Agentsensor.AgentStateEventsSubscribeRecord>) responseObserver;
            return new StreamObserver<EventbusSupport.SubscribeRequest>() {
                @Override
                public void onNext(EventbusSupport.SubscribeRequest value) {
                }

                @Override
                public void onError(Throwable t) {
                }

                @Override
                public void onCompleted() {
                    Thread pump = new Thread(() -> pump(out));
                    pump.setDaemon(true);
                    pump.start();
                }
            };
        }

        private static void pump(ServerCallStreamObserver<Agentsensor.AgentStateEventsSubscribeRecord> out) {
            byte[] body = new byte[BODY_BYTES];
            for (int i = 0; i < TOTAL; i++) {
                while (!out.isReady()) {
                    if (out.isCancelled()) {
                        return;
                    }
                    LockSupport.parkNanos(100_000L);
                }
                Agentsensor.AgentEvent event = Agentsensor.AgentEvent.newBuilder()
                        .setId(Integer.toString(i))
                        .setBody(ByteString.copyFrom(body))
                        .build();
                out.onNext(Agentsensor.AgentStateEventsSubscribeRecord.newBuilder().setPayload(event).build());
                sent.incrementAndGet();
            }
            out.onCompleted();
        }
    }

    private static void check(boolean ok, String message) {
        if (!ok) {
            throw new AssertionError(message);
        }
    }

    public static void main(String[] args) throws Exception {
        Server server = ServerBuilder.forPort(0).addService(new Broker()).build().start();
        ManagedChannel channel = ManagedChannelBuilder.forAddress("localhost", server.getPort())
                .usePlaintext()
                .build();
        try {
            StateClient client = new StateClient();
            client.setChannel(channel, null);
            Iterator<Agentsensor.AgentStateEventsSubscribeRecord> records = client.subscribeToEventsRecords();

            Thread.sleep(HOLD_MILLIS);
            long pushedWhileHeld = sent.get();
            System.out.println("SubscribeBackpressureTest: broker pushed " + pushedWhileHeld
                    + " of " + TOTAL + " records while the consumer held the iterator");
            check(pushedWhileHeld <= BOUND,
                    "broker pushed " + pushedWhileHeld + " records while the consumer held the iterator; bound is " + BOUND);

            int received = 0;
            while (records.hasNext()) {
                Agentsensor.AgentStateEventsSubscribeRecord record = records.next();
                check(record.getPayload().getId().equals(Integer.toString(received)),
                        "record " + received + " out of order: " + record.getPayload().getId());
                received++;
            }
            check(received == TOTAL, "received " + received + " records, expected " + TOTAL);
            System.out.println("SubscribeBackpressureTest: all checks passed");
        } finally {
            channel.shutdownNow();
            server.shutdownNow();
        }
    }
}
