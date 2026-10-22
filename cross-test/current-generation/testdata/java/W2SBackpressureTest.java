// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the W2S WebSocket listener. NOT generated: run.sh copies
// this file into the generated AgentUploadApi W2S client package and runs it as
// a main class.
//
// What it pins: a fake W2S server pushes text frames at full rate while the
// consumer holds the socket without reading. The listener must not read ahead
// of the consumer, so the messages it buffers while held stay near one instead
// of the whole stream. Every frame must still arrive, in order, once read.

package com.gitlab.example.dummy.agentsensor.model.w2sm.internal.grpc_over_rest.services.agentsensor;

import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.lang.reflect.Constructor;
import java.lang.reflect.Field;
import java.lang.reflect.Method;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.net.http.HttpClient;
import java.net.http.WebSocket;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.time.Duration;
import java.util.Base64;
import java.util.Collection;

public final class W2SBackpressureTest {
    private static final int TOTAL = 20_000;
    private static final long HOLD_MILLIS = 2_000;
    private static final int BOUND = 16;
    private static final String GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";

    private W2SBackpressureTest() {}

    private static void check(boolean ok, String message) {
        if (!ok) {
            throw new AssertionError(message);
        }
    }

    private static void serve(ServerSocket server) {
        try (Socket socket = server.accept()) {
            BufferedReader in = new BufferedReader(new InputStreamReader(socket.getInputStream(), StandardCharsets.US_ASCII));
            String key = null;
            String line;
            while ((line = in.readLine()) != null && !line.isEmpty()) {
                if (line.regionMatches(true, 0, "Sec-WebSocket-Key:", 0, 18)) {
                    key = line.substring(18).trim();
                }
            }
            String accept = Base64.getEncoder().encodeToString(
                    MessageDigest.getInstance("SHA-1").digest((key + GUID).getBytes(StandardCharsets.US_ASCII)));
            OutputStream out = socket.getOutputStream();
            out.write(("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
                    + "Sec-WebSocket-Accept: " + accept + "\r\n\r\n").getBytes(StandardCharsets.US_ASCII));
            for (int i = 0; i < TOTAL; i++) {
                byte[] payload = ("{\"i\":" + i + "}").getBytes(StandardCharsets.US_ASCII);
                out.write(0x81);
                out.write(payload.length);
                out.write(payload);
            }
            out.flush();
            in.read();
        } catch (Exception e) {
            // The client closing early ends the server; nothing to report.
        }
    }

    public static void main(String[] args) throws Exception {
        try (ServerSocket server = new ServerSocket(0, 50, InetAddress.getLoopbackAddress())) {
            Thread serverThread = new Thread(() -> serve(server));
            serverThread.setDaemon(true);
            serverThread.start();

            AgentUploadApiW2SClient client = new AgentUploadApiW2SClient(HttpClient.newHttpClient(),
                    "http://127.0.0.1:" + server.getLocalPort(), () -> "token");

            Class<?> listenerClass = Class.forName(AgentUploadApiW2SClient.class.getName() + "$TextQueueListener");
            Constructor<?> ctor = listenerClass.getDeclaredConstructor();
            ctor.setAccessible(true);
            Object listener = ctor.newInstance();

            Method connect = AgentUploadApiW2SClient.class.getDeclaredMethod("connectWebSocket", String.class, listenerClass);
            connect.setAccessible(true);
            WebSocket ws = (WebSocket) connect.invoke(client, "/ws", listener);

            Field messagesField = listenerClass.getDeclaredField("messages");
            messagesField.setAccessible(true);
            Collection<?> messages = (Collection<?>) messagesField.get(listener);

            Method nextText = listenerClass.getDeclaredMethod("nextText", Duration.class);
            nextText.setAccessible(true);

            Thread.sleep(HOLD_MILLIS);
            int buffered = messages.size();
            System.out.println("W2SBackpressureTest: listener buffered " + buffered
                    + " of " + TOTAL + " frames while the consumer held the socket");
            check(buffered <= BOUND,
                    "listener buffered " + buffered + " frames while the consumer held the socket; bound is " + BOUND);

            for (int i = 0; i < TOTAL; i++) {
                String text = (String) nextText.invoke(listener, Duration.ofSeconds(10));
                check(("{\"i\":" + i + "}").equals(text), "frame " + i + " out of order: " + text);
            }
            ws.abort();
            System.out.println("W2SBackpressureTest: all checks passed");
        }
    }
}
