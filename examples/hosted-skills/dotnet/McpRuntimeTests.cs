#pragma warning disable MAAI001 // Preview SDK invocation context required for provider assertions.

using System.Buffers.Binary;
using System.Collections.Concurrent;
using System.IO.Compression;
using System.Net;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using FAM.Skills;
using Microsoft.Agents.AI;
using Microsoft.Extensions.AI;

internal static class McpRuntimeTests
{
    private const string Resource = "skill://greeting/1/skill.zip";
    private const string MarkdownResource = "skill://greeting/1/SKILL.md";
    private const string Description = "An offline MCP test Skill.";
    private const string Document = "---\nname: greeting\ndescription: " + Description +
        "\nallowed-tools: unrelated\n---\nInclude FAM_DOTNET_SKILL_LOADED when greeting.\n";
    private const string Schema = "https://schemas.agentskills.io/discovery/0.2.0/schema.json";

    internal static async Task RunAsync(Func<string, Func<Task>, Task> run)
    {
        await run("real UseMcpSkills advertises/loads, filters before downloads and cleans up", RoundTripAsync);
        await run("MCP SSE responses use the same validation boundary", SseAsync);
        await run("real UseMcpSkills skill-md advertises/loads exact text without archive extraction", SkillMarkdownAsync);
        await run("MCP skill-md pins instruction and advertised text digests", SkillMarkdownDigestsAsync);
        await run("MCP skill-md requires exactly one matching text resource", SkillMarkdownResponsesAsync);
        await run("MCP skill-md rejects invalid UTF-8 and unpaired Unicode surrogates", SkillMarkdownEncodingAsync);
        await run("MCP skill-md validates selected type, identity and instructions-only metadata", SkillMarkdownMetadataAsync);
        await run("MCP notifications accept only empty 202 or service-observed 204 acknowledgements", NotificationAcknowledgementsAsync);
        await run("MCP notifications reject other statuses and bounded body-bearing acknowledgements", InvalidNotificationAcknowledgementsAsync);
        await run("MCP negotiates initialize revisions without a hard-coded version", NegotiationAsync);
        await run("malformed MCP negotiation errors cannot fall back into readiness", InvalidNegotiationAsync);
        await run("MCP discovery probe timeout falls back to initialize", ProbeTimeoutAsync);
        await run("empty MCP inventory never requests credentials or HTTP", EmptyAsync);
        await run("MCP baseline lock works without index version/archive digest", BaselineLockAsync);
        await run("MCP does not apply the Skill-name quota to Toolbox names", ToolboxNameAsync);
        await run("MCP overrides SDK one-MiB extraction defaults", LargeArchiveAsync);
        await run("MCP does not bypass unrelated tool approvals", ApprovalAsync);
        await run("missing/empty/malformed/duplicate MCP indices fail readiness", InvalidIndicesAsync);
        await run("MCP archives reject extra entries, paths, links and corrupt bytes", InvalidArchivesAsync);
        await run("MCP pins content/archive digests and selected identities", InvalidDigestsAsync);
        await run("MCP transport rejects redirects and mismatched resource responses", InvalidResponsesAsync);
        await run("MCP cancellation cleans up the authenticated session", CancellationAsync);
        await run("MCP authentication and HTTP failures propagate", AuthenticationAsync);
        await run("MCP cleanup failures remain visible", CleanupFailureAsync);
        await run("MCP cleanup rejects pending and unexpected success statuses", CleanupAcknowledgementAsync);
        await run("MCP startup rejects changed manifest selection", ManifestChangeAsync);
        await run("MCP startup has a whole-operation deadline", StartupDeadlineAsync);
        await run("MCP retained load tools reject use after disposal", RetainedToolAsync);
        await run("MCP survives host lifetime and cleans up host failure", HostFailureAsync);
    }

    private static async Task RoundTripAsync()
    {
        using var fixture = new Fixture();
        await using var runtime = await fixture.OpenAsync();
        var cache = runtime.McpCacheDirectory!;
        Check.That(Directory.Exists(cache) && !fixture.Server.Disposed && fixture.Server.Deletes == 0,
            "The MCP session/cache must remain alive after startup.");
        var model = await ExerciseAsync(runtime);
        Check.That(!model.Advertisement.Contains("unselected", StringComparison.Ordinal) &&
            !model.Advertisement.Contains("UNTRUSTED_SERVER_INSTRUCTIONS", StringComparison.Ordinal),
            "The provider must advertise only selected Skills, not server instructions or unselected Skills.");
        Check.That(fixture.Server.ResourcesRead.SequenceEqual(new[] { "skill://index.json", Resource }),
            "Unselected archives must be removed from discovery before the SDK can request them.");
        Check.That(model.LoadedContent!.StartsWith(Document, StringComparison.Ordinal),
            "Actual MCP load_skill must return the validated instructions.");
        await runtime.DisposeAsync();
        await runtime.DisposeAsync();
        Check.That(fixture.Server.Deletes == 1 && fixture.Server.Disposed && !Directory.Exists(cache),
            "Shutdown must delete exactly this session/cache and dispose its transport once.");
        fixture.CheckRequests();
    }

    private static async Task SseAsync()
    {
        using var fixture = new Fixture();
        fixture.Server.Sse = true;
        await using var runtime = await fixture.OpenAsync();
        await ExerciseAsync(runtime);
    }

    private static async Task BaselineLockAsync()
    {
        using var fixture = new Fixture();
        fixture.Entry.Remove("archiveSHA256");
        fixture.Server.Selected.Remove("digest");
        fixture.Save();
        await using var runtime = await fixture.OpenAsync();
        await ExerciseAsync(runtime);
    }

    private static async Task SkillMarkdownAsync()
    {
        var text = Document.Replace("\n", "\r\n") + "\r\nUTF-8: \u00e9 \U0001f680.\r\n";
        foreach (var sse in new[] { false, true })
        foreach (var digest in new[] { "present", "omitted", "null" })
        {
            using var fixture = new Fixture(text, skillMd: true);
            fixture.Server.Sse = sse;
            fixture.Server.NotificationStatus = HttpStatusCode.NoContent;
            var uri = sse ? "https://opaque.invalid/greeting/1/SKILL.md" : MarkdownResource;
            fixture.Server.Selected["url"] = uri;
            if (digest == "omitted") fixture.Server.Selected.Remove("digest");
            if (digest == "null") fixture.Server.Selected["digest"] = null;
            fixture.Entry["archiveSHA256"] = new string('a', 64);
            fixture.Save();
            await using var runtime = await fixture.OpenAsync();
            var cache = runtime.McpCacheDirectory!;
            Check.That(!Directory.EnumerateFileSystemEntries(cache).Any(),
                "skill-md must use the actual SDK text loader, not create or extract a fake archive.");
            fixture.Server.SkillMarkdown = "A changed logical default must not replace cached pinned instructions.";
            var model = await ExerciseAsync(runtime);
            Check.That(model.LoadedContent!.StartsWith(text, StringComparison.Ordinal) &&
                !model.Advertisement.Contains("unselected", StringComparison.Ordinal),
                "Actual skill-md loading must preserve locked CRLF/UTF-8 text and selected-only advertising.");
            Check.That(fixture.Server.ResourcesRead.SequenceEqual(new[] { "skill://index.json", uri }),
                "Only the selected text URI may be read; a URL remains an opaque MCP argument.");
            await runtime.DisposeAsync();
            Check.That(fixture.Server.Deletes == 1 && fixture.Server.Disposed && !Directory.Exists(cache),
                "Direct Skill loading must preserve session/cache ownership and cleanup.");
            fixture.CheckRequests();
        }
    }

    private static async Task SkillMarkdownDigestsAsync()
    {
        foreach (var mode in new[] { "lock", "advertised", "archive-digest", "malformed", "changed-content" })
        {
            using var fixture = new Fixture(skillMd: true);
            switch (mode)
            {
                case "lock": fixture.Entry["sha256"] = new string('a', 64); break;
                case "advertised": fixture.Server.Selected["digest"] = "sha256:" + new string('a', 64); break;
                case "archive-digest": fixture.Server.Selected["digest"] = "sha256:" + fixture.Entry["archiveSHA256"]; break;
                case "malformed": fixture.Server.Selected["digest"] = "sha256:short"; break;
                case "changed-content": fixture.Server.SkillMarkdown = Document + "Unpinned version 2."; break;
            }
            fixture.Save();
            await RejectAsync(fixture);
        }
    }

    private static async Task SkillMarkdownResponsesAsync()
    {
        foreach (var failure in new[]
        {
            "md-blob", "md-both", "md-missing-text", "md-non-string", "md-empty",
            "md-wrong-uri", "md-extra-block", "md-no-block", "md-duplicate-json"
        })
        {
            using var fixture = new Fixture(skillMd: true);
            fixture.Server.Failure = failure;
            await RejectAsync(fixture);
            fixture.CheckRequests();
        }
    }

    private static async Task SkillMarkdownEncodingAsync()
    {
        foreach (var failure in new[] { "md-invalid-utf8", "md-invalid-surrogate" })
        {
            using var fixture = new Fixture(Document + "\uFFFD", skillMd: true);
            fixture.Server.Failure = failure;
            await RejectAsync(fixture);
        }
    }

    private static async Task SkillMarkdownMetadataAsync()
    {
        foreach (var text in new[]
        {
            Document.Replace("name: greeting", "name: unexpected"),
            Document.Replace(Description, "Changed description."),
            Document.Replace("name: greeting", "name: greeting\nname: greeting"),
            Document.Replace("allowed-tools: unrelated", "resources:\n  - notes.md"),
            Document.Replace("allowed-tools: unrelated", "scripts:\n  - run.py"),
            Document.Replace("allowed-tools: unrelated", "metadata:\n  - not-a-mapping"),
            Document.Replace("allowed-tools: unrelated", "metadata:\n  nested:\n    value: unsupported"),
            Document.Replace("allowed-tools: unrelated", "metadata:\n  key: first\n  key: duplicate"),
            Document.Replace("allowed-tools: unrelated", "allowed-tools:\n  - unrelated"),
            Document + "\0"
        })
        {
            using var fixture = new Fixture(text, skillMd: true);
            await RejectAsync(fixture);
        }
        foreach (var field in new[] { "resources", "scripts", "allowed-tools", "metadata" })
        {
            using var fixture = new Fixture(skillMd: true);
            fixture.Server.Selected[field] = new[] { "must-not-be-silently-dropped" };
            await RejectAsync(fixture);
            Check.That(!fixture.Server.ResourcesRead.Contains(MarkdownResource),
                "Unsupported selected metadata must fail before fetching instructions.");
        }
        foreach (var type in new[] { "mcp-resource-template", "markdown", "SKILL-MD" })
        {
            using var fixture = new Fixture(skillMd: true);
            fixture.Server.Selected["type"] = type;
            await RejectAsync(fixture);
        }
    }

    private static async Task NotificationAcknowledgementsAsync()
    {
        foreach (var status in new[] { HttpStatusCode.Accepted, HttpStatusCode.NoContent })
        foreach (var unknownLength in new[] { false, true })
        {
            using var fixture = new Fixture();
            fixture.Server.NotificationStatus = status;
            fixture.Server.NotificationUnknownLength = unknownLength;
            await using var runtime = await fixture.OpenAsync();
            await ExerciseAsync(runtime);
            await runtime.DisposeAsync();
            if (unknownLength)
                Check.That(fixture.Server.NotificationStream is { MaxReadSize: 1, BytesRead: 0 },
                    "Unknown-length acknowledgements must be probed for EOF, not discarded without reading.");
            Check.That(fixture.Server.Deletes == 1 && fixture.Server.Disposed,
                "Both acknowledgement statuses must preserve actual Skill loading and session cleanup.");
            fixture.CheckRequests();
        }
    }

    private static async Task InvalidNotificationAcknowledgementsAsync()
    {
        foreach (var status in new[] { HttpStatusCode.OK, HttpStatusCode.Created, HttpStatusCode.ResetContent, HttpStatusCode.PartialContent })
        {
            using var fixture = new Fixture();
            fixture.Server.NotificationStatus = status;
            await RejectNotificationAsync(fixture);
        }
        foreach (var status in new[] { HttpStatusCode.Accepted, HttpStatusCode.NoContent })
        foreach (var length in new[] { "known", "unknown", "declared-empty" })
        {
            using var fixture = new Fixture();
            fixture.Server.NotificationStatus = status;
            fixture.Server.NotificationBody = new string(' ', 64 * 1024);
            fixture.Server.NotificationUnknownLength = length != "known";
            fixture.Server.NotificationDeclaredEmpty = length == "declared-empty";
            await RejectNotificationAsync(fixture);
            if (length != "known")
                Check.That(fixture.Server.NotificationStream is { MaxReadSize: 1, BytesRead: 1 },
                    "A body-bearing acknowledgement must fail after one byte, including a false Content-Length: 0.");
        }
    }

    private static async Task RejectNotificationAsync(Fixture fixture)
    {
        await ExpectFailureAsync(async () => { await using var runtime = await fixture.OpenAsync(); },
            error => error is InvalidDataException);
        Check.That(fixture.Server.ResourcesRead.IsEmpty && fixture.Server.Deletes == 1 && fixture.Server.Disposed,
            "An invalid acknowledgement must fail before Skill reads and still close its initialized session.");
    }

    private static async Task NegotiationAsync()
    {
        foreach (var version in new[] { "2025-03-26", "2025-06-18", "2025-11-25" })
        {
            using var fixture = new Fixture();
            fixture.Server.ProtocolVersion = version;
            await using var runtime = await fixture.OpenAsync();
            await ExerciseAsync(runtime);
            await runtime.DisposeAsync();
            Check.That(fixture.Server.Methods.Contains("server/discover") &&
                fixture.Server.Methods.Contains("initialize") && fixture.Server.Methods.Contains("notifications/initialized"),
                "The actual SDK must negotiate its probe into the documented initialize/initialized handshake.");
            Check.That(fixture.Server.CleanupProtocolVersion == version,
                "Session cleanup must use the server's negotiated revision, not a fixed client revision.");
            fixture.CheckRequests();
        }
    }

    private static async Task ProbeTimeoutAsync()
    {
        using var fixture = new Fixture();
        fixture.Server.BlockDiscovery = true;
        await using var runtime = await fixture.OpenAsync();
        await ExerciseAsync(runtime);
        Check.That(fixture.Server.DiscoveryCancelled && fixture.Server.Methods.Contains("initialize"),
            "The SDK's actual bounded probe must cancel and fall back, not poison successful Skill readiness.");
    }

    private static async Task InvalidNegotiationAsync()
    {
        using var fixture = new Fixture();
        fixture.Server.Failure = "duplicate-probe-json";
        await ExpectFailureAsync(async () => { await using var runtime = await fixture.OpenAsync(); },
            error => error is InvalidDataException or JsonException);
        Check.That(fixture.Server.ResourcesRead.IsEmpty && fixture.Server.Deletes == 0 && fixture.Server.Disposed,
            "Malformed negotiation data must abort before initialization or Skill downloads, then dispose transport.");
    }

    private static async Task EmptyAsync()
    {
        using var fixture = new Fixture();
        fixture.Detach();
        await using var runtime = await FamSkillsRuntime.OpenCoreAsync(fixture.Path, RuntimeTests.Project,
            CancellationToken.None, _ => throw new InvalidOperationException("Detached Skills must not request a token."),
            fixture.Server);
        using var model = new RecordingModel("greeting", RuntimeTests.Marker);
        var agent = new ChatClientAgent(model, new ChatClientAgentOptions());
        var context = await runtime.Provider.InvokingAsync(
            new AIContextProvider.InvokingContext(agent, null, new AIContext()));
        Check.That(context.Tools is null || !context.Tools.Any(), "Detached MCP Skills must expose no tools.");
        Check.That(fixture.Server.Requests.IsEmpty && runtime.McpCacheDirectory is null,
            "Explicit detach must not initialize a client or extraction cache.");
    }

    private static async Task LargeArchiveAsync()
    {
        var bytes = new byte[2 * 1024 * 1024];
        new Random(123).NextBytes(bytes);
        using var fixture = new Fixture(Document + Convert.ToBase64String(bytes));
        Check.That(fixture.Server.Archive.Length > 1024 * 1024, "Fixture must exceed both SDK one-MiB defaults.");
        await using var runtime = await fixture.OpenAsync();
        await ExerciseAsync(runtime);
    }

    private static async Task ToolboxNameAsync()
    {
        var name = new string('t', 65);
        using var fixture = new Fixture(toolboxName: name);
        await using var runtime = await fixture.OpenAsync();
        await ExerciseAsync(runtime);
        Check.That(fixture.Server.Requests.All(r => r.Uri ==
            RuntimeTests.Project + "/toolboxes/" + name + "/versions/7/mcp?api-version=v1"),
            "Toolbox names must remain pinned without inventing an unrelated 64-character quota.");
    }

    private static async Task ApprovalAsync()
    {
        using var fixture = new Fixture();
        await using var runtime = await fixture.OpenAsync();
        var invoked = false;
        using var model = new RecordingModel("greeting", RuntimeTests.Marker, requestUnrelated: true);
        var agent = new ChatClientAgent(model, new ChatClientAgentOptions
        {
            AIContextProviders = [runtime.Provider],
            ChatOptions = new ChatOptions
            {
                Tools = [new ApprovalRequiredAIFunction(AIFunctionFactory.Create(
                    () => { invoked = true; return "unrelated"; }, "unrelated"))]
            }
        });
        var result = await agent.RunAsync("Load a Skill and request an unrelated tool.");
        Check.That(model.Loaded && !invoked &&
            result.Messages.SelectMany(m => m.Contents).OfType<ToolApprovalRequestContent>().Any(),
            "Only load_skill can bypass approval; metadata cannot grant unrelated tools.");
    }

    private static async Task InvalidIndicesAsync()
    {
        foreach (var text in new[]
        {
            "", "not-json", "{}", "{\"skills\":[]}", "{\"skills\":null}",
            "{\"skills\":[],\"skills\":[]}",
            "{\"skills\":[],\"extra\":{\"key\":1,\"key\":2}}",
            JsonSerializer.Serialize(new { skills = new[] { new { name = "unselected", type = "archive" } } })
        })
        {
            using var fixture = new Fixture();
            fixture.Server.IndexOverride = text;
            await RejectAsync(fixture);
            Check.That(!fixture.Server.ResourcesRead.Contains(Resource), "Bad indices must fail before archive download.");
        }
        using var missing = new Fixture();
        missing.Server.MissingIndex = true;
        await RejectAsync(missing);
        using var duplicate = new Fixture();
        duplicate.Server.IndexOverride = JsonSerializer.Serialize(new { skills = new[] { duplicate.Server.Selected, duplicate.Server.Selected } });
        await RejectAsync(duplicate);
        using var unsupported = new Fixture();
        unsupported.Server.Selected["type"] = "mcp-resource-template";
        await RejectAsync(unsupported);
    }

    private static async Task InvalidArchivesAsync()
    {
        foreach (var name in new[] { "../SKILL.md", "/SKILL.md", "C:/SKILL.md", "greeting/SKILL.md", "SKILL.md:stream" })
        {
            using var fixture = new Fixture(archive: Zip(Document, name));
            await RejectAsync(fixture);
        }
        foreach (var extra in new[] { "run.py", "notes.md", "SKILL.md", "skill.md", "nested/" })
        {
            using var fixture = new Fixture(archive: Zip(Document, extra: extra));
            await RejectAsync(fixture);
        }
        foreach (var mode in new[] { 0xa1ff0000u, 0x41ed0010u, 0x21a40000u })
        {
            using var fixture = new Fixture(archive: Zip(Document, attributes: unchecked((int)mode)));
            await RejectAsync(fixture);
        }
        var corruptCrc = Zip(Document);
        var central = FindCentralDirectory(corruptCrc);
        corruptCrc[central + 16] ^= 1;
        using (var fixture = new Fixture(archive: corruptCrc)) await RejectAsync(fixture);
        var oversized = Zip(Document);
        BinaryPrimitives.WriteUInt32LittleEndian(oversized.AsSpan(FindCentralDirectory(oversized) + 24, 4),
            FamSkillsRuntime.MaxSkillBytes + 1u);
        using (var fixture = new Fixture(archive: oversized)) await RejectAsync(fixture);
        var encrypted = Zip(Document);
        encrypted[FindCentralDirectory(encrypted) + 8] |= 1;
        using (var fixture = new Fixture(archive: encrypted)) await RejectAsync(fixture);
        using (var fixture = new Fixture(archive: Zip(Document)[..^12])) await RejectAsync(fixture);
        using (var fixture = new Fixture(archive: Encoding.UTF8.GetBytes("not a ZIP"))) await RejectAsync(fixture);
        using (var fixture = new Fixture(archive: Zip(Document, empty: true))) await RejectAsync(fixture);
    }

    private static async Task InvalidDigestsAsync()
    {
        using (var fixture = new Fixture())
        {
            fixture.Entry["sha256"] = new string('a', 64);
            fixture.Save();
            await RejectAsync(fixture);
        }
        using (var fixture = new Fixture())
        {
            fixture.Entry["archiveSHA256"] = new string('a', 64);
            fixture.Save();
            await RejectAsync(fixture);
        }
        foreach (var digest in new[] { "md5:bad", "sha256:short", "sha256:" + new string('a', 64) })
        {
            using var fixture = new Fixture();
            fixture.Server.Selected["digest"] = digest;
            await RejectAsync(fixture);
        }
        using (var fixture = new Fixture(Document.Replace("name: greeting", "name: unexpected"))) await RejectAsync(fixture);
        using (var fixture = new Fixture(Document.Replace("name: greeting", "name: greeting\nname: greeting"))) await RejectAsync(fixture);
        using (var fixture = new Fixture())
        {
            fixture.Server.Selected["description"] = "Not the locked description.";
            await RejectAsync(fixture);
        }
    }

    private static async Task InvalidResponsesAsync()
    {
        foreach (var mode in new[] { "redirect", "wrong-uri", "extra-block", "bad-base64", "server-request", "wrong-id", "duplicate-json" })
        {
            using var fixture = new Fixture();
            fixture.Server.Failure = mode;
            await RejectAsync(fixture);
            fixture.CheckRequests();
        }
    }

    private static async Task CancellationAsync()
    {
        using var fixture = new Fixture();
        fixture.Server.BlockIndex = true;
        using var cancellation = new CancellationTokenSource();
        var opening = fixture.OpenAsync(cancellation.Token);
        await Task.WhenAny(opening, fixture.Server.IndexStarted.Task);
        if (!fixture.Server.IndexStarted.Task.IsCompleted)
        {
            await using var unexpected = await opening;
            throw new InvalidOperationException("Readiness completed without requesting the required index.");
        }
        cancellation.Cancel();
        await ExpectFailureAsync(async () => { await using var runtime = await opening; },
            error => error is OperationCanceledException);
        Check.That(fixture.Server.Deletes == 1 && fixture.Server.Disposed,
            "Cancelled readiness must dispose transport and close its session with a fresh non-cancelled token.");
    }

    private static async Task AuthenticationAsync()
    {
        using (var fixture = new Fixture())
        {
            fixture.Server.Unauthorized = true;
            await ExpectFailureAsync(async () => { await using var runtime = await fixture.OpenAsync(); },
                error => error is HttpRequestException { StatusCode: HttpStatusCode.Unauthorized });
            Check.That(fixture.Server.Disposed, "Authentication failure must dispose the transport.");
        }
        using (var fixture = new Fixture())
        {
            await ExpectFailureAsync(async () =>
            {
                await using var runtime = await FamSkillsRuntime.OpenCoreAsync(fixture.Path, RuntimeTests.Project,
                    CancellationToken.None, _ => ValueTask.FromResult("bad token\r\n"), fixture.Server);
            }, error => error is InvalidDataException);
            Check.That(fixture.Server.Requests.Count == 0 && fixture.Server.Disposed,
                "An invalid token must never be sent.");
        }
        using (var fixture = new Fixture())
        {
            var failed = false;
            await ExpectFailureAsync(async () =>
            {
                await using var runtime = await FamSkillsRuntime.OpenCoreAsync(fixture.Path, RuntimeTests.Project,
                    CancellationToken.None, token =>
                    {
                        token.ThrowIfCancellationRequested();
                        if (!failed && fixture.Server.ResourcesRead.Contains("skill://index.json"))
                        {
                            failed = true;
                            throw new UnauthorizedAccessException("synthetic credential renewal failure");
                        }
                        return ValueTask.FromResult("renewed-token");
                    }, fixture.Server);
            }, error => error is UnauthorizedAccessException { Message: "synthetic credential renewal failure" });
            Check.That(failed && fixture.Server.Deletes == 1 && fixture.Server.Disposed,
                "Credential renewal errors must survive SDK discovery catches and still clean up the session.");
        }
    }

    private static async Task CleanupFailureAsync()
    {
        foreach (var failDispose in new[] { false, true })
        {
            using var fixture = new Fixture();
            await using var runtime = await fixture.OpenAsync();
            var cache = runtime.McpCacheDirectory!;
            fixture.Server.FailDelete = !failDispose;
            fixture.Server.FailDispose = failDispose;
            await ExpectFailureAsync(async () => await runtime.DisposeAsync(),
                error => failDispose
                    ? error is IOException { Message: "synthetic HTTP disposal failure" }
                    : error is HttpRequestException { StatusCode: HttpStatusCode.InternalServerError });
            Check.That(fixture.Server.Disposed && !Directory.Exists(cache),
                "A server or HTTP cleanup failure must not prevent remaining cache cleanup.");
        }
    }

    private static async Task HostFailureAsync()
    {
        using var fixture = new Fixture();
        string? cache = null;
        try
        {
            await using var runtime = await fixture.OpenAsync();
            cache = runtime.McpCacheDirectory;
            await ExerciseAsync(runtime);
            throw new InvalidOperationException("synthetic host failure");
        }
        catch (InvalidOperationException error) when (error.Message == "synthetic host failure") { }
        Check.That(cache is not null && !Directory.Exists(cache) && fixture.Server.Disposed && fixture.Server.Deletes == 1,
            "Host failure must end the long-lived session and remove only its owned cache.");
    }

    private static async Task CleanupAcknowledgementAsync()
    {
        foreach (var status in new[] { HttpStatusCode.Accepted, HttpStatusCode.Created, HttpStatusCode.PartialContent })
        {
            using var fixture = new Fixture();
            await using var runtime = await fixture.OpenAsync();
            var cache = runtime.McpCacheDirectory!;
            fixture.Server.DeleteStatus = status;
            await ExpectFailureAsync(async () => await runtime.DisposeAsync(),
                error => error is HttpRequestException failure && failure.StatusCode == status);
            Check.That(fixture.Server.Disposed && !Directory.Exists(cache),
                "An uncertain remote termination must remain visible while local resources are cleaned.");
        }
    }

    private static async Task ManifestChangeAsync()
    {
        using var fixture = new Fixture();
        await ExpectFailureAsync(async () =>
        {
            await using var runtime = await FamSkillsRuntime.OpenCoreAsync(fixture.Path, RuntimeTests.Project,
                CancellationToken.None, token =>
                {
                    token.ThrowIfCancellationRequested();
                    if (fixture.Server.ResourcesRead.Contains("skill://index.json")) fixture.Detach();
                    return ValueTask.FromResult("synthetic-renewed-token");
                }, fixture.Server);
        }, error => error is InvalidDataException { Message: var message } &&
            message.Contains("manifest changed", StringComparison.Ordinal));
        Check.That(fixture.Server.Deletes == 1 && fixture.Server.Disposed,
            "Manifest edits during discovery must fail readiness and close the original session.");
    }

    private static async Task StartupDeadlineAsync()
    {
        using var fixture = new Fixture();
        fixture.Server.BlockIndex = true;
        await ExpectFailureAsync(async () =>
        {
            await using var runtime = await fixture.OpenAsync(startupTimeout: TimeSpan.FromSeconds(1))
                .WaitAsync(TimeSpan.FromSeconds(5));
        }, error => error is OperationCanceledException);
        Check.That(fixture.Server.Disposed, "The startup deadline must dispose the pending MCP transport.");
    }

    private static async Task RetainedToolAsync()
    {
        using var fixture = new Fixture();
        await using var runtime = await fixture.OpenAsync();
        using var model = new RecordingModel("greeting", RuntimeTests.Marker);
        var agent = new ChatClientAgent(model);
        var context = await runtime.Provider.InvokingAsync(
            new AIContextProvider.InvokingContext(agent, null, new AIContext()));
        var load = context.Tools!.OfType<AIFunction>().Single();
        var parameter = load.JsonSchema.GetProperty("properties").EnumerateObject().Single().Name;
        await runtime.DisposeAsync();
        await ExpectFailureAsync(async () =>
            await load.InvokeAsync(new AIFunctionArguments { [parameter] = "greeting" }),
            error => error is ObjectDisposedException);
    }

    private static async Task<RecordingModel> ExerciseAsync(FamSkillsRuntime runtime)
    {
        using var model = new RecordingModel("greeting", RuntimeTests.Marker);
        var agent = new ChatClientAgent(model, new ChatClientAgentOptions { AIContextProviders = [runtime.Provider] });
        await agent.RunAsync("Load the selected Skill through the real MCP provider.");
        Check.That(model.Advertised && model.Loaded, "Actual UseMcpSkills advertisement/load was not completed.");
        return model;
    }

    private static async Task RejectAsync(Fixture fixture)
    {
        await ExpectFailureAsync(async () => { await using var runtime = await fixture.OpenAsync(); },
            error => error is InvalidDataException or JsonException);
        Check.That(fixture.Server.Deletes == 1 && fixture.Server.Disposed,
            "Failed MCP readiness must close its authenticated session and dispose transport.");
    }

    private static async Task ExpectFailureAsync(Func<Task> action, Func<Exception, bool> expected)
    {
        try { await action(); }
        catch (Exception error)
        {
            if (Matches(error, expected)) return;
            throw;
        }
        throw new InvalidOperationException("The invalid MCP operation unexpectedly succeeded.");
    }

    private static bool Matches(Exception error, Func<Exception, bool> expected) =>
        expected(error) || error is AggregateException aggregate && aggregate.InnerExceptions.Any(e => Matches(e, expected)) ||
        error.InnerException is not null && Matches(error.InnerException, expected);

    private static string Hash(byte[] bytes) => Convert.ToHexString(SHA256.HashData(bytes)).ToLowerInvariant();

    private static byte[] Zip(string text, string name = "SKILL.md", string? extra = null, int? attributes = null, bool empty = false)
    {
        using var output = new MemoryStream();
        using (var zip = new ZipArchive(output, ZipArchiveMode.Create, leaveOpen: true))
        {
            if (!empty)
            {
                var entry = zip.CreateEntry(name, CompressionLevel.Optimal);
                if (attributes is not null) entry.ExternalAttributes = attributes.Value;
                using (var stream = entry.Open()) stream.Write(Encoding.UTF8.GetBytes(text));
                if (extra is not null)
                {
                    using var stream = zip.CreateEntry(extra).Open();
                    stream.Write(Encoding.UTF8.GetBytes("must not be silently discarded"));
                }
            }
        }
        return output.ToArray();
    }

    private static int FindCentralDirectory(byte[] bytes)
    {
        for (var i = 0; i <= bytes.Length - 4; i++)
            if (BinaryPrimitives.ReadUInt32LittleEndian(bytes.AsSpan(i, 4)) == 0x02014b50) return i;
        throw new InvalidOperationException("Fixture has no ZIP central directory.");
    }

    private sealed class Fixture : IDisposable
    {
        private readonly string _root = Directory.CreateTempSubdirectory("fam-skills-mcp-test-").FullName;
        private int _tokens;
        internal string Path => System.IO.Path.Combine(_root, "manifest.json");
        internal Dictionary<string, object?> Entry { get; }
        internal FakeServer Server { get; }
        private Dictionary<string, object?> Manifest { get; }

        internal Fixture(string text = Document, byte[]? archive = null, string toolboxName = "selected", bool skillMd = false)
        {
            archive ??= skillMd ? [] : Zip(text);
            Entry = new()
            {
                ["name"] = "greeting", ["version"] = "1", ["sha256"] = Hash(Encoding.UTF8.GetBytes(text)),
                ["archiveSHA256"] = Hash(archive)
            };
            Manifest = new()
            {
                ["formatVersion"] = 1, ["mode"] = "mcp", ["projectEndpoint"] = RuntimeTests.Project,
                ["toolboxName"] = toolboxName, ["toolboxVersion"] = "7", ["skills"] = new[] { Entry }
            };
            Server = new FakeServer(archive);
            if (skillMd)
            {
                Server.SkillMarkdown = text;
                Server.Selected["type"] = "skill-md";
                Server.Selected["url"] = MarkdownResource;
                Server.Selected["digest"] = "sha256:" + Entry["sha256"];
            }
            Save();
        }

        internal void Save() => File.WriteAllText(Path, JsonSerializer.Serialize(Manifest), new UTF8Encoding(false));
        internal void Detach()
        {
            Manifest["skills"] = Array.Empty<object>();
            Save();
        }
        internal Task<FamSkillsRuntime> OpenAsync(CancellationToken cancellationToken = default, TimeSpan? startupTimeout = null) =>
            FamSkillsRuntime.OpenCoreAsync(Path, RuntimeTests.Project, cancellationToken,
                token =>
                {
                    token.ThrowIfCancellationRequested();
                    return ValueTask.FromResult("renewed-token-" + Interlocked.Increment(ref _tokens));
                }, Server, startupTimeout);

        internal void CheckRequests()
        {
            var requests = Server.Requests.ToArray();
            Check.That(requests.All(r => r.Uri == FakeServer.Endpoint && r.Preview == ""),
                "Every request, including cleanup, must use the pinned endpoint without requiring a preview header.");
            Check.That(requests.Select(r => r.Token).Distinct().Count() == requests.Length &&
                requests.All(r => r.Token.StartsWith("renewed-token-", StringComparison.Ordinal)),
                "The credential callback must refresh authorization for every actual HTTP request.");
        }

        public void Dispose() => Directory.Delete(_root, recursive: true);
    }

    private sealed class FakeServer(byte[] archive) : HttpMessageHandler
    {
        internal const string Endpoint = RuntimeTests.Project + "/toolboxes/selected/versions/7/mcp?api-version=v1";
        internal byte[] Archive { get; } = archive;
        internal Dictionary<string, object?> Selected { get; } = new()
        {
            ["name"] = "greeting", ["description"] = Description, ["type"] = "archive",
            ["url"] = Resource, ["digest"] = "sha256:" + Hash(archive)
        };
        internal ConcurrentQueue<(string Uri, string Token, string Preview)> Requests { get; } = new();
        internal ConcurrentQueue<string> ResourcesRead { get; } = new();
        internal ConcurrentQueue<string> Methods { get; } = new();
        internal TaskCompletionSource IndexStarted { get; } = new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal bool Disposed { get; private set; }
        internal int Deletes { get; private set; }
        internal bool Sse { get; set; }
        internal string? SkillMarkdown { get; set; }
        internal HttpStatusCode NotificationStatus { get; set; } = HttpStatusCode.Accepted;
        internal string NotificationBody { get; set; } = "";
        internal bool NotificationUnknownLength { get; set; }
        internal bool NotificationDeclaredEmpty { get; set; }
        internal NotificationReadStream? NotificationStream { get; private set; }
        internal bool MissingIndex { get; set; }
        internal bool BlockIndex { get; set; }
        internal bool BlockDiscovery { get; set; }
        internal bool DiscoveryCancelled { get; private set; }
        internal string ProtocolVersion { get; set; } = "2025-03-26";
        internal string? CleanupProtocolVersion { get; private set; }
        internal bool Unauthorized { get; set; }
        internal bool FailDelete { get; set; }
        internal HttpStatusCode DeleteStatus { get; set; } = HttpStatusCode.NoContent;
        internal bool FailDispose { get; set; }
        internal string? IndexOverride { get; set; }
        internal string? Failure { get; set; }

        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
        {
            Check.That(!Disposed, "MCP transport was disposed before the host finished.");
            Check.That(request.Headers.Authorization?.Scheme == "Bearer", "Every MCP request needs bearer authorization.");
            Requests.Enqueue((request.RequestUri!.AbsoluteUri, request.Headers.Authorization?.Parameter ?? "",
                request.Headers.TryGetValues("Foundry-Features", out var preview) ? string.Join(",", preview) : ""));
            if (request.Method == HttpMethod.Delete)
            {
                Deletes++;
                Check.That(request.Headers.GetValues("Mcp-Session-Id").Single() == "fam-test-session",
                    "Cleanup must target the exact live session.");
                CleanupProtocolVersion = request.Headers.GetValues("MCP-Protocol-Version").Single();
                return new HttpResponseMessage(FailDelete ? HttpStatusCode.InternalServerError : DeleteStatus);
            }
            if (Unauthorized) return new HttpResponseMessage(HttpStatusCode.Unauthorized);
            using var input = JsonDocument.Parse(await request.Content!.ReadAsByteArrayAsync(cancellationToken));
            var rpc = input.RootElement;
            var method = rpc.GetProperty("method").GetString()!;
            Methods.Enqueue(method);
            if (method == "notifications/initialized")
                return NotificationResponse();
            if (method == "notifications/cancelled")
                return new HttpResponseMessage(HttpStatusCode.Accepted);
            var id = rpc.GetProperty("id");
            if (method == "server/discover")
            {
                if (Failure == "duplicate-probe-json")
                    return new HttpResponseMessage(HttpStatusCode.BadRequest)
                    {
                        Content = new StringContent("{\"error\":{},\"error\":{}}", Encoding.UTF8, "application/json")
                    };
                if (BlockDiscovery)
                {
                    try { await Task.Delay(Timeout.InfiniteTimeSpan, cancellationToken); }
                    catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
                    {
                        DiscoveryCancelled = true;
                        throw;
                    }
                }
                return Json(new { jsonrpc = "2.0", id, error = new { code = -32601, message = "Method not found" } });
            }
            if (method == "initialize")
            {
                var result = new
                {
                    protocolVersion = ProtocolVersion,
                    serverInfo = new { name = "synthetic-fam-toolbox", version = "1" },
                    capabilities = new { resources = new { }, tools = new { } },
                    instructions = "UNTRUSTED_SERVER_INSTRUCTIONS"
                };
                var response = Reply(id, result);
                response.Headers.Add("Mcp-Session-Id", "fam-test-session");
                return response;
            }
            if (method == "ping") return Reply(id, new { });
            Check.That(method == "resources/read", "The Skills SDK must not request or execute Toolbox tools.");
            Check.That(request.Headers.TryGetValues("Mcp-Session-Id", out var session) &&
                session.Single() == "fam-test-session", "Every resource read must reuse the negotiated live session.");
            var uri = rpc.GetProperty("params").GetProperty("uri").GetString()!;
            ResourcesRead.Enqueue(uri);
            if (uri == "skill://index.json")
            {
                IndexStarted.TrySetResult();
                if (BlockIndex) await Task.Delay(Timeout.InfiniteTimeSpan, cancellationToken);
                if (MissingIndex)
                    return Json(new { jsonrpc = "2.0", id, error = new { code = -32002, message = "resource not found" } });
                if (Failure == "redirect")
                {
                    var response = new HttpResponseMessage(HttpStatusCode.Found);
                    response.Headers.Location = new Uri("https://untrusted.invalid/collect");
                    return response;
                }
                if (Failure == "server-request")
                    return Json(new { jsonrpc = "2.0", id, method = "sampling/createMessage", @params = new { } });
                if (Failure == "duplicate-json")
                    return new HttpResponseMessage(HttpStatusCode.OK)
                    {
                        Content = new StringContent("{\"jsonrpc\":\"2.0\",\"id\":1,\"id\":1,\"result\":{}}", Encoding.UTF8, "application/json")
                    };
                var index = IndexOverride ?? JsonSerializer.Serialize(new Dictionary<string, object?>
                {
                    ["$schema"] = Schema,
                    ["skills"] = new object[]
                    {
                        Selected,
                        new { name = "unselected", description = "Must not download.", type = "archive", url = "skill://unselected/unsafe.zip" }
                    }
                });
                var content = new { uri = Failure == "wrong-uri" ? "skill://unexpected" : uri, mimeType = "application/json", text = index };
                return Reply(Failure == "wrong-id" ? JsonSerializer.SerializeToElement("wrong") : id,
                    new { contents = Failure == "extra-block" ? new[] { content, content } : new[] { content } });
            }
            Check.That(Selected["url"] is string selectedUri && uri == selectedUri,
                "An unselected resource reached the server; filtering happened too late.");
            if (SkillMarkdown is not null) return MarkdownReply(id, uri);
            return Reply(id, new
            {
                contents = new[] { new { uri, mimeType = "application/zip", blob = Failure == "bad-base64" ? "%%invalid" : Convert.ToBase64String(Archive) } }
            });
        }

        private HttpResponseMessage Reply(JsonElement id, object result) => Json(new { jsonrpc = "2.0", id, result });

        private HttpResponseMessage MarkdownReply(JsonElement id, string uri)
        {
            var content = new Dictionary<string, object?> { ["uri"] = uri, ["mimeType"] = "text/markdown", ["text"] = SkillMarkdown };
            if (Failure is "md-blob" or "md-both") content["blob"] = Convert.ToBase64String(Encoding.UTF8.GetBytes(SkillMarkdown!));
            if (Failure is "md-blob" or "md-missing-text") content.Remove("text");
            if (Failure == "md-non-string") content["text"] = 1;
            if (Failure == "md-empty") content["text"] = "";
            if (Failure == "md-wrong-uri") content["uri"] = "skill://unexpected/SKILL.md";
            if (Failure is "md-invalid-utf8" or "md-invalid-surrogate" or "md-duplicate-json")
                return MalformedMarkdownReply(id, content);
            object[] contents = Failure == "md-no-block" ? [] :
                Failure == "md-extra-block" ? [content, content] : [content];
            return Reply(id, new { contents });
        }

        private HttpResponseMessage MalformedMarkdownReply(JsonElement id, Dictionary<string, object?> content)
        {
            var json = JsonSerializer.Serialize(new { jsonrpc = "2.0", id, result = new { contents = new[] { content } } });
            if (Failure == "md-duplicate-json")
                json = json.Replace("\"text\":", "\"text\":\"duplicate\",\"text\":", StringComparison.Ordinal);
            else
            {
                Check.That(json.Contains("\\uFFFD", StringComparison.Ordinal), "Encoding fixture must contain its replacement marker.");
                json = json.Replace("\\uFFFD", Failure == "md-invalid-utf8" ? "~" : "\\uD800", StringComparison.Ordinal);
            }
            var bytes = Encoding.UTF8.GetBytes(json);
            if (Failure == "md-invalid-utf8") bytes[Array.IndexOf(bytes, (byte)'~')] = 0xff;
            var response = new HttpResponseMessage(HttpStatusCode.OK) { Content = new ByteArrayContent(bytes) };
            response.Content.Headers.ContentType = new System.Net.Http.Headers.MediaTypeHeaderValue("application/json");
            return response;
        }

        private HttpResponseMessage NotificationResponse()
        {
            var bytes = Encoding.UTF8.GetBytes(NotificationBody);
            HttpContent content;
            if (NotificationUnknownLength)
            {
                NotificationStream = new NotificationReadStream(bytes);
                content = new StreamContent(NotificationStream);
                Check.That(content.Headers.ContentLength is null, "The fixture must not synthesize a known Content-Length.");
            }
            else content = new ByteArrayContent(bytes);
            if (NotificationDeclaredEmpty) content.Headers.ContentLength = 0;
            return new HttpResponseMessage(NotificationStatus) { Content = content };
        }

        private HttpResponseMessage Json(object value)
        {
            var json = JsonSerializer.Serialize(value);
            return new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new StringContent(Sse ? ": keepalive\n\nid: 1\nevent: message\ndata: " + json + "\n\n" : json,
                    Encoding.UTF8, Sse ? "text/event-stream" : "application/json")
            };
        }

        protected override void Dispose(bool disposing)
        {
            Disposed = true;
            base.Dispose(disposing);
            if (FailDispose) throw new IOException("synthetic HTTP disposal failure");
        }
    }

    private sealed class NotificationReadStream(byte[] bytes) : MemoryStream(bytes, writable: false)
    {
        internal int MaxReadSize { get; private set; }
        internal int BytesRead { get; private set; }
        public override bool CanSeek => false;

        public override ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken cancellationToken = default)
        {
            cancellationToken.ThrowIfCancellationRequested();
            MaxReadSize = Math.Max(MaxReadSize, buffer.Length);
            var count = base.Read(buffer.Span);
            BytesRead += count;
            return ValueTask.FromResult(count);
        }
    }
}

#pragma warning restore MAAI001
