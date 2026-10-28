// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTenantRouteCSharpBehaviour compiles the generated C# client for a
// tenant-routed fixture with dotnet and runs it: every C2S call shape (blocking
// and async unary, server streaming, client streaming, bidi) and the W2S unary
// call refuse an empty or malformed route field before anything is sent, a tid
// passes through, a stream refuses a message naming a second tenant, and a
// platform rpc is untouched. Skipped when dotnet is absent, under -short, or
// when the NuGet package restore fails (offline).
func TestTenantRouteCSharpBehaviour(t *testing.T) {
	if testing.Short() {
		t.Skip("dotnet build of generated C# skipped under -short")
	}
	dotnet, err := exec.LookPath("dotnet")
	if err != nil {
		t.Skip("dotnet not installed")
	}

	lib := generateTenantRoutedFixture(t)
	// Workaround for a pre-existing bug outside this package, unrelated to
	// tenant routing: every unary user rpc of a c2s service is auto-exposed as an
	// MCP tool, which pulls sys/mcp.proto into the closure, and
	// proto.StripSpecOptionsFromDir keeps its `import "sys/spec.proto";` because
	// a COMMENT in mcp.proto mentions "(spec.rpc)" (contentNeedsSpecProto
	// matches "spec." on comment lines), while the C# closure never ships
	// sys/spec.proto, so protoc fails. Drop the dangling import here so the probe
	// exercises the tenant-routing code.
	mcpProto := filepath.Join(lib, "Protos", "sys", "mcp.proto")
	if b, err := os.ReadFile(mcpProto); err == nil {
		fixed := strings.Replace(string(b), "import \"sys/spec.proto\";\n", "", 1)
		if err := os.WriteFile(mcpProto, []byte(fixed), 0644); err != nil {
			t.Fatalf("rewrite mcp.proto: %v", err)
		}
	}
	app := filepath.Join(t.TempDir(), "probe")
	if err := os.MkdirAll(app, 0755); err != nil {
		t.Fatalf("mkdir probe: %v", err)
	}
	project := `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <OutputType>Exe</OutputType>
    <TargetFramework>net8.0</TargetFramework>
    <RollForward>Major</RollForward>
    <Nullable>enable</Nullable>
    <ImplicitUsings>enable</ImplicitUsings>
    <TreatWarningsAsErrors>true</TreatWarningsAsErrors>
  </PropertyGroup>
  <ItemGroup>
    <ProjectReference Include="` + filepath.Join(lib, "DockClient.csproj") + `" />
  </ItemGroup>
</Project>
`
	if err := os.WriteFile(filepath.Join(app, "probe.csproj"), []byte(project), 0644); err != nil {
		t.Fatalf("write probe.csproj: %v", err)
	}
	if err := os.WriteFile(filepath.Join(app, "Program.cs"), []byte(tenantRouteProbeProgram), 0644); err != nil {
		t.Fatalf("write Program.cs: %v", err)
	}

	env := append(os.Environ(),
		"DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"DOTNET_NOLOGO=1",
		"DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1",
	)
	run := func(timeout time.Duration, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, dotnet, args...)
		cmd.Dir = app
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := run(5*time.Minute, "restore", "--nologo"); err != nil {
		t.Skipf("dotnet restore failed (offline?); skipping:\n%s", out)
	}
	// The generated library builds as part of the probe. Any compiler warning
	// fails the test, except the missing-XML-doc warnings (CS1591) W2SClient.cs
	// already carried on its constructors before tenant routing.
	buildOut, err := run(5*time.Minute, "build", "--nologo", "--no-restore")
	if err != nil {
		t.Fatalf("dotnet build of generated C# + probe failed: %v\n%s", err, buildOut)
	}
	for _, line := range strings.Split(buildOut, "\n") {
		if !strings.Contains(line, ": warning ") {
			continue
		}
		if strings.Contains(line, "W2SClient.cs(") && strings.Contains(line, "warning CS1591: Missing XML comment") {
			continue
		}
		t.Errorf("dotnet build warning: %s", strings.TrimSpace(line))
	}
	out, err := run(2*time.Minute, "run", "--no-build", "--no-restore")
	t.Logf("probe output:\n%s", out)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if !strings.Contains(out, "PROBE OK") {
		t.Fatalf("probe did not finish")
	}
}

const tenantRouteProbeProgram = `using System.Net;
using Grpc.Core;
using DockClient;

var failures = 0;
void Expect(bool ok, string what)
{
    Console.WriteLine((ok ? "ok   " : "FAIL ") + what);
    if (!ok) failures++;
}

// Not tids: empty, upper case, short, long, punctuation, trailing newline (a
// .NET Regex "$" would accept it), non-ASCII digits (\d would accept them),
// surrounding space (no trimming).
var malformed = new[]
{
    "", "ABCDEFGH", "abc", "abcdefghi", "abcdefg!", "abcdefg\n", "abcdefg٣",
    "abcdefg１", " aaaaaaa", "aaaaaaa ", "aaaaaaaA",
};
var valid = new[] { "aaaaaaaa", "0123abcz" };

// W2S: a refused call never reaches the HTTP handler.
var handler = new RecordingHandler();
var w2s = new CrewServiceW2SClient(new W2SClient(
    "http://127.0.0.1:1",
    _ => Task.FromResult("token"),
    new HttpClient(handler)));
foreach (var tid in malformed)
{
    var before = handler.Count;
    try
    {
        await w2s.GetSkipperAsync(new Crew.GetSkipperRequest { Tenant = tid });
        Expect(false, $"W2S refuses {Show(tid)}");
    }
    catch (W2SClientException ex)
    {
        Expect(ex.Message.Contains("route field 'tenant'") && ex.StatusCode == null && handler.Count == before,
            $"W2S refuses {Show(tid)} before sending: {ex.Message}");
    }
}
foreach (var tid in valid)
{
    var before = handler.Count;
    await w2s.GetSkipperAsync(new Crew.GetSkipperRequest { Tenant = tid });
    Expect(handler.Count == before + 1, $"W2S sends {Show(tid)}");
}
try
{
    await w2s.SendMessageAsync(new Crew.SendMessageRequest { Text = "hi", TargetTenant = "" });
    Expect(false, "W2S refuses an empty target_tenant");
}
catch (W2SClientException ex)
{
    Expect(ex.Message.Contains("route field 'target_tenant'"), $"W2S names target_tenant: {ex.Message}");
}
var pierW2S = new PierServiceW2SClient(new W2SClient("http://127.0.0.1:1", _ => Task.FromResult("token"), new HttpClient(handler)));
var pierBefore = handler.Count;
await pierW2S.GetPierAsync(new Pier.GetPierRequest { Name = "" });
Expect(handler.Count == pierBefore + 1, "W2S platform rpc sends unchecked");

// C2S: nothing listens on 127.0.0.1:1, so a call that gets past the check fails
// UNAVAILABLE; a refused one fails INVALID_ARGUMENT naming the field.
using var c2s = new C2SClient("127.0.0.1", 1, useTls: false);
var crew = c2s.WithCrewService();
bool Refused(RpcException ex, string field) =>
    ex.StatusCode == StatusCode.InvalidArgument && ex.Status.Detail.Contains($"route field '{field}'");

foreach (var tid in malformed)
{
    try { await crew.GetSkipperAsync(new Crew.GetSkipperRequest { Tenant = tid }); Expect(false, $"C2S async unary refuses {Show(tid)}"); }
    catch (RpcException ex) { Expect(Refused(ex, "tenant"), $"C2S async unary refuses {Show(tid)}: {ex.Status}"); }

    try { crew.GetSkipper(new Crew.GetSkipperRequest { Tenant = tid }); Expect(false, $"C2S blocking unary refuses {Show(tid)}"); }
    catch (RpcException ex) { Expect(Refused(ex, "tenant"), $"C2S blocking unary refuses {Show(tid)}: {ex.Status}"); }

    try
    {
        using var call = crew.GenerateRoster(new Crew.GetSkipperRequest { Tenant = tid });
        await call.ResponseStream.MoveNext();
        Expect(false, $"C2S server streaming refuses {Show(tid)}");
    }
    catch (RpcException ex) { Expect(Refused(ex, "tenant"), $"C2S server streaming refuses {Show(tid)}: {ex.Status}"); }
}
foreach (var tid in valid)
{
    try { await crew.GetSkipperAsync(new Crew.GetSkipperRequest { Tenant = tid }); Expect(false, $"C2S passes {Show(tid)} (expected UNAVAILABLE)"); }
    catch (RpcException ex) { Expect(ex.StatusCode == StatusCode.Unavailable, $"C2S passes {Show(tid)} to the transport: {ex.Status}"); }
}

// Client streaming and bidi: every message is checked before it is written;
// the first admitted message pins the stream's tenant.
async Task Stream(string name, IClientStreamWriter<Crew.SendMessageRequest> writer)
{
    foreach (var tid in malformed)
    {
        try { await writer.WriteAsync(new Crew.SendMessageRequest { TargetTenant = tid }); Expect(false, $"{name} refuses {Show(tid)}"); }
        catch (RpcException ex) { Expect(Refused(ex, "target_tenant"), $"{name} refuses {Show(tid)}: {ex.Status}"); }
    }
    try { await writer.WriteAsync(new Crew.SendMessageRequest { TargetTenant = "aaaaaaaa" }); }
    catch (Exception ex) when (ex is not RpcException { StatusCode: StatusCode.InvalidArgument })
    {
        // The transport may fail the write (nothing listens); the check passed.
    }
    catch (RpcException ex) { Expect(false, $"{name} admits a tid: {ex.Status}"); }
    try { await writer.WriteAsync(new Crew.SendMessageRequest { TargetTenant = "bbbbbbbb" }); Expect(false, $"{name} refuses a second tenant"); }
    catch (RpcException ex)
    {
        Expect(ex.StatusCode == StatusCode.InvalidArgument && ex.Status.Detail.Contains("pinned to tenant 'aaaaaaaa'"),
            $"{name} refuses a second tenant: {ex.Status}");
    }
    try { await writer.WriteAsync(new Crew.SendMessageRequest { TargetTenant = "aaaaaaaa" }, CancellationToken.None); }
    catch (RpcException ex) when (ex.StatusCode == StatusCode.InvalidArgument) { Expect(false, $"{name} admits the pinned tenant again: {ex.Status}"); }
    catch (Exception) { }
}
using (var call = crew.ExecuteBatch()) { await Stream("C2S client streaming", call.RequestStream); }
using (var call = crew.SendChat()) { await Stream("C2S bidi", call.RequestStream); }

// A platform service keeps the plain channel: no route check.
try { await c2s.WithPierService().GetPierAsync(new Pier.GetPierRequest()); Expect(false, "C2S platform rpc (expected UNAVAILABLE)"); }
catch (RpcException ex) { Expect(ex.StatusCode == StatusCode.Unavailable, $"C2S platform rpc unchecked: {ex.Status}"); }

static string Show(string s) => "\"" + string.Concat(s.Select(c => c < 0x20 || c > 0x7e ? $"\\u{(int)c:x4}" : c.ToString())) + "\"";

if (failures > 0)
{
    Console.WriteLine($"{failures} failure(s)");
    return 1;
}
Console.WriteLine("PROBE OK");
return 0;

sealed class RecordingHandler : HttpMessageHandler
{
    public int Count;
    protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    {
        Interlocked.Increment(ref Count);
        return Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK) { Content = new StringContent("{}") });
    }
}
`
