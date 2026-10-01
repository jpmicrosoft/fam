using System.Text.Json;
using Microsoft.Agents.AI;
using Microsoft.Extensions.AI;

internal sealed class RecordingModel(string skillName, string marker, bool requestUnrelated = false) : IChatClient
{
    public bool Advertised { get; private set; }
    public string Advertisement { get; private set; } = "";
    public bool Loaded { get; private set; }
    public string? LoadedContent { get; private set; }
    private int _calls;

    public Task<ChatResponse> GetResponseAsync(IEnumerable<ChatMessage> messages,
        ChatOptions? options = null, CancellationToken cancellationToken = default)
    {
        cancellationToken.ThrowIfCancellationRequested();
        var input = messages.ToArray();
        var functions = options?.Tools?.OfType<AIFunction>().ToArray() ?? [];
        Check.That(functions.All(f => f.Name is "load_skill" or "unrelated"),
            "Only load_skill and the application's unrelated tool may be exposed.");
        var load = functions.Single(f => f.Name == "load_skill");
        Check.That(load is not ApprovalRequiredAIFunction, "Only this provider's load_skill opts out of approval.");
        if (requestUnrelated)
            Check.That(functions.Single(f => f.Name == "unrelated") is ApprovalRequiredAIFunction,
                "Unrelated tool approval must remain intact.");
        if (_calls++ == 0)
        {
            var advertisement = (options?.Instructions ?? "") + string.Join("\n", input.Select(m => m.Text));
            Advertisement = advertisement;
            Check.That(advertisement.Contains(skillName, StringComparison.Ordinal), "Skill name must be advertised.");
            Check.That(!advertisement.Contains(marker, StringComparison.Ordinal), "Instructions must not be eagerly injected.");
            Advertised = true;
            var parameters = load.JsonSchema.GetProperty("properties").EnumerateObject().ToArray();
            Check.That(parameters.Length == 1, "Pinned SDK load_skill must take exactly one Skill name.");
            return Task.FromResult(Call("load-1", load.Name,
                new Dictionary<string, object?> { [parameters[0].Name] = skillName }));
        }
        var result = input.SelectMany(m => m.Contents).OfType<FunctionResultContent>()
            .Single(r => r.CallId == "load-1");
        LoadedContent = result.Result is JsonElement json ? json.ToString() : result.Result?.ToString();
        Check.That(LoadedContent?.Contains(marker, StringComparison.Ordinal) == true,
            "Actual load_skill result must contain the instruction marker.");
        Loaded = true;
        if (requestUnrelated)
            return Task.FromResult(Call("unrelated-1", "unrelated", new Dictionary<string, object?>()));
        return Task.FromResult(new ChatResponse(new ChatMessage(ChatRole.Assistant,
            "PASS: real provider advertised greeting and load_skill returned its locked instructions.")));
    }

    private static ChatResponse Call(string id, string name, Dictionary<string, object?> arguments) =>
        new(new ChatMessage(ChatRole.Assistant, [new FunctionCallContent(id, name, arguments)]));

    public IAsyncEnumerable<ChatResponseUpdate> GetStreamingResponseAsync(IEnumerable<ChatMessage> messages,
        ChatOptions? options = null, CancellationToken cancellationToken = default) =>
        throw new NotSupportedException("This deterministic fake implements non-streaming requests only.");

    public object? GetService(Type serviceType, object? serviceKey = null) =>
        serviceKey is null && serviceType.IsInstanceOfType(this) ? this : null;

    public void Dispose() { }
}

internal static class Check
{
    internal static void That(bool condition, string message)
    {
        if (!condition) throw new InvalidOperationException(message);
    }

    internal static async Task RejectsAsync(Func<Task> action, string message)
    {
        try { await action(); }
        catch (InvalidDataException) { return; }
        catch (JsonException) { return; }
        catch (FileNotFoundException) { return; }
        catch (DirectoryNotFoundException) { return; }
        throw new InvalidOperationException(message);
    }
}
