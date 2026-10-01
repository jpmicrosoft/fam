# .NET Hosted Skills runtime

This example uses the real `Microsoft.Agents.AI` Skill provider with a
deterministic, local `IChatClient`. It never connects to Azure or invokes a model
service. The fake model requests the SDK's actual `load_skill` function and
checks the returned instructions, rather than substituting a fake provider.

Both delivery modes are implemented independently. MCP uses the real
`UseMcpSkills` extension on a long-lived authenticated client; a validating
transport restricts discovery and validates direct `skill-md` text or complete
`archive` packages **before** the SDK receives them. It never falls back to a
logical-default Toolbox or bundle.

## Dependencies and commands

Use .NET SDK **10.0.401**, matching the runtime CI gate, with these pinned
dependencies:

| Package | Version | Purpose |
| --- | --- | --- |
| `Microsoft.Agents.AI` | `1.23.0` | Real filesystem Skill discovery and `AgentSkillsProvider` |
| `Microsoft.Agents.AI.Mcp` | `1.23.0-alpha.260928.1` | Real MCP Skills provider |
| `YamlDotNet` | `16.3.0` | Strict frontmatter parsing without a handwritten YAML subset |

MCP's package metadata depends on `Microsoft.Agents.AI` 1.23.0,
`ModelContextProtocol` 2.2.0, and `Microsoft.Extensions.AI.Abstractions`
10.10.1. The adapter also uses .NET 10 ZIP integrity metadata and SSE parsing.

The required `AIContextProvider.InvokingContext` preview API is acknowledged by
file-local `MAAI001` pragmas. Other diagnostics remain enabled, and the example
treats warnings as errors.

Run from the repository root with .NET SDK 10.0.401. These commands are
verification instructions for the selected source revision, separate from the
Go test suite. Rerun them after runtime fixes or dependency/packaging changes.

**PowerShell (Windows):**

```powershell
$project = '.\examples\hosted-skills\dotnet\HostedSkills.Example.csproj'
$artifacts = Join-Path $env:TEMP ('fam-skills-dotnet-' + [guid]::NewGuid().ToString('N'))
dotnet build $project -c Release --artifacts-path $artifacts --no-incremental
dotnet run --project $project -c Release --artifacts-path $artifacts --no-build
dotnet run --project $project -c Release --artifacts-path $artifacts --no-build -- --self-test
dotnet publish $project -c Release --artifacts-path $artifacts --no-restore -o "$artifacts\published"
dotnet "$artifacts\published\HostedSkills.Example.dll" --self-test
```

**POSIX shell (Linux/macOS with the required SDK):**

```bash
project='./examples/hosted-skills/dotnet/HostedSkills.Example.csproj'
artifacts="$(mktemp -d)"
dotnet build "$project" -c Release --artifacts-path "$artifacts" --no-incremental
dotnet run --project "$project" -c Release --artifacts-path "$artifacts" --no-build
dotnet run --project "$project" -c Release --artifacts-path "$artifacts" --no-build -- --self-test-require-symlinks
dotnet publish "$project" -c Release --artifacts-path "$artifacts" --no-restore -o "$artifacts/published"
dotnet "$artifacts/published/HostedSkills.Example.dll" --self-test-require-symlinks
```

The POSIX commands are portable guidance, not qualification for every operating
system. Use a filesystem that supports symlinks for strict checks. Require a
clean build/publish and inspect the results from both output assemblies.
Compare complete relative-file inventories and SHA-256 hashes under their
`fam_skills` directories, including the helper against the source template.

The [Linux CI gate](../../../docs/development-and-releases.md#hosted-skills-runtime-ci-gate)
builds with `--no-incremental`, publishes with `--no-restore`, and runs
`--self-test-require-symlinks` against **both** output assemblies. It then
compares complete `fam_skills` file/hash inventories and the helper's source
hash. Require a successful run on the final candidate source, not just earlier
Windows or Linux results. Keep run counts, skips, tool versions, and image
digests with the tested revision in PR or release-qualification evidence.

Also test a local Skill attached and synchronized by the built FAM CLI into
the application's .NET workspace. The published application should advertise
and load that actual CLI-generated artifact, including ownership and helper
hashes, without regenerating the lock or authenticating for bundle loading.

Use an absolute scratch artifact directory for your own environment; these
commands keep restore/build/publish output outside the source tree.

Use `--self-test-require-symlinks` instead of `--self-test` in a Windows
Developer Mode/admin-enabled or Unix CI environment. Normal self-tests report a
symlink-permission skip explicitly; the strict variant fails instead. On Windows,
the `IOException` skip is limited to `HRESULT_FROM_WIN32(ERROR_PRIVILEGE_NOT_HELD)`
(`0x80070522`, Win32 error 1314); unrelated I/O errors still fail.

For offline bundle-container qualification, execute the published application
as a non-root user with networking disabled and no credential directories
mounted. Use the pinned SDK, test the intended container architecture, and
rerun after adapter or packaging changes. A previous container run is not
evidence for a new image.

Both built and published self-tests read the **output** `fam_skills` directory
through `AppContext.BaseDirectory`, with no source-tree fallback. The example-only build
target creates a lock from the synthetic fixture's exact bytes, including the
checkout's line endings. It is not a production synchronization mechanism.
**Do not regenerate production digests during build or startup.**

Tests cover actual advertisement and tool results, deferred instruction loading,
CRLF preservation, SDK snapshots, 64-character names, 1024-character descriptions,
instructions larger than one MiB, explicit detach, duplicate JSON/YAML fields,
33 selected Skills with more than eight MiB of aggregate content,
invalid UTF-8, identity mismatch, digest mismatch, missing files, traversal,
extra files/directories, links, cancellation, disposal, trusted project binding,
and preservation of an actual unrelated tool approval request. MCP tests use
the real `HttpClientTransport`, `McpClient`, and `UseMcpSkills` with an in-process
fake HTTP handler. They cover JSON/SSE, pre-download allowlisting, full ZIP
validation, digests, missing/empty indices, token refresh, pinned endpoints,
redirect rejection, cancellation, visible cleanup failures, and host lifetime.
They also exercise automatic protocol negotiation, the SDK's actual discovery
probe timeout and initialize fallback, and zero HTTP/credential calls on detach.
Notification regressions exercise empty 202/204 acknowledgements with known and
unknown lengths, reject other statuses and body-bearing responses, and assert
one-byte reads even for bodies falsely declaring `Content-Length: 0`.
Direct `skill-md` cases cover actual SDK advertisement/loading, exact CRLF and
UTF-8 content, optional text digests, archive-provenance separation, cached
instructions, one-block resource identity/type checks, malformed UTF-8/Unicode,
and rejected instructions-only metadata violations.
The synthetic fixtures are not service captures, and the offline harness makes
no Azure calls.

### Separate live MCP qualification

An authorized check on 2026-10-01 (UTC) exercised the public `OpenAsync` adapter
and actual `UseMcpSkills` provider against a real Foundry Toolbox. It consumed a
FAM-synchronized .NET artifact pinned to Toolbox v1 and Skill v1 after the
Skill's default moved to v2. The unique v1 marker was absent before the actual
`load_skill` call and present in its result; only the selected load tool was
exposed. The check used renewable subscription-bound credentials and completed
local cleanup.

The live service returned an empty HTTP 204 notification acknowledgement and
the `skill-md` discovery form. Both are now covered by the adapter's regression
tests alongside the existing 202 and archive forms. That first check was
**local .NET provider execution with real Foundry MCP and a fake model**.

A subsequent run also qualified a real .NET Responses host: FAM/azd code
deployment consumed a bundled Skill, and a digest-pinned, non-root Linux
`amd64` prebuilt image consumed the selected Skill through real Foundry MCP.
Both actual model responses matched markers absent from the invocation prompt.
The host kept the public `OpenAsync` runtime alive alongside its existing
application context provider; no global tool-approval override was installed.
It used `Microsoft.Agents.AI.Foundry.Hosting` `1.23.0-preview.260928.1`,
`Azure.AI.Projects` `3.0.0-beta.3`, `Azure.Identity` `1.21.0`, and Responses
protocol `2.0.0`, in addition to the adapter pins above. These hosting packages
belong to the host application, not this offline example project.
See the [shared coverage boundary](../README.md#live-qualification-boundary)
for tooling versions, identity requirements, and representative packaging limits.
These observations do not qualify subsequent runtime or packaging changes;
retain fresh evidence for the actual release revision.

## Integrate an existing application

Synchronization materializes the single adapter at
`fam_skills\FamSkillsRuntime.cs`. It does not rewrite the application's entry
point. Add the pinned dependencies and register the returned **filtered**
provider alongside the application's existing context providers:

```csharp
using FAM.Skills;
using Microsoft.Agents.AI;

await using var skills = await FamSkillsRuntime.OpenAsync(
    Path.Combine(AppContext.BaseDirectory, "fam_skills", "manifest.json"),
    projectEndpoint: trustedProjectEndpoint,
    cancellationToken: stoppingToken,
    getBearerToken: ct => GetFreshProjectBearerTokenAsync(ct));

var options = new ChatClientAgentOptions
{
    // Preserve existing ChatOptions, tools, context providers, and approval policy.
    AIContextProviders = [.. existingContextProviders, skills.Provider]
};
var agent = new ChatClientAgent(existingChatClient, options);
await RunExistingHostAsync(agent, stoppingToken);
```

The host/model variables and `GetFreshProjectBearerTokenAsync` belong to the
existing application; they are not additional APIs exported by this adapter.
The token hook has signature `Func<CancellationToken, ValueTask<string>>`.
Call the application's renewable Foundry credential/token provider inside that
hook, requesting scope **`https://ai.azure.com/.default`**; do not capture one
startup token. For an existing `Azure.Core.TokenCredential`, the callback can
await `GetTokenAsync(new TokenRequestContext(["https://ai.azure.com/.default"]), ct)`
and return the resulting `AccessToken.Token`. The adapter does not require a new
credential package or take ownership of the application's credential.
The hook is required only for nonempty MCP inventories and is never called in
bundle mode or explicit detach. Add `skills.Provider` to an existing provider
collection rather than replacing that collection.

`OpenAsync` returns an `IAsyncDisposable` runtime, not a bare SDK provider.
Retain it for the entire agent/host lifetime. Before disposing it, stop accepting
new agent invocations and await completion of in-flight invocations, requesting
cancellation if needed.
`await using` also disposes on agent construction or host failure.
The application continues to own its existing model client and host. There are
no network clients, credentials, or sessions in bundle mode.

The provider and every adapter-issued `load_skill` function become invalid
after runtime disposal, including functions retained in a caller's tool cache.
The adapter uses the SDK's `DelegatingAIFunction` to preserve the underlying
function's metadata and JSON schema while checking disposal and cancellation
before and after invocation. If either check fails, the invocation does not
return a successful result. This guard does not undo work that already ran,
revoke previously returned instructions, or replace the host's responsibility
to stop invocations before teardown.

`OpenAsync` links the caller's cancellation token to one **60-second startup
deadline** covering bundle or MCP discovery and readiness, in addition to the
MCP request/initialization timeouts. This is cooperative cancellation, not
process preemption: synchronous file reads/parsing and application callbacks
that ignore cancellation cannot be forcibly interrupted by the token. Supply
a cancellation-aware renewable token callback and propagate startup failures
instead of continuing with a Skills-free agent.

`Provider` is an `AIContextProvider` wrapping a real `AgentSkillsProvider`.
Only its own `load_skill` is exposed, with approval disabled on that provider's
specific tool. No name-based global approval rule is installed. The adapter
does not expose `read_skill_resource` or `run_skill_script`, does not supply a
script runner, and does not grant tools from `allowed-tools` metadata.

Files and exact-byte digests are validated before SDK discovery. The SDK's
advertised names/descriptions and loaded content must match the complete lock;
SDK omission or deduplication cannot silently succeed. The adapter reads
`manifest.json` under the existing **8 MiB manifest guard**, retains its SHA-256
digest, and rechecks it during startup, including before readiness. A changed
manifest is a startup consistency failure, not an implicit reload.

After successful startup, validated file-backed Skill objects retain their
content snapshot for the runtime lifetime; this behavior is unchanged by the
startup manifest checks. New runtime instances revalidate disk contents.
Deploy artifacts read-only; this is not a hot-reloading Skill store or a defense
against a process that can rewrite the application and its trusted manifest.

## Copy the complete synchronized artifact on build and publish

For an existing SDK-style project with `fam_skills` under its project root,
the default `Compile` glob already compiles `FamSkillsRuntime.cs`. Add this
content rule to copy **all** files, including the helper, manifest, ownership
record, and the exact `SKILL.md` bytes:

```xml
<ItemGroup>
  <Content Include="fam_skills\**\*"
           CopyToOutputDirectory="PreserveNewest"
           CopyToPublishDirectory="PreserveNewest" />
</ItemGroup>
```

If the project already declares these as `Content`, use `Update` instead of
adding duplicate items. Do not include arbitrary support files just because the
MSBuild glob copies them: runtime validation rejects them. In containers, copy
the actual publish output and ensure `.dockerignore` does not remove
`fam_skills`. Prebuilt images must already contain this integration; changing a
bundle requires rebuilding the image. Deployment metadata cannot inject files.

## Lock contract and safety limits

`manifest.json` uses `formatVersion: 1`, `mode: "bundle"`, and `skills` entries
containing `name`, lowercase `sha256`, and exactly `name/SKILL.md` as the
relative `path`. A local Skill omits `version`; a versioned Skill requires an
immutable `version` and `projectEndpoint` matching the trusted application
argument. The root contains one directory per selected Skill, each containing
exactly one `SKILL.md`. The only optional non-Skill siblings accepted are
`.ownership.json` and `FamSkillsRuntime.cs`.

`service`, `language`, `declarationHash`, source/archive provenance, image
evidence, and future inert fields can coexist with the baseline lock. Duplicate
JSON properties are rejected even in ignored metadata. An explicitly supplied
language must be `dotnet`. The adapter does not recompute the declaration hash:
FAM synchronization validates declarations and ownership before deployment.

An explicit empty `skills: []` detaches Skills and exposes no Skill tools. It is
not equivalent to an MCP server omitting the index for required Skills.
Names are limited to 64 ASCII characters and descriptions to 1024 Unicode
characters, following the Azure Skills documentation. The 8 MiB manifest,
64 MiB instruction-file, and 256 MiB artifact-read guards reuse FAM safety
bounds; they are not Azure quotas. No 32-Skill, one-MiB-body, or eight-MiB-total
product limit is imposed.
Those guards do not reserve enough memory for every accepted inventory or
concurrent host. Startup validation and SDK snapshots retain content from the
selected inventory; memory use scales with its instruction bytes and may
include multiple representations. Size and monitor the real application for
parsing, validation, SDK snapshots, and concurrent invocations using the
[host resource guidance](../../../docs/hosted-agents.md#host-resources-and-memory).

## MCP validation and lifetime

MCP locks require `projectEndpoint`, `toolboxName`, `toolboxVersion`, and explicit
versions for each selected Skill. They omit bundle `path` entries/directories.
The required `sha256` pins exact SKILL.md bytes in both distribution formats.
`archiveSHA256`, when present, additionally pins the ZIP when MCP serves an
`archive`. For direct `skill-md` delivery it remains synchronization provenance:
a text-only response cannot verify the source ZIP digest or attest to files
not returned. Synchronization remains responsible for validating the complete
source package. The runtime never compares a ZIP digest against Markdown bytes.

The adapter constructs the immutable Toolbox route documented in
[Foundry Toolbox: Verify tooling](https://learn.microsoft.com/en-us/azure/foundry/agents/how-to/tools/toolbox#verify-tooling):
`{projectEndpoint}/toolboxes/{name}/versions/{version}/mcp?api-version=v1`.
It uses bearer scope `https://ai.azure.com/.default` through the caller's
renewable token callback. No preview header is required or added at this MCP
endpoint. Sync verifies pinned Toolbox references and downloads selected
immutable versions; runtime verifies the same project, Toolbox version, selected
names, and locked digests. A separate
`version` field in the MCP index is not required.

The transport accepts Streamable HTTP JSON/SSE responses, with no redirects,
legacy SSE endpoint fallback, cookies, or unsolicited server-request stream.
The SDK negotiates the protocol instead of requiring one exact date revision.
Its read-only `server/discover` probe can fall back to the documented
`initialize`/`notifications/initialized` handshake on the same pinned endpoint;
bounded negotiation errors/timeouts are left to the SDK's negotiation logic.
Authentication failures, redirects and invalid responses still fail readiness.
No Toolbox tool RPC is permitted.

Notification acknowledgements must be empty HTTP 202 or HTTP 204 responses.
The additional 204 acceptance accommodates the observed Foundry
`notifications/initialized` response; it is service compatibility, not a change
to the MCP specification. A one-byte EOF probe under the request deadline
verifies empty bodies even when `Content-Length` is absent or zero. Other
statuses and any body bytes are rejected.
This notification rule does **not** apply to session-termination DELETE:
HTTP 202 is not a confirmed deletion.

Every HTTP request, including session cleanup, obtains a fresh bearer token
through the supplied renewable credential callback. Requests and initialization
have 60-second safety timeouts; startup also has the linked overall deadline
described above. Skill resource URIs are opaque `resources/read`
arguments sent only to the pinned Toolbox; they never choose a local file or
another HTTP download host.

The required index must be nonempty, duplicate-free, and include every selected
name. Unselected entries are removed before the SDK can request their content.
Selected entries must use `archive` or `skill-md`; unsupported discovery metadata
and types fail rather than being silently dropped. Direct `skill-md` delivery,
as observed from Foundry, uses the SDK's native text loader without synthesizing
an archive. It requires exactly one matching text resource, strict UTF-8,
the locked instruction SHA-256, matching name/description, and the same
instructions-only frontmatter validation as bundles and archives. A supplied
index `digest` must match the UTF-8 Markdown bytes. Neither supporting-resource
reads nor script/tool calls are allowed, even if referenced in instruction text.

For `archive`, each complete ZIP is checked before SDK extraction: exactly one
regular, unencrypted root `SKILL.md`, bounded decompression, valid length/CRC32, matching package identity,
description and content hash, plus advertised/locked archive hashes when present.
Scripts, resources, directories, links, duplicate/case-colliding entries,
traversal, corrupted data and overflow fail readiness rather than being dropped.

The SDK's extraction limits are explicitly set to FAM's existing 256 MiB download
and 64 MiB decompression guards, with one file because this integration is
instructions-only. These are not Azure quotas or the SDK's lower one-MiB
defaults. The actual provider's `load_skill` results are checked before returning
the runtime. SDK empty-list/skipped-archive behavior cannot become readiness.

The client, transport and private extraction directory remain alive for the
runtime lifetime. When a session ID was recorded, disposal explicitly requests
termination at the pinned endpoint. Only HTTP **200**, **204**, or **404**
is accepted; 404 means the session is already absent. HTTP **202** and other
2xx responses are uncertain outcomes and fail cleanup rather than being
reported as confirmed termination.

Disposal also releases HTTP resources and removes only the runtime-owned cache.
Cleanup continues through failures and reports collected errors. A failed or
timed-out DELETE does not establish remote cleanup, and an accepted response
does not prove that every server-side resource was released. Ordinary Toolbox
tools and approval policies are not registered or altered by this Skills client.
