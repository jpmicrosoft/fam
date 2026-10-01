using FAM.Skills;
using Microsoft.Agents.AI;

if (args.Length > 1 || args.Length == 1 && args[0] is not ("--self-test" or "--self-test-require-symlinks"))
    throw new ArgumentException("Usage: HostedSkills.Example [--self-test|--self-test-require-symlinks]");

if (args.Length == 1)
{
    await RuntimeTests.RunAsync(args[0] == "--self-test-require-symlinks");
}
else
{
    var manifest = Path.Combine(AppContext.BaseDirectory, "fam_skills", "manifest.json");
    await using var runtime = await FamSkillsRuntime.OpenAsync(manifest);
    using var model = new RecordingModel("greeting", "FAM_DOTNET_SKILL_LOADED");
    var agent = new ChatClientAgent(model, new ChatClientAgentOptions
    {
        AIContextProviders = [runtime.Provider]
    });
    var response = await agent.RunAsync("Demonstrate progressive Skill loading.");
    Check.That(model.Advertised && model.Loaded, "The real provider must advertise and load the Skill.");
    Console.WriteLine(response.Text);
}
