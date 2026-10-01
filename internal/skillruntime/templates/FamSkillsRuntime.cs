#nullable enable
#pragma warning disable MAAI001 // Preview SDK invocation context required for provider validation.

using System;
using System.Collections.Generic;
using System.Collections.Concurrent;
using System.IO;
using System.IO.Compression;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Net.ServerSentEvents;
using System.Runtime.ExceptionServices;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Agents.AI;
using Microsoft.Extensions.AI;
using ModelContextProtocol.Client;
using YamlDotNet.Core;
using YamlDotNet.Core.Events;
using YamlDotNet.RepresentationModel;

namespace FAM.Skills;

public sealed class FamSkillsRuntime : IAsyncDisposable
{
    private readonly InstructionsOnlyProvider _provider;
    private readonly McpLifetime? _mcp;
    private int _disposed;
    internal string? McpCacheDirectory => _mcp?.Directory;

    private FamSkillsRuntime(AgentSkillsProvider provider, bool empty, McpLifetime? mcp = null)
    {
        _provider = new InstructionsOnlyProvider(provider, empty);
        _mcp = mcp;
    }

    public AIContextProvider Provider
    {
        get
        {
            ObjectDisposedException.ThrowIf(Volatile.Read(ref _disposed) != 0, this);
            return _provider;
        }
    }

    public static async Task<FamSkillsRuntime> OpenAsync(string manifestPath,
        string? projectEndpoint = null, CancellationToken cancellationToken = default,
        Func<CancellationToken, ValueTask<string>>? getBearerToken = null)
        => await OpenCoreAsync(manifestPath, projectEndpoint, cancellationToken, getBearerToken, null).ConfigureAwait(false);

    internal static async Task<FamSkillsRuntime> OpenCoreAsync(string manifestPath,
        string? projectEndpoint, CancellationToken cancellationToken,
        Func<CancellationToken, ValueTask<string>>? getBearerToken, HttpMessageHandler? testTransport,
        TimeSpan? startupTimeout = null)
    {
        using var deadline = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        deadline.CancelAfter(startupTimeout ?? NetworkTimeout);
        cancellationToken = deadline.Token;
        cancellationToken.ThrowIfCancellationRequested();
        var inventory = ReadLock(manifestPath, projectEndpoint);
        if (inventory.Mode == "mcp" && inventory.Skills.Count != 0)
        {
            if (getBearerToken is null) throw Invalid("MCP requires a renewable trusted-project bearer token callback.");
            return await OpenMcpAsync(inventory, getBearerToken, testTransport, cancellationToken).ConfigureAwait(false);
        }
        var locked = ReadBundle(inventory, cancellationToken);
        var skills = await DiscoverBundleAsync(locked, cancellationToken).ConfigureAwait(false);
        ValidateTree(inventory);
        cancellationToken.ThrowIfCancellationRequested();
        var provider = new AgentSkillsProvider(skills, new AgentSkillsProviderOptions
        {
            DisableLoadSkillApproval = true
        });
        return new FamSkillsRuntime(provider, skills.Length == 0);
    }

    private static async Task<AgentSkill[]> DiscoverBundleAsync(IReadOnlyList<SkillText> locked,
        CancellationToken cancellationToken)
    {
        if (locked.Count == 0) return [];
        using var source = new AgentFileSkillsSource(locked.Select(s => s.Directory),
            options: new AgentFileSkillsSourceOptions
            {
                AllowedResourceExtensions = [],
                AllowedScriptExtensions = [],
                SearchDepth = 1
            });
        using var model = new ValidationChatClient();
        var agent = new ChatClientAgent(model, new ChatClientAgentOptions());
        var context = new AgentSkillsSourceContext(agent, null);
        var discovered = (await source.GetSkillsAsync(context, cancellationToken).ConfigureAwait(false)).ToArray();
        if (discovered.Length != locked.Count)
            throw Invalid("The SDK did not discover every locked Skill.");
        var expected = locked.ToDictionary(s => s.Entry.Name, StringComparer.Ordinal);
        foreach (var skill in discovered)
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (!expected.Remove(skill.Frontmatter.Name, out var item) ||
                skill.Frontmatter.Description != item.Description)
                throw Invalid("The SDK advertised an unexpected or duplicate Skill identity/description.");
            var loaded = await skill.GetContentAsync(cancellationToken).ConfigureAwait(false);
            if (!loaded.StartsWith(item.Content, StringComparison.Ordinal) ||
                !Regex.IsMatch(loaded[item.Content.Length..],
                    @"\A\s*(?:<available_resources\s*/>\s*)?(?:<available_scripts\s*/>\s*)?\z"))
                throw Invalid("SDK-loaded instructions differ from the locked SKILL.md bytes.");
            // The SDK objects retain the validated content; subsequent loads cannot reread changed files.
            skill.Frontmatter.AllowedTools = null;
        }
        foreach (var item in locked)
        {
            var files = Directory.EnumerateFileSystemEntries(item.Directory).ToArray();
            if (files.Length != 1 || Path.GetFileName(files[0]) != "SKILL.md")
                throw Invalid("Skill layout changed during SDK discovery.");
            ValidateSkill(item.Entry, ReadRegularFile(files[0], MaxSkillBytes), item.Directory);
        }
        return discovered;
    }

    public async ValueTask DisposeAsync()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0) return;
        try { _provider.Dispose(); }
        finally { if (_mcp is not null) await _mcp.DisposeAsync().ConfigureAwait(false); }
    }

    private static async Task<FamSkillsRuntime> OpenMcpAsync(Lock inventory,
        Func<CancellationToken, ValueTask<string>> getBearerToken, HttpMessageHandler? testTransport,
        CancellationToken cancellationToken)
    {
        var lifetime = new McpLifetime(inventory, getBearerToken, testTransport);
        AgentSkillsProvider? provider = null;
        try
        {
            lifetime.Client = await McpClient.CreateAsync(lifetime.Transport, new McpClientOptions
            {
                InitializationTimeout = NetworkTimeout
            }, cancellationToken: cancellationToken).ConfigureAwait(false);
            provider = new AgentSkillsProviderBuilder()
                .UseMcpSkills(lifetime.Client, new AgentMcpSkillsSourceOptions
                {
                    ArchiveSkillsDirectory = lifetime.Directory,
                    ArchiveResourceExtensions = [],
                    ArchiveResourceSearchDepth = 1,
                    ArchiveMaxFileCount = 1,
                    ArchiveMaxSizeBytes = MaxDownloadBytes,
                    ArchiveMaxUncompressedSizeBytes = MaxSkillBytes
                })
                .UseOptions(options => options.DisableLoadSkillApproval = true)
                .Build();
            var runtime = new FamSkillsRuntime(provider, empty: false, lifetime);
            await runtime.VerifyMcpProviderAsync(inventory, cancellationToken).ConfigureAwait(false);
            lifetime.Guard.Failure?.Throw();
            return runtime;
        }
        catch (Exception startupError)
        {
            var transportError = lifetime.Guard.Failure;
            try
            {
                try { provider?.Dispose(); }
                finally { await lifetime.DisposeAsync().ConfigureAwait(false); }
            }
            catch (Exception cleanupError)
            {
                throw new AggregateException(transportError?.SourceException ?? startupError, cleanupError);
            }
            transportError?.Throw();
            throw;
        }
    }

    private async Task VerifyMcpProviderAsync(Lock inventory, CancellationToken cancellationToken)
    {
        using var model = new ValidationChatClient();
        var agent = new ChatClientAgent(model, new ChatClientAgentOptions());
        var context = await _provider.InvokingAsync(
            new AIContextProvider.InvokingContext(agent, null, new AIContext()), cancellationToken).ConfigureAwait(false);
        var load = context.Tools!.OfType<AIFunction>().Single();
        var parameters = load.JsonSchema.GetProperty("properties").EnumerateObject().ToArray();
        if (parameters.Length != 1) throw Invalid("Unsupported SDK load_skill argument contract.");
        foreach (var entry in inventory.Skills)
        {
            cancellationToken.ThrowIfCancellationRequested();
            var result = await load.InvokeAsync(new AIFunctionArguments
            {
                [parameters[0].Name] = entry.Name
            }, cancellationToken).ConfigureAwait(false);
            cancellationToken.ThrowIfCancellationRequested();
            var content = result switch
            {
                string text => text,
                JsonElement { ValueKind: JsonValueKind.String } json => json.GetString()!,
                _ => throw Invalid("Unexpected SDK load_skill result.")
            };
            if (!_mcp!.Guard.Verified.TryGetValue(entry.Name, out var verified) ||
                !content.StartsWith(verified.Content, StringComparison.Ordinal) ||
                !Regex.IsMatch(content[verified.Content.Length..],
                    @"\A\s*(?:<available_resources\s*/>\s*)?(?:<available_scripts\s*/>\s*)?\z"))
                throw Invalid("SDK MCP discovery/loading omitted or changed a locked Skill.");
        }
        ValidateTree(inventory);
    }

    private sealed class InstructionsOnlyProvider(AgentSkillsProvider inner, bool empty) : AIContextProvider, IDisposable
    {
        private int _disposed;

        private void ThrowIfDisposed() =>
            ObjectDisposedException.ThrowIf(Volatile.Read(ref _disposed) != 0, this);

        protected override async ValueTask<AIContext> ProvideAIContextAsync(
            InvokingContext context, CancellationToken cancellationToken = default)
        {
            ThrowIfDisposed();
            cancellationToken.ThrowIfCancellationRequested();
            var provided = await inner.InvokingAsync(context, cancellationToken).ConfigureAwait(false);
            ThrowIfDisposed();
            cancellationToken.ThrowIfCancellationRequested();
            var tools = provided.Tools?.Where(t => t.Name == "load_skill").ToList() ?? [];
            if (!empty && (tools.Count != 1 || tools[0] is not AIFunction))
                throw Invalid("The SDK did not expose exactly one load_skill tool.");
            provided.Tools = empty ? [] : [new LifetimeFunction((AIFunction)tools[0], this)];
            return provided;
        }

        private sealed class LifetimeFunction(AIFunction function, InstructionsOnlyProvider owner) : DelegatingAIFunction(function)
        {
            protected override async ValueTask<object?> InvokeCoreAsync(AIFunctionArguments arguments, CancellationToken cancellationToken)
            {
                owner.ThrowIfDisposed();
                cancellationToken.ThrowIfCancellationRequested();
                var result = await base.InvokeCoreAsync(arguments, cancellationToken).ConfigureAwait(false);
                owner.ThrowIfDisposed();
                cancellationToken.ThrowIfCancellationRequested();
                return result;
            }
        }

        public void Dispose()
        {
            if (Interlocked.Exchange(ref _disposed, 1) == 0) inner.Dispose();
        }
    }

    private sealed class ValidationChatClient : IChatClient
    {
        public Task<ChatResponse> GetResponseAsync(IEnumerable<ChatMessage> messages,
            ChatOptions? options = null, CancellationToken cancellationToken = default) =>
            throw new InvalidOperationException("Skill discovery must not invoke a model.");
        public IAsyncEnumerable<ChatResponseUpdate> GetStreamingResponseAsync(IEnumerable<ChatMessage> messages,
            ChatOptions? options = null, CancellationToken cancellationToken = default) =>
            throw new InvalidOperationException("Skill discovery must not invoke a model.");
        public object? GetService(Type serviceType, object? serviceKey = null) => null;
        public void Dispose() { }
    }

    // Existing FAM filesystem/download guards, not Azure Skills quotas.
    internal const int MaxSkillBytes = 64 * 1024 * 1024;
    internal const int MaxDownloadBytes = 256 * 1024 * 1024;
    internal const int MaxManifestBytes = 8 * 1024 * 1024;
    private static readonly TimeSpan NetworkTimeout = TimeSpan.FromSeconds(60);
    private const string IndexUri = "skill://index.json";
    private static readonly UTF8Encoding StrictUtf8 = new(false, true);
    private static readonly Regex NamePattern = new(
        @"\A[a-z0-9]+(?:-[a-z0-9]+)*\z", RegexOptions.CultureInvariant);
    private static readonly Regex VersionPattern = new(
        @"\A[A-Za-z0-9][A-Za-z0-9._-]*\z", RegexOptions.CultureInvariant);
    private static readonly Regex DigestPattern = new(
        @"\A[0-9a-f]{64}\z", RegexOptions.CultureInvariant);

    private sealed record Entry(string Name, string? Version, string Sha256, string? Path, string? ArchiveSha256);
    private sealed record SkillText(Entry Entry, string Content, string Description, string Directory);
    private sealed record Lock(string Directory, string Mode, string? ProjectEndpoint,
        string? ToolboxName, string? ToolboxVersion, IReadOnlyList<Entry> Skills, byte[] ManifestDigest);

    private static Lock ReadLock(string manifestPath, string? trustedProjectEndpoint)
    {
        if (!Path.IsPathFullyQualified(manifestPath) ||
            Path.GetFileName(manifestPath) != "manifest.json")
            throw Invalid("Use the absolute path to fam_skills/manifest.json.");
        var directory = Path.GetDirectoryName(Path.GetFullPath(manifestPath))!;
        var manifestBytes = ReadRegularFile(manifestPath, MaxManifestBytes);
        using var json = ParseJson(manifestBytes);
        var root = json.RootElement;
        RequireObject(root);
        if (!root.TryGetProperty("formatVersion", out var format) || format.ValueKind != JsonValueKind.Number ||
            !format.TryGetInt32(out var number) || number != 1)
            throw Invalid("Unsupported or missing manifest formatVersion.");
        var mode = RequiredString(root, "mode");
        if (mode is not ("bundle" or "mcp"))
            throw Invalid("Manifest mode must be bundle or mcp.");
        var language = OptionalString(root, "language");
        if (language is not null && language != "dotnet")
            throw Invalid("The manifest language is not dotnet.");
        var declarationHash = OptionalString(root, "declarationHash");
        if (declarationHash is not null && !DigestPattern.IsMatch(declarationHash))
            throw Invalid("Invalid declarationHash.");

        var endpoint = OptionalString(root, "projectEndpoint");
        if (endpoint == "") endpoint = null;
        if (endpoint is not null)
        {
            endpoint = ValidateProjectEndpoint(endpoint);
            if (trustedProjectEndpoint is null ||
                endpoint != ValidateProjectEndpoint(trustedProjectEndpoint))
                throw Invalid("Manifest project does not match the application's trusted project.");
        }
        var toolbox = OptionalString(root, "toolboxName");
        var toolboxVersion = OptionalString(root, "toolboxVersion");
        if (!root.TryGetProperty("skills", out var skills) || skills.ValueKind != JsonValueKind.Array)
            throw Invalid("Manifest skills must be an explicit array; use [] only to detach.");
        var entries = new List<Entry>();
        var names = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
        foreach (var item in skills.EnumerateArray())
        {
            RequireObject(item);
            var name = RequiredString(item, "name");
            ValidateName(name);
            if (!names.Add(name)) throw Invalid("Duplicate selected Skill identity.");
            var version = OptionalString(item, "version");
            if (version is not null) ValidateVersion(version);
            if (version is not null && endpoint is null)
                throw Invalid("Versioned Skills require a trusted project binding.");
            var digest = RequiredString(item, "sha256");
            if (!DigestPattern.IsMatch(digest)) throw Invalid("Invalid SKILL.md SHA-256.");
            var archiveDigest = OptionalString(item, "archiveSHA256");
            if (archiveDigest is not null && !DigestPattern.IsMatch(archiveDigest))
                throw Invalid("Invalid archive SHA-256.");
            var path = OptionalString(item, "path");
            if (mode == "bundle" && path != name + "/SKILL.md")
                throw Invalid("Bundle paths must be exactly name/SKILL.md, relative to fam_skills.");
            if (mode == "mcp" && (path is not null || version is null))
                throw Invalid("MCP Skills require immutable versions and must not contain bundle paths.");
            entries.Add(new Entry(name, version, digest, path, archiveDigest));
        }
        if (mode == "bundle" && (toolbox is not null || toolboxVersion is not null))
            throw Invalid("Bundle mode must not declare a Toolbox.");
        if (mode == "mcp" && entries.Count != 0)
        {
            if (endpoint is null || toolbox is null || toolboxVersion is null)
                throw Invalid("MCP Skills require a trusted project and immutable Toolbox.");
            if (!Regex.IsMatch(toolbox, @"\A[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?\z"))
                throw Invalid("Invalid Toolbox name.");
            ValidateVersion(toolboxVersion);
        }
        var result = new Lock(directory, mode, endpoint, toolbox, toolboxVersion, entries, SHA256.HashData(manifestBytes));
        ValidateTree(result);
        return result;
    }

    private static void ValidateTree(Lock inventory)
    {
        CheckPath(inventory.Directory, directory: true);
        var expected = new HashSet<string>(StringComparer.Ordinal) { "manifest.json" };
        if (inventory.Mode == "bundle")
            foreach (var entry in inventory.Skills) expected.Add(entry.Name);
        var found = new HashSet<string>(StringComparer.Ordinal);
        foreach (var item in Directory.EnumerateFileSystemEntries(inventory.Directory))
        {
            var name = Path.GetFileName(item);
            if (name is ".ownership.json" or "FamSkillsRuntime.cs")
            {
                CheckPath(item, directory: false);
                continue;
            }
            if (!expected.Contains(name)) throw Invalid("Unexpected file or directory in fam_skills.");
            found.Add(name);
            CheckPath(item, directory: name != "manifest.json");
            if (name == "manifest.json")
            {
                if (!SHA256.HashData(ReadRegularFile(item, MaxManifestBytes)).AsSpan().SequenceEqual(inventory.ManifestDigest))
                    throw Invalid("The Skills manifest changed during initialization.");
                continue;
            }
            var children = Directory.EnumerateFileSystemEntries(item).ToArray();
            if (children.Length != 1 || Path.GetFileName(children[0]) != "SKILL.md")
                throw Invalid("Instructions-only Skill directories must contain exactly one SKILL.md.");
            CheckPath(children[0], directory: false);
        }
        if (!expected.SetEquals(found)) throw Invalid("Locked Skill files are missing.");
    }

    private static IReadOnlyList<SkillText> ReadBundle(Lock inventory, CancellationToken cancellationToken)
    {
        var result = new List<SkillText>();
        foreach (var entry in inventory.Skills)
        {
            cancellationToken.ThrowIfCancellationRequested();
            var directory = Path.Combine(inventory.Directory, entry.Name);
            var bytes = ReadRegularFile(Path.Combine(directory, "SKILL.md"), MaxSkillBytes);
            result.Add(ValidateSkill(entry, bytes, directory));
        }
        ValidateTree(inventory);
        return result;
    }

    private static SkillText ValidateSkill(Entry entry, byte[] bytes, string directory)
    {
        if (bytes.Length > MaxSkillBytes) throw Invalid("SKILL.md exceeds the FAM 64 MiB safety guard.");
        if (!StringComparer.Ordinal.Equals(Convert.ToHexString(SHA256.HashData(bytes)).ToLowerInvariant(),
                entry.Sha256))
            throw Invalid("SKILL.md differs from its locked SHA-256; explicitly resynchronize.");
        string content;
        try { content = StrictUtf8.GetString(bytes); }
        catch (DecoderFallbackException) { throw Invalid("SKILL.md must be valid UTF-8."); }
        if (content.Contains('\0')) throw Invalid("SKILL.md must not contain NUL bytes.");
        using var reader = new StringReader(content);
        if (reader.ReadLine() != "---") throw Invalid("SKILL.md must start with --- frontmatter.");
        var header = new StringBuilder();
        string? line;
        while ((line = reader.ReadLine()) is not null && line != "---") header.AppendLine(line);
        if (line is null || string.IsNullOrWhiteSpace(reader.ReadToEnd()))
            throw Invalid("SKILL.md requires a closing delimiter and nonempty instructions.");
        var fields = ParseFrontmatter(header.ToString());
        var name = RequiredPlainScalar(fields, "name");
        ValidateName(name);
        if (name != entry.Name) throw Invalid("SKILL.md has an unexpected selected identity.");
        var description = RequiredPlainScalar(fields, "description");
        if (description.EnumerateRunes().Count() > 1024)
            throw Invalid("Skill descriptions must not exceed 1024 characters.");
        return new SkillText(entry, content, description, directory);
    }

    private static Dictionary<string, YamlNode> ParseFrontmatter(string header)
    {
        try
        {
            var parser = new Parser(new StringReader(header));
            while (parser.MoveNext())
            {
                if (parser.Current is AnchorAlias ||
                    parser.Current is NodeEvent node && (!node.Anchor.IsEmpty || !node.Tag.IsEmpty))
                    throw Invalid("Skill frontmatter must not contain anchors, aliases, or explicit tags.");
            }
            var yaml = new YamlStream();
            yaml.Load(new StringReader(header));
            if (yaml.Documents.Count != 1 || yaml.Documents[0].RootNode is not YamlMappingNode mapping)
                throw Invalid("Skill frontmatter must be one YAML mapping.");
            var fields = StringMapping(mapping);
            foreach (var (key, value) in fields)
            {
                switch (key)
                {
                    case "name":
                    case "description":
                    case "license":
                    case "compatibility":
                    case "allowed-tools":
                        if (value is not YamlScalarNode)
                            throw Invalid("Skill frontmatter fields must be scalar text.");
                        break;
                    case "metadata":
                        if (value is not YamlMappingNode metadata ||
                            StringMapping(metadata).Values.Any(v => v is not YamlScalarNode))
                            throw Invalid("Skill metadata must be a string mapping.");
                        break;
                    default: throw Invalid("Unsupported Skill frontmatter field.");
                }
            }
            return fields;
        }
        catch (YamlException) { throw Invalid("Malformed or duplicate-key Skill frontmatter."); }
    }

    private static Dictionary<string, YamlNode> StringMapping(YamlMappingNode mapping)
    {
        var result = new Dictionary<string, YamlNode>(StringComparer.OrdinalIgnoreCase);
        foreach (var (key, value) in mapping.Children)
        {
            if (key is not YamlScalarNode { Value: not null } scalar ||
                scalar.Value == "<<" || !result.TryAdd(scalar.Value, value))
                throw Invalid("Skill frontmatter contains duplicate, merge, or non-text keys.");
        }
        return result;
    }

    private static string RequiredPlainScalar(Dictionary<string, YamlNode> fields, string key)
    {
        if (!fields.TryGetValue(key, out var node) || node is not YamlScalarNode scalar ||
            scalar.Style != ScalarStyle.Plain || string.IsNullOrWhiteSpace(scalar.Value))
            throw Invalid("Skill name and description must be nonempty, plain unquoted YAML text.");
        return scalar.Value;
    }

    private static JsonDocument ParseJson(byte[] bytes)
    {
        try { _ = StrictUtf8.GetCharCount(bytes); }
        catch (DecoderFallbackException) { throw Invalid("JSON must be valid UTF-8."); }
        var json = JsonDocument.Parse(bytes);
        try { RejectDuplicateKeys(json.RootElement); return json; }
        catch { json.Dispose(); throw; }
    }

    private static void RejectDuplicateKeys(JsonElement element)
    {
        if (element.ValueKind == JsonValueKind.Object)
        {
            var keys = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            foreach (var property in element.EnumerateObject())
            {
                if (!keys.Add(property.Name)) throw Invalid("Duplicate JSON property.");
                RejectDuplicateKeys(property.Value);
            }
        }
        else if (element.ValueKind == JsonValueKind.Array)
            foreach (var child in element.EnumerateArray()) RejectDuplicateKeys(child);
    }

    private static string RequiredString(JsonElement element, string name) =>
        OptionalString(element, name) is { Length: > 0 } value
            ? value : throw Invalid("Missing or empty required manifest/index field: " + name);

    private static string? OptionalString(JsonElement element, string name)
    {
        if (!element.TryGetProperty(name, out var value)) return null;
        if (value.ValueKind != JsonValueKind.String) throw Invalid("Expected string field: " + name);
        return value.GetString()!;
    }

    private static void RequireObject(JsonElement element)
    {
        if (element.ValueKind != JsonValueKind.Object) throw Invalid("Expected a JSON object.");
    }

    private static void ValidateName(string name)
    {
        if (name.Length is < 1 or > 64 || !NamePattern.IsMatch(name))
            throw Invalid("Skill names must be 1-64 lowercase letters/digits with single internal hyphens.");
    }

    private static void ValidateVersion(string version)
    {
        if (!VersionPattern.IsMatch(version) ||
            version.Equals("latest", StringComparison.OrdinalIgnoreCase) ||
            version.Equals("default", StringComparison.OrdinalIgnoreCase))
            throw Invalid("An explicit immutable version is required, not an alias or path.");
    }

    private static string ValidateProjectEndpoint(string endpoint)
    {
        if (!Uri.TryCreate(endpoint, UriKind.Absolute, out var uri) || uri.Scheme != "https" ||
            !uri.IsDefaultPort || uri.UserInfo.Length != 0 || uri.Query.Length != 0 ||
            uri.Fragment.Length != 0 || endpoint.Contains('%') || endpoint.Contains('\\') ||
            !new[] { "services.ai.azure.com", "cognitiveservices.azure.com", "openai.azure.com" }
                .Any(s => uri.Host == s || uri.Host.EndsWith("." + s, StringComparison.OrdinalIgnoreCase)) ||
            !Regex.IsMatch(uri.AbsolutePath, @"\A/api/projects/[A-Za-z0-9][A-Za-z0-9._-]*/?\z") ||
            !uri.AbsoluteUri.TrimEnd('/').Equals(endpoint.TrimEnd('/'), StringComparison.OrdinalIgnoreCase))
            throw Invalid("Expected a canonical HTTPS Foundry /api/projects/{project} endpoint.");
        return uri.AbsoluteUri.TrimEnd('/');
    }

    private static void CheckPath(string path, bool directory)
    {
        var full = Path.GetFullPath(path);
        var attributes = File.GetAttributes(full);
        if ((attributes & (FileAttributes.ReparsePoint | FileAttributes.Device)) != 0 ||
            ((attributes & FileAttributes.Directory) != 0) != directory)
            throw Invalid("Skill artifacts must be regular files/directories, never links or devices.");
        for (var parent = Directory.GetParent(full); parent is not null; parent = parent.Parent)
        {
            if ((parent.Attributes & FileAttributes.ReparsePoint) != 0)
                throw Invalid("Skill artifact paths must not traverse links or reparse points.");
        }
    }

    private static byte[] ReadRegularFile(string path, int limit)
    {
        CheckPath(path, directory: false);
        using var file = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read);
        if (file.Length > limit) throw Invalid("Artifact exceeds an existing FAM file/download safety guard.");
        using var buffer = new MemoryStream();
        var chunk = new byte[81920];
        int count;
        while ((count = file.Read(chunk, 0, chunk.Length)) != 0)
        {
            if (buffer.Length + count > limit) throw Invalid("Artifact exceeded its FAM safety guard while reading.");
            buffer.Write(chunk, 0, count);
        }
        CheckPath(path, directory: false);
        if (buffer.Length != file.Length) throw Invalid("Skill artifact changed while reading.");
        return buffer.ToArray();
    }

    private static InvalidDataException Invalid(string message) => new("FAM Skills: " + message);

    private sealed record McpSelection(Entry Entry, string Type, string Url, string Description, string? Digest);

    private sealed class McpLifetime : IAsyncDisposable
    {
        internal string Directory { get; }
        internal McpGuardHandler Guard { get; }
        internal HttpClientTransport Transport { get; }
        internal McpClient? Client { get; set; }
        private readonly HttpClient _http;
        private readonly Lock _inventory;

        internal McpLifetime(Lock inventory, Func<CancellationToken, ValueTask<string>> getBearerToken,
            HttpMessageHandler? testTransport)
        {
            _inventory = inventory;
            var endpoint = new Uri(inventory.ProjectEndpoint + "/toolboxes/" +
                Uri.EscapeDataString(inventory.ToolboxName!) + "/versions/" +
                Uri.EscapeDataString(inventory.ToolboxVersion!) + "/mcp?api-version=v1");
            CheckPath(Path.GetTempPath(), directory: true);
            Directory = System.IO.Directory.CreateTempSubdirectory("fam-skills-mcp-").FullName;
            try
            {
                Guard = new McpGuardHandler(inventory, endpoint, getBearerToken, Directory)
                {
                    InnerHandler = testTransport ?? new SocketsHttpHandler
                    {
                        AllowAutoRedirect = false,
                        UseCookies = false,
                        AutomaticDecompression = DecompressionMethods.None,
                        ConnectTimeout = NetworkTimeout
                    }
                };
                _http = new HttpClient(Guard) { Timeout = NetworkTimeout };
                Transport = new HttpClientTransport(new HttpClientTransportOptions
                {
                    Endpoint = endpoint,
                    TransportMode = HttpTransportMode.StreamableHttp,
                    EnableStandaloneGetStream = false,
                    OwnsSession = false,
                    MaxReconnectionAttempts = 0,
                    ConnectionTimeout = NetworkTimeout
                }, _http, loggerFactory: null, ownsHttpClient: false);
            }
            catch (Exception setupError)
            {
                try
                {
                    try
                    {
                        if (_http is not null) _http.Dispose();
                        else Guard?.Dispose();
                    }
                    finally { System.IO.Directory.Delete(Directory); }
                }
                catch (Exception cleanupError) { throw new AggregateException(setupError, cleanupError); }
                throw;
            }
        }

        public async ValueTask DisposeAsync()
        {
            var errors = new List<Exception>();
            try { if (Client is not null) await Client.DisposeAsync().ConfigureAwait(false); }
            catch (Exception error) { errors.Add(error); }
            try { await Transport.DisposeAsync().ConfigureAwait(false); }
            catch (Exception error) { errors.Add(error); }
            try
            {
                // Explicit DELETE makes failed session cleanup visible instead of relying on SDK best effort.
                if (Guard.SessionId is not null)
                {
                    using var request = new HttpRequestMessage(HttpMethod.Delete, Guard.Endpoint);
                    request.Headers.Add("Mcp-Session-Id", Guard.SessionId);
                    if (Guard.ProtocolVersion is not null)
                        request.Headers.Add("MCP-Protocol-Version", Guard.ProtocolVersion);
                    using var response = await _http.SendAsync(request, HttpCompletionOption.ResponseHeadersRead).ConfigureAwait(false);
                    if (response.StatusCode is not (HttpStatusCode.OK or HttpStatusCode.NoContent or HttpStatusCode.NotFound))
                        throw new HttpRequestException("FAM Skills: MCP session termination was not acknowledged.",
                            null, response.StatusCode);
                }
            }
            catch (Exception error) { errors.Add(error); }
            try { _http.Dispose(); }
            catch (Exception error) { errors.Add(error); }
            try { DeleteOwnedCache(Directory, _inventory); }
            catch (Exception error) { errors.Add(error); }
            if (errors.Count != 0) throw new AggregateException("FAM Skills: MCP cleanup failed.", errors);
        }
    }

    private static void DeleteOwnedCache(string directory, Lock inventory)
    {
        if (!System.IO.Directory.Exists(directory)) return;
        CheckPath(directory, directory: true);
        var names = inventory.Skills.Select(s => s.Name).ToHashSet(StringComparer.Ordinal);
        foreach (var child in System.IO.Directory.EnumerateFileSystemEntries(directory))
        {
            if (!names.Contains(Path.GetFileName(child))) throw Invalid("Unexpected SDK cache entry; refusing cleanup.");
            CheckPath(child, directory: true);
            foreach (var file in System.IO.Directory.EnumerateFileSystemEntries(child))
            {
                if (Path.GetFileName(file) != "SKILL.md") throw Invalid("Unexpected SDK cache file; refusing cleanup.");
                CheckPath(file, directory: false);
                File.Delete(file);
            }
            System.IO.Directory.Delete(child);
        }
        System.IO.Directory.Delete(directory);
    }

    private sealed class McpGuardHandler(Lock inventory, Uri endpoint,
        Func<CancellationToken, ValueTask<string>> getBearerToken, string cacheDirectory) : DelegatingHandler
    {
        internal Uri Endpoint => endpoint;
        internal string? SessionId { get; private set; }
        internal string? ProtocolVersion { get; private set; }
        internal ConcurrentDictionary<string, SkillText> Verified { get; } = new(StringComparer.Ordinal);
        internal ExceptionDispatchInfo? Failure => Volatile.Read(ref _failure);
        private ExceptionDispatchInfo? _failure;
        private static readonly HttpRequestOptionsKey<bool> NegotiationCancellation = new("FAM.Skills.NegotiationCancellation");
        private IReadOnlyDictionary<string, McpSelection> _selected =
            new Dictionary<string, McpSelection>(StringComparer.Ordinal);

        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
        {
            try { return await SendValidatedAsync(request, cancellationToken).ConfigureAwait(false); }
            catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested &&
                request.Options.TryGetValue(NegotiationCancellation, out var expected) && expected)
            {
                // The SDK cancels an unanswered discovery probe before its initialize fallback.
                throw;
            }
            catch (Exception error)
            {
                // The SDK catches discovery/download errors; readiness must retain the original failure.
                Interlocked.CompareExchange(ref _failure, ExceptionDispatchInfo.Capture(error), null);
                throw;
            }
        }

        private async Task<HttpResponseMessage> SendValidatedAsync(HttpRequestMessage request, CancellationToken cancellationToken)
        {
            if (request.RequestUri?.AbsoluteUri != endpoint.AbsoluteUri)
                throw Invalid("MCP may contact only the pinned same-project Toolbox endpoint.");
            using var deadline = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
            deadline.CancelAfter(NetworkTimeout);
            var token = deadline.Token;
            string? method = null;
            string? resource = null;
            JsonElement id = default;
            if (request.Method != HttpMethod.Delete)
            {
                if (request.Method != HttpMethod.Post || request.Content is null)
                    throw Invalid("Only Streamable HTTP MCP requests are permitted.");
                using var input = ParseJson(await request.Content.ReadAsByteArrayAsync(token).ConfigureAwait(false));
                var rpc = input.RootElement;
                RequireObject(rpc);
                method = RequiredString(rpc, "method");
                if (method is not ("server/discover" or "initialize" or "notifications/initialized" or "resources/read" or "ping" or "notifications/cancelled"))
                    throw Invalid("The Skills transport does not permit tools or unrelated MCP methods.");
                if (method != "notifications/cancelled") Failure?.Throw();
                request.Options.Set(NegotiationCancellation, method is "server/discover" or "notifications/cancelled");
                if (rpc.TryGetProperty("id", out var requestId)) id = requestId.Clone();
                if (method == "resources/read")
                {
                    resource = RequiredString(rpc.GetProperty("params"), "uri");
                    if (resource != IndexUri && !Volatile.Read(ref _selected).ContainsKey(resource))
                        throw Invalid("MCP resource is outside the locked Skill allowlist.");
                }
            }
            var bearer = await getBearerToken(token).ConfigureAwait(false);
            if (string.IsNullOrWhiteSpace(bearer) || bearer.Any(char.IsWhiteSpace) || bearer.Any(char.IsControl))
                throw Invalid("The trusted credential returned an invalid bearer token.");
            request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", bearer);
            if (request.Headers.TryGetValues("MCP-Protocol-Version", out var versions))
                ProtocolVersion = versions.Single();
            var response = await base.SendAsync(request, token).ConfigureAwait(false);
            try
            {
                if ((int)response.StatusCode is >= 300 and < 400)
                    throw Invalid("MCP redirects are prohibited.");
                CaptureSession(response);
                if (request.Method == HttpMethod.Delete) return response;
                if (method == "server/discover" && response.StatusCode is
                    HttpStatusCode.BadRequest or HttpStatusCode.NotFound or HttpStatusCode.MethodNotAllowed)
                {
                    // Preserve bounded protocol-negotiation errors for the SDK's documented fallback.
                    var content = response.Content;
                    ValidateResponseSize(content);
                    await using var stream = await content.ReadAsStreamAsync(token).ConfigureAwait(false);
                    var negotiationBytes = await ReadBytesAsync(stream, MaxDownloadBytes, token).ConfigureAwait(false);
                    var mediaType = content.Headers.ContentType?.MediaType?.ToLowerInvariant();
                    if (mediaType == "application/json" || mediaType?.EndsWith("+json", StringComparison.Ordinal) == true)
                    {
                        using var error = ParseJson(negotiationBytes);
                    }
                    response.Content = new ByteArrayContent(negotiationBytes);
                    response.Content.Headers.ContentType = content.Headers.ContentType;
                    content.Dispose();
                    return response;
                }
                if (!response.IsSuccessStatusCode)
                    throw new HttpRequestException("FAM Skills: MCP HTTP request failed.", null, response.StatusCode);
                if (id.ValueKind == JsonValueKind.Undefined)
                {
                    if (response.StatusCode is not (HttpStatusCode.Accepted or HttpStatusCode.NoContent) ||
                        response.Content.Headers.ContentLength > 0)
                        throw Invalid("Unexpected MCP notification response.");
                    await using (var acknowledgement = await response.Content.ReadAsStreamAsync(token).ConfigureAwait(false))
                    {
                        if (await acknowledgement.ReadAsync(new byte[1].AsMemory(), token).ConfigureAwait(false) != 0)
                            throw Invalid("MCP notification acknowledgements must have an empty body.");
                    }
                    response.Content.Dispose();
                    response.Content = new ByteArrayContent([]);
                    return response;
                }
                var bytes = await ReadRpcResponseAsync(response.Content, token).ConfigureAwait(false);
                using var document = ParseJson(bytes);
                var rpc = document.RootElement;
                RequireObject(rpc);
                if (RequiredString(rpc, "jsonrpc") != "2.0" || rpc.TryGetProperty("method", out _) ||
                    rpc.TryGetProperty("result", out _) == rpc.TryGetProperty("error", out _) ||
                    !rpc.TryGetProperty("id", out var responseId) ||
                    responseId.GetRawText() != id.GetRawText())
                    throw Invalid("Unexpected MCP server request or response identity.");
                if (method == "initialize" && rpc.TryGetProperty("result", out var initialized))
                {
                    var version = RequiredString(initialized, "protocolVersion");
                    if (!Regex.IsMatch(version, @"\A[0-9]{4}-[0-9]{2}-[0-9]{2}\z"))
                        throw Invalid("Invalid MCP negotiated protocol version.");
                    ProtocolVersion = version;
                }
                if (resource is not null)
                {
                    if (rpc.TryGetProperty("error", out _)) throw Invalid("Required MCP Skill resource could not be read.");
                    var content = OneResource(rpc.GetProperty("result"), resource);
                    if (resource == IndexUri)
                        bytes = FilterIndex(id, content);
                    else if (Volatile.Read(ref _selected)[resource].Type == "skill-md")
                        VerifySkillMarkdown(resource, content);
                    else
                        await VerifyArchiveAsync(resource, content, token).ConfigureAwait(false);
                }
                response.Content.Dispose();
                response.Content = new ByteArrayContent(bytes);
                response.Content.Headers.ContentType = new MediaTypeHeaderValue("application/json");
                return response;
            }
            catch { response.Dispose(); throw; }
        }

        private void CaptureSession(HttpResponseMessage response)
        {
            if (!response.Headers.TryGetValues("Mcp-Session-Id", out var values)) return;
            var id = values.Single();
            if (id.Length == 0 || id.Any(c => c < 0x21 || c > 0x7e) ||
                SessionId is not null && SessionId != id)
                throw Invalid("Unexpected MCP session identity.");
            SessionId = id;
        }

        private byte[] FilterIndex(JsonElement id, JsonElement content)
        {
            if (content.TryGetProperty("blob", out _)) throw Invalid("The MCP index must be one text resource.");
            using var document = ParseJson(StrictUtf8.GetBytes(RequiredString(content, "text")));
            var index = document.RootElement;
            RequireObject(index);
            if (!index.TryGetProperty("skills", out var skills) || skills.ValueKind != JsonValueKind.Array ||
                skills.GetArrayLength() == 0) throw Invalid("Required MCP index is missing or empty.");
            const string schema = "https://schemas.agentskills.io/discovery/0.2.0/schema.json";
            if (OptionalString(index, "$schema") is { } actualSchema && actualSchema != schema)
                throw Invalid("Unsupported MCP Skill index schema.");
            var expected = inventory.Skills.ToDictionary(e => e.Name, StringComparer.Ordinal);
            var selected = new Dictionary<string, McpSelection>(StringComparer.Ordinal);
            var seen = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            foreach (var item in skills.EnumerateArray())
            {
                RequireObject(item);
                var name = OptionalString(item, "name");
                if (name is null) continue;
                if (!seen.Add(name)) throw Invalid("Duplicate MCP index identity.");
                if (!expected.Remove(name, out var entry)) continue;
                if (item.EnumerateObject().Any(p => p.Name is not ("name" or "type" or "description" or "url" or "digest")))
                    throw Invalid("Unsupported selected MCP Skill discovery metadata.");
                var type = RequiredString(item, "type");
                if (type is not ("archive" or "skill-md"))
                    throw Invalid("Selected MCP Skills must use archive or skill-md distribution.");
                var url = RequiredString(item, "url");
                if (url == IndexUri || url.Any(char.IsControl) || !Uri.TryCreate(url, UriKind.Absolute, out _))
                    throw Invalid("Invalid MCP Skill resource URI.");
                var description = RequiredString(item, "description");
                if (description.EnumerateRunes().Count() > 1024) throw Invalid("MCP Skill description exceeds 1024 characters.");
                var digest = item.TryGetProperty("digest", out var rawDigest) && rawDigest.ValueKind == JsonValueKind.Null
                    ? null : OptionalString(item, "digest");
                if (digest is not null && (!digest.StartsWith("sha256:", StringComparison.Ordinal) ||
                    !DigestPattern.IsMatch(digest[7..]))) throw Invalid("Invalid MCP Skill digest.");
                if (!selected.TryAdd(url, new McpSelection(entry, type, url, description, digest)))
                    throw Invalid("Selected Skills share an ambiguous MCP resource URI.");
            }
            if (expected.Count != 0) throw Invalid("The MCP index omits a locked Skill.");
            Volatile.Write(ref _selected, selected);
            var filtered = JsonSerializer.Serialize(new Dictionary<string, object>
            {
                ["$schema"] = schema,
                ["skills"] = selected.Values.Select(s => new
                {
                    name = s.Entry.Name, type = s.Type, description = s.Description, url = s.Url, digest = s.Digest
                }).ToArray()
            });
            return JsonSerializer.SerializeToUtf8Bytes(new
            {
                jsonrpc = "2.0", id,
                result = new { contents = new[] { new { uri = IndexUri, mimeType = "application/json", text = filtered } } }
            });
        }

        private void VerifySkillMarkdown(string resource, JsonElement content)
        {
            if (content.TryGetProperty("blob", out _)) throw Invalid("MCP SKILL.md must be one text resource.");
            var selection = Volatile.Read(ref _selected)[resource];
            string markdown;
            try { markdown = RequiredString(content, "text"); }
            catch (InvalidOperationException) { throw Invalid("MCP SKILL.md text must contain valid Unicode."); }
            byte[] bytes;
            try
            {
                if (StrictUtf8.GetByteCount(markdown) > MaxSkillBytes)
                    throw Invalid("SKILL.md exceeds the FAM 64 MiB safety guard.");
                bytes = StrictUtf8.GetBytes(markdown);
            }
            catch (EncoderFallbackException) { throw Invalid("MCP SKILL.md text must contain valid Unicode."); }
            var text = ValidateSkill(selection.Entry, bytes, Path.Combine(cacheDirectory, selection.Entry.Name));
            if (selection.Digest is not null && selection.Digest != "sha256:" + selection.Entry.Sha256)
                throw Invalid("MCP SKILL.md differs from its advertised SHA-256.");
            if (text.Description != selection.Description) throw Invalid("MCP index and SKILL.md descriptions disagree.");
            Verified[selection.Entry.Name] = text;
        }

        private async Task VerifyArchiveAsync(string resource, JsonElement content, CancellationToken cancellationToken)
        {
            if (content.TryGetProperty("text", out _)) throw Invalid("An MCP Skill archive must be a binary resource.");
            var selection = Volatile.Read(ref _selected)[resource];
            var encoded = RequiredString(content, "blob");
            if ((long)encoded.Length > ((long)MaxDownloadBytes + 2) / 3 * 4)
                throw Invalid("MCP archive exceeds the FAM 256 MiB download safety guard.");
            byte[] bytes;
            try { bytes = Convert.FromBase64String(encoded); }
            catch (FormatException) { throw Invalid("Invalid MCP archive base64."); }
            var digest = Convert.ToHexString(SHA256.HashData(bytes)).ToLowerInvariant();
            if (bytes.Length > MaxDownloadBytes ||
                selection.Digest is not null && selection.Digest != "sha256:" + digest ||
                selection.Entry.ArchiveSha256 is not null && selection.Entry.ArchiveSha256 != digest)
                throw Invalid("MCP archive differs from its locked or advertised SHA-256.");
            var text = await ValidateArchiveAsync(selection.Entry, bytes,
                Path.Combine(cacheDirectory, selection.Entry.Name), cancellationToken).ConfigureAwait(false);
            if (text.Description != selection.Description) throw Invalid("MCP index and SKILL.md descriptions disagree.");
            Verified[selection.Entry.Name] = text;
        }
    }

    private static JsonElement OneResource(JsonElement result, string uri)
    {
        RequireObject(result);
        if (!result.TryGetProperty("contents", out var contents) || contents.ValueKind != JsonValueKind.Array ||
            contents.GetArrayLength() != 1) throw Invalid("Expected exactly one MCP Skill resource content block.");
        var content = contents[0];
        RequireObject(content);
        if (RequiredString(content, "uri") != uri) throw Invalid("MCP returned a different resource identity.");
        return content;
    }

    private static async Task<byte[]> ReadRpcResponseAsync(HttpContent content, CancellationToken cancellationToken)
    {
        ValidateResponseSize(content);
        await using var stream = await content.ReadAsStreamAsync(cancellationToken).ConfigureAwait(false);
        using var bounded = new BoundedReadStream(stream, MaxDownloadBytes);
        switch (content.Headers.ContentType?.MediaType?.ToLowerInvariant())
        {
            case "application/json":
                return await ReadBytesAsync(bounded, MaxDownloadBytes, cancellationToken).ConfigureAwait(false);
            case "text/event-stream":
                var parser = SseParser.Create<byte[]>(bounded, static (_, data) => data.ToArray());
                await foreach (var item in parser.EnumerateAsync(cancellationToken).ConfigureAwait(false))
                {
                    if (item.Data.Length == 0) continue;
                    if (item.EventType != "message") throw Invalid("Unexpected MCP SSE event.");
                    return item.Data;
                }
                throw Invalid("MCP SSE stream ended without a response.");
            default: throw Invalid("Expected an MCP JSON or SSE response.");
        }
    }

    private static void ValidateResponseSize(HttpContent content)
    {
        if (content.Headers.ContentLength > MaxDownloadBytes || content.Headers.ContentEncoding.Count != 0)
            throw Invalid("MCP response exceeds its download guard or uses unsupported content encoding.");
    }

    private static async Task<SkillText> ValidateArchiveAsync(Entry entry, byte[] bytes, string directory,
        CancellationToken cancellationToken)
    {
        using var archive = new ZipArchive(new MemoryStream(bytes, writable: false), ZipArchiveMode.Read);
        if (archive.Entries.Count != 1 || archive.Entries[0].FullName != "SKILL.md")
            throw Invalid("Instructions-only ZIPs must contain exactly one root SKILL.md; no extra entries.");
        var file = archive.Entries[0];
        var kind = (file.ExternalAttributes >> 16) & 0xf000;
        if (kind is not (0 or 0x8000) || file.IsEncrypted ||
            (file.ExternalAttributes & (int)(FileAttributes.Directory | FileAttributes.Device | FileAttributes.ReparsePoint)) != 0)
            throw Invalid("ZIP Skill entries must be regular, unencrypted files, never links or devices.");
        if (file.Length > MaxSkillBytes) throw Invalid("SKILL.md exceeds the FAM 64 MiB decompression safety guard.");
        await using var stream = file.Open();
        var content = await ReadBytesAsync(stream, MaxSkillBytes, cancellationToken).ConfigureAwait(false);
        if (content.LongLength != file.Length || ComputeCrc32(content) != file.Crc32)
            throw Invalid("Corrupt or truncated SKILL.md ZIP entry.");
        return ValidateSkill(entry, content, directory);
    }

    private static uint ComputeCrc32(byte[] bytes)
    {
        uint crc = uint.MaxValue;
        foreach (var value in bytes) crc = CrcTable[(crc ^ value) & 255] ^ (crc >> 8);
        return ~crc;
    }

    private static readonly uint[] CrcTable = Enumerable.Range(0, 256).Select(value =>
    {
        var crc = (uint)value;
        for (var bit = 0; bit < 8; bit++) crc = (crc >> 1) ^ ((crc & 1) != 0 ? 0xedb88320u : 0u);
        return crc;
    }).ToArray();

    private static async Task<byte[]> ReadBytesAsync(Stream stream, int limit, CancellationToken cancellationToken)
    {
        using var output = new MemoryStream();
        var buffer = new byte[81920];
        int count;
        while ((count = await stream.ReadAsync(buffer.AsMemory(), cancellationToken).ConfigureAwait(false)) != 0)
        {
            if (output.Length + count > limit) throw Invalid("Content exceeds an existing FAM safety guard.");
            output.Write(buffer, 0, count);
        }
        return output.ToArray();
    }

    private sealed class BoundedReadStream(Stream inner, long limit) : Stream
    {
        private long _read;
        private int Count(int count)
        {
            _read += count;
            if (_read > limit) throw Invalid("MCP response exceeded the FAM download safety guard.");
            return count;
        }
        public override int Read(byte[] buffer, int offset, int count) => Count(inner.Read(buffer, offset, count));
        public override int Read(Span<byte> buffer) => Count(inner.Read(buffer));
        public override async ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken cancellationToken = default) =>
            Count(await inner.ReadAsync(buffer, cancellationToken).ConfigureAwait(false));
        public override Task<int> ReadAsync(byte[] buffer, int offset, int count, CancellationToken cancellationToken) =>
            ReadAsync(buffer.AsMemory(offset, count), cancellationToken).AsTask();
        public override bool CanRead => true;
        public override bool CanSeek => false;
        public override bool CanWrite => false;
        public override long Length => throw new NotSupportedException();
        public override long Position { get => _read; set => throw new NotSupportedException(); }
        public override void Flush() => throw new NotSupportedException();
        public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
        public override void SetLength(long value) => throw new NotSupportedException();
        public override void Write(byte[] buffer, int offset, int count) => throw new NotSupportedException();
    }
}

#pragma warning restore MAAI001
