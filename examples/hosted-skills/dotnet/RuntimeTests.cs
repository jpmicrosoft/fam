using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using FAM.Skills;
using Microsoft.Agents.AI;
using Microsoft.Extensions.AI;

internal static class RuntimeTests
{
    internal const string Marker = "FAM_DOTNET_SKILL_LOADED";
    internal const string Project = "https://example.services.ai.azure.com/api/projects/example";
    private static int _passed;
    private static int _skipped;

    internal static async Task RunAsync(bool requireSymlinks)
    {
        await Run("published fixture advertises and loads", PublishedFixtureAsync);
        await Run("bundle exact bytes, metadata, no broad approvals", BundleAsync);
        await Run("SDK load is an immutable validated snapshot", SnapshotAsync);
        await Run("unrelated tool approval is preserved", UnrelatedApprovalAsync);
        await Run("name length 64 and description length 1024", LimitsAsync);
        await Run("instructions larger than one MiB are accepted", LargeInstructionsAsync);
        await Run("33 Skills and more than eight MiB total are accepted", LargerInventoryAsync);
        await Run("explicit empty inventory detaches", EmptyAsync);
        await Run("MCP requires renewable authentication without fallback", McpBoundaryAsync);
        await Run("extra root files, resources, scripts and directories rejected", ExtraFilesAsync);
        await Run("missing content and mismatched SHA rejected", MissingAndTamperedAsync);
        await Run("traversal, duplicate identity and invalid digest rejected", InvalidEntriesAsync);
        await Run("duplicate JSON keys including ignored metadata rejected", DuplicateJsonAsync);
        await Run("malformed frontmatter, name mismatch, duplicates and empty body rejected", InvalidSkillAsync);
        await Run("trusted project, versions, modes and language checked", ManifestAsync);
        await Run("cancellation fails startup", CancellationAsync);
        await Run("use after disposal rejected", DisposalAsync);
        await Run("symlink permission skips are narrowly classified", SymlinkFailureClassificationAsync);
        await McpRuntimeTests.RunAsync(Run);
        if (await SymlinksAsync(requireSymlinks)) { _passed++; Console.WriteLine("PASS links and reparse points rejected"); }
        else { _skipped++; Console.WriteLine("SKIP symlink creation requires platform permission; use --self-test-require-symlinks in CI"); }
        Console.WriteLine($"{_passed} passed; {_skipped} skipped. No Azure/model/MCP network calls.");
    }

    private static async Task Run(string name, Func<Task> test)
    {
        await test();
        _passed++;
        Console.WriteLine("PASS " + name);
    }

    private static async Task PublishedFixtureAsync()
    {
        var manifest = Path.Combine(AppContext.BaseDirectory, "fam_skills", "manifest.json");
        Check.That(File.Exists(Path.Combine(AppContext.BaseDirectory, "fam_skills", "FamSkillsRuntime.cs")),
            "Build/publish must include the helper sibling, not just compiled code.");
        await using var runtime = await FamSkillsRuntime.OpenAsync(manifest);
        await ExerciseAsync(runtime);
    }

    private static async Task BundleAsync()
    {
        using var fixture = new Fixture();
        fixture.WriteSkill(Skill().Replace("\n", "\r\n"));
        fixture.Manifest["service"] = "agent";
        fixture.Manifest["declarationHash"] = new string('a', 64);
        fixture.Manifest["futureInertField"] = new { note = "Forward-compatible artifact metadata" };
        fixture.Save();
        await using var runtime = await FamSkillsRuntime.OpenAsync(fixture.Path);
        var model = await ExerciseAsync(runtime);
        Check.That(model.LoadedContent!.StartsWith(fixture.Text, StringComparison.Ordinal),
            "load_skill must preserve exact CRLF SKILL.md text, not reconstructed Markdown.");
    }

    private static async Task SnapshotAsync()
    {
        using var fixture = new Fixture();
        await using var runtime = await FamSkillsRuntime.OpenAsync(fixture.Path);
        File.WriteAllText(fixture.SkillPath, "changed after startup");
        await ExerciseAsync(runtime);
        await Check.RejectsAsync(async () => { await using var next = await FamSkillsRuntime.OpenAsync(fixture.Path); },
            "A new runtime must reject changed on-disk bytes rather than update the lock.");
    }

    private static async Task UnrelatedApprovalAsync()
    {
        using var fixture = new Fixture();
        fixture.WriteSkill(Skill().Replace("description:", "allowed-tools: unrelated\nmetadata:\n  purpose: test\nlicense: MIT\ncompatibility: Any host\ndescription:"));
        fixture.Save();
        await using var runtime = await FamSkillsRuntime.OpenAsync(fixture.Path);
        var called = false;
        var unrelated = new ApprovalRequiredAIFunction(
            AIFunctionFactory.Create(() => { called = true; return "must require approval"; }, "unrelated"));
        using var model = new RecordingModel("greeting", Marker, requestUnrelated: true);
        var agent = new ChatClientAgent(model, new ChatClientAgentOptions
        {
            AIContextProviders = [runtime.Provider],
            ChatOptions = new ChatOptions { Tools = [unrelated] }
        });
        var response = await agent.RunAsync("Load a Skill, then request the unrelated tool.");
        Check.That(model.Loaded && !called, "Skill loading must not grant allowed-tools metadata or unrelated approval.");
        Check.That(response.Messages.SelectMany(m => m.Contents).OfType<ToolApprovalRequestContent>().Any(),
            "The unrelated call must surface an actual approval request.");
    }

    private static async Task LimitsAsync()
    {
        using var fixture = new Fixture(new string('a', 64));
        fixture.WriteSkill(Skill(fixture.Name, new string('d', 1024)));
        fixture.Save();
        await using var runtime = await FamSkillsRuntime.OpenAsync(fixture.Path);
        await ExerciseAsync(runtime, fixture.Name);
        await RejectSkillAsync(Skill(description: new string('d', 1025)));
        using var tooLong = new Fixture(new string('a', 65));
        await RejectAsync(tooLong);
    }

    private static async Task LargeInstructionsAsync()
    {
        using var fixture = new Fixture();
        fixture.WriteSkill(Skill() + new string('x', 1024 * 1024 + 1));
        fixture.Save();
        await using var runtime = await FamSkillsRuntime.OpenAsync(fixture.Path);
        await ExerciseAsync(runtime);
    }

    private static async Task EmptyAsync()
    {
        using var fixture = new Fixture();
        Directory.Delete(Path.GetDirectoryName(fixture.SkillPath)!, recursive: true);
        fixture.Manifest["skills"] = Array.Empty<object>();
        fixture.Save();
        await using var runtime = await FamSkillsRuntime.OpenAsync(fixture.Path);
        using var model = new EmptyModel();
        var agent = new ChatClientAgent(model, new ChatClientAgentOptions { AIContextProviders = [runtime.Provider] });
        await agent.RunAsync("No Skills are declared.");
        Check.That(model.Called, "Explicit detach should still run the application's model.");
        fixture.Manifest["mode"] = "mcp";
        fixture.Save();
        await using var detachedMcp = await FamSkillsRuntime.OpenAsync(fixture.Path);
        var detachedAgent = new ChatClientAgent(model,
            new ChatClientAgentOptions { AIContextProviders = [detachedMcp.Provider] });
        await detachedAgent.RunAsync("Explicitly detached MCP has no required Skills.");
    }

    private static async Task LargerInventoryAsync()
    {
        using var fixture = new Fixture();
        var entries = new List<Dictionary<string, object?>> { fixture.Entry };
        for (var i = 1; i < 33; i++)
        {
            var name = $"skill-{i:00}";
            var directory = Path.Combine(fixture.Root, name);
            Directory.CreateDirectory(directory);
            var content = Skill(name) + (i <= 9 ? new string('x', 1024 * 1024) : "");
            var bytes = new UTF8Encoding(false).GetBytes(content);
            File.WriteAllBytes(Path.Combine(directory, "SKILL.md"), bytes);
            entries.Add(new()
            {
                ["name"] = name, ["path"] = name + "/SKILL.md",
                ["sha256"] = Convert.ToHexString(SHA256.HashData(bytes)).ToLowerInvariant()
            });
        }
        fixture.Manifest["skills"] = entries;
        fixture.Save();
        await using var runtime = await FamSkillsRuntime.OpenAsync(fixture.Path);
        var model = await ExerciseAsync(runtime);
        foreach (var entry in entries)
            Check.That(model.Advertisement.Contains((string)entry["name"]!, StringComparison.Ordinal),
                "Every selected Skill must be advertised; inventory must not be truncated.");
    }

    private static async Task McpBoundaryAsync()
    {
        using var fixture = new Fixture();
        Directory.Delete(Path.GetDirectoryName(fixture.SkillPath)!, recursive: true);
        fixture.Entry.Remove("path");
        fixture.Entry["version"] = "1";
        fixture.Manifest["mode"] = "mcp";
        fixture.Manifest["projectEndpoint"] = Project;
        fixture.Manifest["toolboxName"] = "pinned";
        fixture.Manifest["toolboxVersion"] = "7";
        fixture.Save();
        try { await using var runtime = await FamSkillsRuntime.OpenAsync(fixture.Path, Project); }
        catch (InvalidDataException error) when (error.Message.Contains("bearer token callback", StringComparison.Ordinal))
        {
            return;
        }
        throw new InvalidOperationException("Unauthenticated MCP must fail, not fall back or return an empty provider.");
    }

    private static async Task ExtraFilesAsync()
    {
        foreach (var name in new[] { "run.py", "notes.md", "SKILL.MD", "extra.txt", "nested" })
        {
            using var fixture = new Fixture();
            var path = Path.Combine(Path.GetDirectoryName(fixture.SkillPath)!, name);
            if (name == "nested") Directory.CreateDirectory(path);
            else if (name == "SKILL.MD" && OperatingSystem.IsWindows()) continue;
            else File.WriteAllText(path, "not executed");
            await RejectAsync(fixture);
        }
        using var root = new Fixture();
        File.WriteAllText(Path.Combine(root.Root, "arbitrary.cs"), "not a runtime helper");
        await RejectAsync(root);
        File.Delete(Path.Combine(root.Root, "arbitrary.cs"));
        Directory.CreateDirectory(Path.Combine(root.Root, "unselected"));
        await RejectAsync(root);
    }

    private static async Task MissingAndTamperedAsync()
    {
        using var fixture = new Fixture();
        File.AppendAllText(fixture.SkillPath, "\nchanged");
        await RejectAsync(fixture);
        File.Delete(fixture.SkillPath);
        await RejectAsync(fixture);
    }

    private static async Task InvalidEntriesAsync()
    {
        foreach (var path in new[] { "../greeting/SKILL.md", "greeting\\SKILL.md", "/greeting/SKILL.md",
                     "greeting/SKILL.md:stream", "greeting/../SKILL.md", "greeting/%2e%2e/SKILL.md" })
        {
            using var fixture = new Fixture();
            fixture.Entry["path"] = path;
            fixture.Save();
            await RejectAsync(fixture);
        }
        using var duplicate = new Fixture();
        duplicate.Manifest["skills"] = new[] { duplicate.Entry, duplicate.Entry };
        duplicate.Save();
        await RejectAsync(duplicate);
        using var digest = new Fixture();
        digest.Entry["sha256"] = new string('A', 64);
        digest.Save();
        await RejectAsync(digest);
    }

    private static async Task DuplicateJsonAsync()
    {
        using var fixture = new Fixture();
        var original = File.ReadAllText(fixture.Path);
        foreach (var text in new[]
        {
            original.Replace("\"formatVersion\":1", "\"formatVersion\":1,\"formatVersion\":1"),
            original.Replace("\"sha256\":", "\"SHA256\":\"ignored\",\"sha256\":"),
            original.Replace("\"mode\":", "\"extra\":{\"a\":1,\"a\":2},\"mode\":"),
            original + "{}"
        })
        {
            File.WriteAllText(fixture.Path, text, new UTF8Encoding(false));
            await RejectAsync(fixture);
        }
    }

    private static async Task InvalidSkillAsync()
    {
        foreach (var text in new[]
        {
            "no frontmatter", Skill("unexpected"), Skill().Replace("name: greeting", "name: Greeting"),
            Skill().Replace("name: greeting", "name: greeting\nname: greeting"),
            Skill().Replace("description:", "unexpected: field\ndescription:"),
            Skill().Replace("description:", "metadata:\n  a: one\n  a: two\ndescription:"),
            Skill().Replace("description:", "metadata: &anchor\n  a: one\ndescription:"),
            Skill().Replace("description:", "metadata: {a: one, A: two}\ndescription:"),
            Skill().Replace("name: greeting", "name: 'greeting'"),
            Skill().Replace(Marker, "\0"),
            "---\nname: greeting\ndescription: Valid description\n---\n  \n"
        }) await RejectSkillAsync(text);
        using var invalidUtf8 = new Fixture();
        invalidUtf8.WriteBytes([0xff, 0xfe, 0x41]);
        invalidUtf8.Save();
        await RejectAsync(invalidUtf8);
    }

    private static async Task ManifestAsync()
    {
        foreach (var field in new[] { "formatVersion", "mode", "language" })
        {
            using var fixture = new Fixture();
            fixture.Manifest[field] = field == "formatVersion" ? 2 : "unexpected";
            fixture.Save();
            await RejectAsync(fixture);
        }
        foreach (var version in new[] { "", "latest", "default", "../1" })
        {
            using var fixture = new Fixture();
            fixture.Entry["version"] = version;
            fixture.Save();
            await RejectAsync(fixture);
        }
        using var remote = new Fixture();
        remote.Entry["version"] = "1";
        remote.Manifest["projectEndpoint"] = Project;
        remote.Save();
        await RejectAsync(remote);
        await Check.RejectsAsync(async () =>
        {
            await using var invalid = await FamSkillsRuntime.OpenAsync(remote.Path,
                "https://other.services.ai.azure.com/api/projects/example");
        }, "Project mismatch must fail before any authentication.");
        await using var valid = await FamSkillsRuntime.OpenAsync(remote.Path, Project);
        await ExerciseAsync(valid);
    }

    private static async Task CancellationAsync()
    {
        using var fixture = new Fixture();
        using var cancellation = new CancellationTokenSource();
        cancellation.Cancel();
        try { await using var runtime = await FamSkillsRuntime.OpenAsync(fixture.Path, cancellationToken: cancellation.Token); }
        catch (OperationCanceledException) { return; }
        throw new InvalidOperationException("Cancelled startup must not return a ready provider.");
    }

    private static async Task DisposalAsync()
    {
        using var fixture = new Fixture();
        var runtime = await FamSkillsRuntime.OpenAsync(fixture.Path);
        await runtime.DisposeAsync();
        await runtime.DisposeAsync();
        try { _ = runtime.Provider; }
        catch (ObjectDisposedException) { return; }
        throw new InvalidOperationException("A disposed runtime must not expose a provider.");
    }

    private static async Task<bool> SymlinksAsync(bool required)
    {
        using var fixture = new Fixture();
        var target = Path.Combine(fixture.Root, "target.md");
        File.Move(fixture.SkillPath, target);
        try { File.CreateSymbolicLink(fixture.SkillPath, target); }
        catch (Exception error) when (ShouldSkipSymlinkFailure(error, required, OperatingSystem.IsWindows()))
        {
            return false;
        }
        File.Move(target, Path.Combine(fixture.Root, "FamSkillsRuntime.cs"));
        File.Delete(fixture.SkillPath);
        File.CreateSymbolicLink(fixture.SkillPath, Path.Combine(fixture.Root, "FamSkillsRuntime.cs"));
        await RejectAsync(fixture);
        File.Delete(fixture.SkillPath);
        File.Move(Path.Combine(fixture.Root, "FamSkillsRuntime.cs"), fixture.SkillPath);
        var directory = Path.GetDirectoryName(fixture.SkillPath)!;
        using var targets = new Fixture();
        var hidden = Path.Combine(targets.Root, "elsewhere");
        Directory.Move(directory, hidden);
        Directory.CreateSymbolicLink(directory, hidden);
        await RejectAsync(fixture);
        Directory.Delete(directory);
        Directory.Move(hidden, directory);
        var rootLink = Path.Combine(targets.Root, "linked-root");
        Directory.CreateSymbolicLink(rootLink, fixture.Root);
        await Check.RejectsAsync(async () =>
        {
            await using var runtime = await FamSkillsRuntime.OpenAsync(Path.Combine(rootLink, "manifest.json"));
        }, "The manifest root itself must not be a link.");
        Directory.Delete(rootLink);
        return true;
    }

    private static bool ShouldSkipSymlinkFailure(Exception error, bool required, bool isWindows) =>
        !required && (error is UnauthorizedAccessException or PlatformNotSupportedException ||
            isWindows && error is IOException { HResult: unchecked((int)0x80070522) }); // HRESULT_FROM_WIN32(1314)

    private static Task SymlinkFailureClassificationAsync()
    {
        var privilege = new IOException("Synthetic ERROR_PRIVILEGE_NOT_HELD", unchecked((int)0x80070522));
        Check.That(ShouldSkipSymlinkFailure(privilege, required: false, isWindows: true),
            "Optional Windows symlink coverage must skip ERROR_PRIVILEGE_NOT_HELD.");
        Check.That(!ShouldSkipSymlinkFailure(privilege, required: true, isWindows: true),
            "Mandatory symlink coverage must propagate the privilege failure.");
        Check.That(!ShouldSkipSymlinkFailure(privilege, required: false, isWindows: false),
            "The Win32 privilege exception must not classify non-Windows failures.");
        foreach (var error in new Exception[]
        {
            new IOException("Unrelated I/O error"),
            new IOException("Access denied, not privilege missing", unchecked((int)0x80070005)),
            new IOException("Same low bits, different HRESULT facility", unchecked((int)0x80130522)),
            new InvalidOperationException("Unrelated failure")
        })
            Check.That(!ShouldSkipSymlinkFailure(error, required: false, isWindows: true),
                "Unrelated errors must propagate rather than becoming permission skips.");
        return Task.CompletedTask;
    }

    private static async Task<RecordingModel> ExerciseAsync(FamSkillsRuntime runtime, string name = "greeting")
    {
        using var model = new RecordingModel(name, Marker);
        var agent = new ChatClientAgent(model, new ChatClientAgentOptions { AIContextProviders = [runtime.Provider] });
        await agent.RunAsync("Load the advertised Skill.");
        Check.That(model.Advertised && model.Loaded, "Real provider advertise/load cycle was not completed.");
        return model;
    }

    private static async Task RejectSkillAsync(string content)
    {
        using var fixture = new Fixture();
        fixture.WriteSkill(content);
        fixture.Save();
        await RejectAsync(fixture);
    }

    private static Task RejectAsync(Fixture fixture) => Check.RejectsAsync(async () =>
    {
        await using var runtime = await FamSkillsRuntime.OpenAsync(fixture.Path);
    }, "Invalid artifacts must fail startup.");

    private static string Skill(string name = "greeting", string description = "An offline test Skill.") =>
        $"---\nname: {name}\ndescription: {description}\n---\nInclude {Marker} when greeting.\n";

    private sealed class Fixture : IDisposable
    {
        internal string Root { get; } = Directory.CreateTempSubdirectory("fam-skills-test-").FullName;
        internal string Name { get; }
        internal string Path => System.IO.Path.Combine(Root, "manifest.json");
        internal string SkillPath => System.IO.Path.Combine(Root, Name, "SKILL.md");
        internal string Text { get; private set; } = "";
        internal Dictionary<string, object?> Entry { get; }
        internal Dictionary<string, object?> Manifest { get; }

        internal Fixture(string name = "greeting")
        {
            Name = name;
            Entry = new() { ["name"] = name, ["path"] = name + "/SKILL.md" };
            Manifest = new() { ["formatVersion"] = 1, ["mode"] = "bundle", ["skills"] = new[] { Entry } };
            Directory.CreateDirectory(System.IO.Path.GetDirectoryName(SkillPath)!);
            WriteSkill(Skill(name));
            Save();
        }

        internal void WriteSkill(string text)
        {
            Text = text;
            WriteBytes(new UTF8Encoding(false).GetBytes(text));
        }

        internal void WriteBytes(byte[] bytes)
        {
            File.WriteAllBytes(SkillPath, bytes);
            Entry["sha256"] = Convert.ToHexString(SHA256.HashData(bytes)).ToLowerInvariant();
        }

        internal void Save() => File.WriteAllText(Path, JsonSerializer.Serialize(Manifest), new UTF8Encoding(false));
        public void Dispose() => Directory.Delete(Root, recursive: true);
    }

    private sealed class EmptyModel : IChatClient
    {
        internal bool Called { get; private set; }
        public Task<ChatResponse> GetResponseAsync(IEnumerable<ChatMessage> messages, ChatOptions? options = null,
            CancellationToken cancellationToken = default)
        {
            Check.That(options?.Tools is null || options.Tools.Count == 0, "Detached Skills must expose no tools.");
            Called = true;
            return Task.FromResult(new ChatResponse(new ChatMessage(ChatRole.Assistant, "No Skills.")));
        }
        public IAsyncEnumerable<ChatResponseUpdate> GetStreamingResponseAsync(IEnumerable<ChatMessage> messages,
            ChatOptions? options = null, CancellationToken cancellationToken = default) => throw new NotSupportedException();
        public object? GetService(Type type, object? key = null) => null;
        public void Dispose() { }
    }
}
