// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package com.example.grid.model.chassis.memberport;

import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.util.SplittableRandom;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;

/**
 * memberport-peer (tacplus#212): the test fixture that stands in for the
 * black-box protocol behind a member port. Test code only, never shipped in a
 * service. It talks raw TCP. Every byte it sends is a seeded pseudo-random
 * stream, and the other side checks what it receives byte for byte as it
 * arrives, so a dropped, duplicated, reordered or corrupted byte fails at the
 * first wrong offset.
 *
 * <p>A {@link Server} accepts and either echoes (ECHO) or exchanges (PUSH): it
 * sends its own seeded stream while it reads and checks the client's, in both
 * directions at once. {@link #exchange} is the client side.
 */
final class MemberportPeer {
    private MemberportPeer() {}

    /** The seeded stream: byte i of seed s is the same however it is chunked. */
    static final class Stream {
        private final SplittableRandom rng;

        Stream(long seed) {
            this.rng = new SplittableRandom(seed);
        }

        void fill(byte[] buf, int off, int len) {
            for (int i = 0; i < len; i++) {
                buf[off + i] = (byte) rng.nextInt(256);
            }
        }

        /** Checks {@code len} received bytes against the stream; returns the first wrong index or -1. */
        int check(byte[] buf, int off, int len) {
            for (int i = 0; i < len; i++) {
                if (buf[off + i] != (byte) rng.nextInt(256)) {
                    return i;
                }
            }
            return -1;
        }
    }

    enum Mode { ECHO, PUSH }

    /** What one side of a connection saw. */
    static final class Result {
        final long received;
        final long firstMismatch;
        final String error;

        Result(long received, long firstMismatch, String error) {
            this.received = received;
            this.firstMismatch = firstMismatch;
            this.error = error;
        }

        boolean ok(long expected) {
            return error == null && firstMismatch < 0 && received == expected;
        }

        @Override
        public String toString() {
            return "Result{received=" + received + ", firstMismatch=" + firstMismatch + ", error=" + error + "}";
        }
    }

    /** Sends {@code n} bytes of seed's stream in seeded chunk sizes, then half-closes. */
    static void send(Socket s, long seed, long n, int maxChunk, long pauseMillisPerChunk) throws IOException, InterruptedException {
        Stream stream = new Stream(seed);
        SplittableRandom chunks = new SplittableRandom(seed ^ 0x5DEECE66DL);
        byte[] buf = new byte[maxChunk];
        OutputStream out = s.getOutputStream();
        long sent = 0;
        while (sent < n) {
            int len = (int) Math.min(n - sent, 1 + chunks.nextInt(maxChunk));
            stream.fill(buf, 0, len);
            out.write(buf, 0, len);
            out.flush();
            sent += len;
            if (pauseMillisPerChunk > 0) {
                Thread.sleep(pauseMillisPerChunk);
            }
        }
        s.shutdownOutput();
    }

    /** Reads to EOF, checking every byte against seed's stream. */
    static Result receive(Socket s, long seed, long readPauseMillis) {
        return receive(s, seed, () -> readPauseMillis);
    }

    /**
     * {@link #receive(Socket, long, long)} with a pause after each read that is
     * re-read while it lasts: setting it to 0 wakes a stalled reader.
     */
    static Result receive(Socket s, long seed, java.util.function.LongSupplier readPauseMillis) {
        Stream stream = new Stream(seed);
        byte[] buf = new byte[16 * 1024];
        long received = 0;
        try {
            InputStream in = s.getInputStream();
            while (true) {
                int n = in.read(buf);
                if (n < 0) {
                    return new Result(received, -1, null);
                }
                int bad = stream.check(buf, 0, n);
                if (bad >= 0) {
                    return new Result(received + n, received + bad, null);
                }
                received += n;
                long until = System.currentTimeMillis() + readPauseMillis.getAsLong();
                while (readPauseMillis.getAsLong() > 0 && System.currentTimeMillis() < until) {
                    Thread.sleep(10);
                }
            }
        } catch (Exception e) {
            return new Result(received, -1, e.toString());
        }
    }

    /**
     * The client side: connects to {@code address}, sends {@code sendBytes} of
     * {@code sendSeed} while it reads and checks {@code expectSeed}'s stream.
     */
    static Result exchange(InetSocketAddress address, long sendSeed, long sendBytes, long expectSeed) throws Exception {
        return exchange(address, sendSeed, sendBytes, expectSeed, 16 * 1024, 0, 0);
    }

    static Result exchange(InetSocketAddress address, long sendSeed, long sendBytes, long expectSeed,
            int maxChunk, long sendPauseMillis, long readPauseMillis) throws Exception {
        try (Socket s = new Socket()) {
            s.connect(address, 10_000);
            s.setSoTimeout(60_000);
            AtomicLong sendError = new AtomicLong();
            Thread writer = new Thread(() -> {
                try {
                    send(s, sendSeed, sendBytes, maxChunk, sendPauseMillis);
                } catch (Exception e) {
                    sendError.incrementAndGet();
                }
            }, "peer-client-send");
            writer.start();
            Result r = receive(s, expectSeed, readPauseMillis);
            writer.join(60_000);
            if (r.error == null && sendError.get() > 0) {
                return new Result(r.received, r.firstMismatch, "send failed");
            }
            return r;
        }
    }

    /** The server side: echoes, or exchanges seeded streams with every client. */
    static final class Server implements AutoCloseable {
        final ServerSocket socket;
        final Mode mode;
        final long pushSeed;
        final long pushBytes;
        final long expectSeed;
        volatile long readPauseMillis;
        /** Connections accepted: zero means nothing ever reached the protocol. */
        final AtomicInteger accepted = new AtomicInteger();
        /** Bytes received over all connections. */
        final AtomicLong bytesReceived = new AtomicLong();
        final ConcurrentHashMap<Integer, Result> results = new ConcurrentHashMap<>();

        Server(Mode mode, long pushSeed, long pushBytes, long expectSeed) throws IOException {
            this.mode = mode;
            this.pushSeed = pushSeed;
            this.pushBytes = pushBytes;
            this.expectSeed = expectSeed;
            this.socket = new ServerSocket(0, 128, InetAddress.getLoopbackAddress());
            Thread t = new Thread(this::acceptLoop, "peer-server");
            t.setDaemon(true);
            t.start();
        }

        InetSocketAddress address() {
            return (InetSocketAddress) socket.getLocalSocketAddress();
        }

        private void acceptLoop() {
            while (!socket.isClosed()) {
                Socket s;
                try {
                    s = socket.accept();
                } catch (IOException e) {
                    return;
                }
                int id = accepted.incrementAndGet();
                Thread t = new Thread(() -> serve(id, s), "peer-server-" + id);
                t.setDaemon(true);
                t.start();
            }
        }

        private void serve(int id, Socket s) {
            try (s) {
                if (mode == Mode.ECHO) {
                    byte[] buf = new byte[16 * 1024];
                    InputStream in = s.getInputStream();
                    OutputStream out = s.getOutputStream();
                    while (true) {
                        int n = in.read(buf);
                        if (n < 0) {
                            break;
                        }
                        bytesReceived.addAndGet(n);
                        out.write(buf, 0, n);
                        out.flush();
                        if (readPauseMillis > 0) {
                            Thread.sleep(readPauseMillis);
                        }
                    }
                    s.shutdownOutput();
                    results.put(id, new Result(-1, -1, null));
                    return;
                }
                Thread writer = new Thread(() -> {
                    try {
                        send(s, pushSeed, pushBytes, 16 * 1024, 0);
                    } catch (Exception ignored) {
                        // The reader's result records the connection's fate.
                    }
                }, "peer-server-send-" + id);
                writer.start();
                Result r = receive(s, expectSeed, () -> readPauseMillis);
                bytesReceived.addAndGet(r.received);
                writer.join(60_000);
                results.put(id, r);
            } catch (Exception e) {
                results.put(id, new Result(0, -1, e.toString()));
            }
        }

        @Override
        public void close() throws IOException {
            socket.close();
        }
    }
}
