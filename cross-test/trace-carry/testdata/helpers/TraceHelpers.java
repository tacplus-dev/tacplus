// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tracehelpers;

import com.gitlab.example.tacplus.trace.tracehelpers.model.s2sm.HelperRowRecordReader;
import com.gitlab.example.tacplus.trace.tracehelpers.model.s2sm.internal.grpc.clients.vservices.helperquery.HelperRecordMapGrpcClient;
import com.gitlab.example.tacplus.trace.tracehelpers.model.s2sm.internal.state.services.tracehelpers.helperstate.StateClient;
import com.gitlab.example.tacplus.trace.tracehelpers.model.types.services.tracehelpers.Tracehelpers;
import com.gitlab.example.tacplus.trace.tracehelpers.model.types.vservices.helperquery.Helperquery;
import com.google.protobuf.Descriptors;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.TimeUnit;

/**
 * The trace-helpers gate's Java leg (tacplus#327). The generated state client
 * and record reader talk to the recorder (recorder/main.go), which logs
 * "&lt;method&gt; &lt;trace_id&gt;" for every request it receives. Each traced
 * overload must put its trace on every request it sends, and today's form
 * none. Then every generated sys message (the outer classes named in the file
 * passed as the first argument) must have trace_id = 200.
 */
public final class TraceHelpers {
    private TraceHelpers() {}

    @FunctionalInterface
    interface Call {
        void run(String traceId) throws Exception;
    }

    private static final List<String> failures = new ArrayList<>();
    private static Path log;
    private static int seen;

    // The requests logged since the last take, but the state client's own
    // background watches (no request causes them).
    private static List<String[]> take() throws Exception {
        List<String> lines = Files.readAllLines(log, StandardCharsets.UTF_8);
        List<String[]> fresh = new ArrayList<>();
        for (String line : lines.subList(seen, lines.size())) {
            int sp = line.indexOf(' ');
            String method = sp < 0 ? line : line.substring(0, sp);
            String trace = sp < 0 ? "" : line.substring(sp + 1);
            if (!method.startsWith("Watch")) {
                fresh.add(new String[] {method, trace});
            }
        }
        seen = lines.size();
        return fresh;
    }

    private static void check(String name, String form, List<String> wantCalls, String wantTrace) throws Exception {
        List<String[]> got = take();
        List<String> methods = new ArrayList<>();
        for (String[] g : got) {
            methods.add(g[0]);
        }
        if (!methods.equals(wantCalls)) {
            failures.add(name + " (" + form + "): sent " + methods + ", want " + wantCalls);
            return;
        }
        for (String[] g : got) {
            if (!g[1].equals(wantTrace)) {
                failures.add(name + " (" + form + "): " + g[0] + " carried trace_id \"" + g[1] + "\", want \"" + wantTrace + "\"");
            }
        }
    }

    private static int cases;

    // runCase calls the traced overload with a trace and today's form (a null
    // trace), each answered by the recorder's empty message.
    private static void runCase(String name, List<String> wantCalls, Call call) throws Exception {
        String trace = "t-" + name;
        call.run(trace);
        check(name, "traced", wantCalls, trace);
        call.run(null);
        check(name, "untraced", wantCalls, "");
        cases++;
    }

    public static void main(String[] args) throws Exception {
        String addr = System.getenv("TRACE_HELPERS_ADDR");
        log = Path.of(System.getenv("TRACE_HELPERS_LOG"));
        ManagedChannel channel = ManagedChannelBuilder.forTarget(addr).usePlaintext().build();
        try {
            StateClient state = new StateClient();
            state.setConfigFetchTimeoutSeconds(1);
            state.setChannel(channel, null);
            Thread.sleep(500);
            take();
            Tracehelpers.Player player = Tracehelpers.Player.newBuilder().setPlayerId("p-1").setTraceId("stored-value-trace").build();
            runCase("fetchSettings", List.of("GetSettings"),
                tr -> { if (tr == null) state.fetchSettings(); else state.fetchSettings(tr); });
            runCase("fetchAllPlayers", List.of("GetAllPlayers"),
                tr -> { if (tr == null) state.fetchAllPlayers(); else state.fetchAllPlayers(tr); });
            runCase("putPlayers", List.of("PutPlayers"),
                tr -> { if (tr == null) state.putPlayers("p-1", player); else state.putPlayers("p-1", player, tr); });
            runCase("deletePlayers", List.of("DeletePlayers"),
                tr -> { if (tr == null) state.deletePlayers("p-1"); else state.deletePlayers("p-1", tr); });
            state.close();
            System.out.println("trace-helpers java state: " + cases + " cases");

            cases = 0;
            HelperRecordMapGrpcClient client = new HelperRecordMapGrpcClient(channel, () -> "token");
            HelperRowRecordReader reader = new HelperRowRecordReader(client);
            Helperquery.HelperRowFilter filter = Helperquery.HelperRowFilter.getDefaultInstance();
            runCase("get", List.of("GetHelperRowRows"),
                tr -> { if (tr == null) reader.get("r-1"); else reader.get("r-1", tr); });
            runCase("list", List.of("CreateHelperRowQuery", "ListHelperRow", "DeleteHelperRowQuery"),
                tr -> { if (tr == null) reader.list(filter, 10, ""); else reader.list(filter, 10, "", tr); });
            System.out.println("trace-helpers java reader: " + cases + " cases");
        } finally {
            channel.shutdownNow().awaitTermination(5, TimeUnit.SECONDS);
        }

        int checked = 0;
        for (String outer : Files.readAllLines(Path.of(args[0]), StandardCharsets.UTF_8)) {
            Descriptors.FileDescriptor file = (Descriptors.FileDescriptor) Class.forName(outer).getMethod("getDescriptor").invoke(null);
            if (!file.getName().startsWith("sys/") || file.getName().equals("sys/spec.proto")) {
                continue;
            }
            for (Descriptors.Descriptor message : file.getMessageTypes()) {
                if (message.getFullName().equals("trace.TraceEvent")) {
                    continue;
                }
                Descriptors.FieldDescriptor field = message.findFieldByNumber(200);
                if (field == null || !field.getName().equals("trace_id") || field.getType() != Descriptors.FieldDescriptor.Type.STRING) {
                    failures.add(message.getFullName() + " (" + file.getName() + ") has no string trace_id = 200");
                }
                checked++;
            }
        }
        if (checked < 100) {
            failures.add("checked " + checked + " sys messages; the sys outer classes were not all listed");
        }
        System.out.println("trace-helpers java sys: " + checked + " messages");

        if (!failures.isEmpty()) {
            for (String f : failures) {
                System.err.println("FAIL " + f);
            }
            System.exit(1);
        }
    }
}
