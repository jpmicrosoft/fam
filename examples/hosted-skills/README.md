# Hosted instructions-only Skills adapters

FAM's `internal/skillruntime.Files("python")` returns filesystem and authenticated
MCP provider adapters, not an entry-point replacement. MCP mode uses an
explicit immutable, same-project Toolbox; it never substitutes a logical
default. See the separate [`dotnet` example](dotnet/README.md) for the .NET adapter.

New FAM-generated Python entry points include the provider-lifetime hook below.
Existing application entry points and tool approvals are not rewritten.
New FAM scaffold dependency pins are updated for SDK compatibility. Existing
apps must explicitly merge the dependency pins below into their own dependency
manifests. Native Prompt Skills are separate from these Hosted
providers.

For end-user commands, start with the
[Hosted bundle workflow](../../docs/hosted-agents.md#hosted-skills) or
[publish-and-pin MCP workflow](../../docs/tools-and-grounding.md#publish-and-pin-a-hosted-mcp-skill).
This page describes application integration and maintainer checks; users do
not call FAM's internal Go packages themselves.

## SDK contract

Microsoft documentation identifies these provider APIs:

| Runtime | Bundle without MCP | Explicit same-project Toolbox MCP |
| --- | --- | --- |
| Python | `SkillsProvider.from_paths`, registered via `Agent.context_providers` | `MCPSkillsSource(client=session)` |
| .NET | Filesystem `AgentSkillsProvider`, registered via `AIContextProviders` | `AgentSkillsProviderBuilder.UseMcpSkills(client)` |

References:

- [Agent Framework Skills](https://learn.microsoft.com/en-us/agent-framework/agents/skills)
- [Foundry Skills](https://learn.microsoft.com/en-us/azure/foundry/agents/how-to/tools/skills?pivots=rest-api)
- [Toolbox MCP endpoint and authentication](https://learn.microsoft.com/en-us/azure/foundry/agents/how-to/tools/toolbox)

The Python adapter and its real-package tests use these pinned packages:

```text
agent-framework-core==1.19.0
agent-framework-foundry==1.13.1
agent-framework-foundry-hosting==1.0.0b260918
mcp==1.30.0
```

Foundry 1.13.1 and hosting 1.0.0b260918 require core >=1.19.0,<2. Hosting
requires MCP >=1.24,<2; MCP 2.x must not be substituted. The adapter checks
the core version at startup and the MCP version before opening a session.
The offline tests below exercise the real SDK providers and MCP transport,
not just generated source strings. `MCPSkillsSource` is experimental;
dependency upgrades require renewed qualification.

Core 1.19.0 exposes resource and script tools even when their discovery filters
reject every file. The FAM adapter therefore uses the public `ContextProvider`
and `SessionContext` APIs to stage the real provider's output, verify exact
discovery/loading, and forward only `load_skill`. Its custom discovery prompt
does not advertise resource reading or script execution. Approval is disabled
only on this adapter's own read-only load tool; there is no global approval
rule and `allowed-tools` metadata grants no capabilities.

The filesystem source is uncached and restricted to the selected directories.
Readiness and every invocation recheck the manifest, strict frontmatter, tree,
byte digests, names, descriptions, and actual SDK-loaded content. SDK newline
normalization is checked separately from byte-exact SHA-256. A missing or
silently skipped required Skill is an error. Instruction bodies are loaded for
integrity checking, but only names/descriptions reach the model until it calls
`load_skill`.

The MCP source checks and filters the index before the SDK sees it, rejects
missing selected entries, and permits only selected `archive` or `skill-md`
instruction resources. Archive responses are verified before SDK extraction;
direct `SKILL.md` responses are checked against the locked instruction-byte
SHA-256 and frontmatter. A text-only response cannot establish the ZIP's
`archiveSHA256`: that remains synchronization provenance, while runtime checks
verify the selected instructions. Synchronization validates the complete pinned
package's instructions-only layout in both cases.
Resource identifiers are opaque `resources/read` parameters, never
HTTP fetch targets. The SDK extraction safety settings match FAM's existing 256 MiB
archive and 64 MiB file guards, with exactly one `SKILL.md` archive entry.
These are instructions-only policy/filesystem guards, not Azure Skills quotas.
The local manifest uses the existing 8 MiB contained-file guard. The optional
frontmatter `compatibility` field follows the SDK's 500-character limit.

Before MCP parsing, the HTTP transport enforces the existing **256 MiB
response-byte guard**, or **8 MiB for DELETE responses**. It rejects invalid
or oversized `Content-Length` values and counts streamed bytes, so a missing
or understated length does not bypass the bound. This is a transport-response
limit, not a Skill count or aggregate-inventory quota.

Every request sends `Accept-Encoding: identity`; responses with a non-identity
`Content-Encoding` are rejected rather than transparently decompressed before
validation. The accepted response stream remains bounded as the MCP SDK reads
it. Termination uses a streaming DELETE and drains accepted response bytes
without buffering the entire body.

These processing bounds are not an aggregate host-memory budget. Measure peak
memory with the intended inventory and invocation concurrency: retained content,
parsing, validation, and SDK representations grow with the selected instruction
bytes and can require multiple in-memory copies. A bounded HTTP response does
not make the complete selected inventory constant-memory. Follow the
[host resource guidance](../../docs/hosted-agents.md#host-resources-and-memory);
do not infer new Skill count, body-size, or aggregate product caps from tests.

MCP uses the documented endpoint
`{project_endpoint}/toolboxes/{name}/versions/{version}/mcp?api-version=v1`
and scope `https://ai.azure.com/.default`, with no default preview header.
The application-owned synchronous identity credential (`azure.identity`,
not `azure.identity.aio`) supplies a fresh/refreshable token on every HTTP
request. Async apps can use the adapter, but must provide a synchronous
credential for Skills rather than moving a loop-bound async credential to
another event loop. Requests must match that exact endpoint, and all
redirects are rejected, including same-origin redirects that the MCP SDK
otherwise follows itself. The adapter never logs tokens.

An AnyIO blocking portal retains one owning event loop for the HTTP client
and MCP session. SDK context managers enter and exit on the same task, and
provider callbacks marshal resource reads back to that loop. Initialization
and readiness are bounded at 60 seconds each, resource/identity requests at
30 seconds, and cleanup at 30 seconds. The session remains alive until the
host exits. For a server-assigned session ID, shutdown explicitly sends an
authenticated DELETE to the same pinned endpoint and requires HTTP 200 or 204.
Other statuses (including unsupported DELETE), redirects, identity failures,
and timeouts propagate rather than being swallowed by the MCP SDK's teardown.
The response is streamed and drained under the cleanup deadline and DELETE
byte guard; an oversized body fails cleanup even with an accepted status.
Local session/transport/client disposal is attempted in `finally` blocks even
when termination fails; a simultaneous host failure remains in the exception
chain. A failed or timed-out termination does not confirm remote cleanup, and
even an acknowledgement cannot prove every server resource was released.
Without a server-assigned session ID there is no targeted DELETE. The adapter
does not dispose the app-owned identity credential. Bundle mode does not start
this portal or authenticate.
The identity deadline bounds the adapter's wait; it cannot forcibly stop an
app-owned synchronous credential's underlying call. Configure that credential's
own network/process timeouts as appropriate.

## Artifact contract

Explicit Hosted Skill synchronization materializes a source-contained
`fam_skills\manifest.json` with this format:

```json
{
  "formatVersion": 1,
  "mode": "bundle",
  "projectEndpoint": "https://ACCOUNT.services.ai.azure.com/api/projects/PROJECT",
  "skills": [
    {
      "name": "greeting",
      "sha256": "<lowercase SHA-256 of the exact SKILL.md bytes>",
      "path": "greeting/SKILL.md"
    }
  ]
}
```

This is a schematic example, not a valid deployable manifest. JSON paths use
portable slashes and are relative to `fam_skills`. Local Skills omit `version`;
remote Skills require immutable versions. MCP mode requires `toolboxName`,
`toolboxVersion`, and immutable versions for every selected Skill. It does
not fall back to a Toolbox's logical default or bundle mode.

The Python adapter validates the format, trusted project binding, exact
selected inventory, instructions-only layout/frontmatter, and byte digests
before reporting readiness. Artifact provenance fields (`service`, `language`,
declaration/image hashes, `sourcePath`, and `archiveSHA256`) are recognized.
`runtimeFiles` maps helper-relative names to exact helper-byte SHA-256 hashes;
the Python adapter permits only `fam_skills_runtime.py` and checks its hash
before readiness and each invocation. MCP archives also check the locked
`archiveSHA256` when supplied, separately from the index's optional digest.
The immutable Toolbox endpoint, selected names, and locked content digests
bind versions; no invented index `version` field is required.
Unknown fields and duplicate JSON/YAML keys fail closed. Unpublished local
bundles and explicit empty inventories may have an empty project binding.
An empty MCP inventory may also omit Toolbox pins, matching offline detachment
in the declaration schema. No empty inventory or bundle authenticates or
performs MCP/Skills API requests.

No Skill metadata may grant tools or choose a network host. MCP authentication
must use fresh/refreshable identity tokens for the trusted same-project
endpoint, disable untrusted redirects, avoid token logging, and keep the
session alive for the agent-host lifetime with explicit cleanup. Provider
integration must not change approval policy for unrelated tools.

## Application and FAM integration

Call `skillruntime.Files("python")` or `skillruntime.Files("dotnet")` only when
Skills integration is requested, and propagate its error. No successful
runtime-readiness result should be inferred from helper generation alone.

`Files` keys use portable slashes and are source-root-relative under
`fam_skills`, not relative to that directory:
`fam_skills/fam_skills_runtime.py`. The Python helper location on Windows is
`fam_skills\fam_skills_runtime.py`. Its public function contract is:

```python
open_runtime(*, manifest_path, project_endpoint, credential)
```

This returns a synchronous context manager yielding exactly one ready context
provider. `manifest_path` is an absolute `pathlib.Path`; `project_endpoint`
and `credential` come from the app's trusted Foundry configuration. Validation
must complete before yielding. Exceptions must propagate, and resources must
be cleaned up on normal shutdown, failed readiness, failed agent construction,
and host failure.

Supply the returned map to `hostedskills.SyncOptions.RuntimeFiles` so helpers
participate in ownership, integrity checks, and the artifact's expected files.
Manifest `runtimeFiles` keys are relative to `fam_skills`, without its prefix.
Adding an untracked helper after synchronization conflicts with artifact validation.
Do not weaken instructions-only validation to accept arbitrary Python/C#
files: only the exact FAM-owned runtime helpers belong in the artifact root.
An explicit empty manifest inventory yields a no-op provider after validation;
bundle and MCP detachment require no network. Missing/malformed manifests and
arbitrary missing/`None` providers are never interpreted as detachment.

New FAM-generated `main.py` loads this exact helper file beside the manifest,
before creating the model client or agent. It registers the yielded provider
through `context_providers` and keeps the context open around the server's
entire `run()` call. No `fam_skills` directory means the original agent
options remain unchanged. An existing directory with missing artifacts,
nonregular files, links/reparse points, helper import failures, and a `None`
provider fail closed. The helper must perform the remaining inventory,
instruction, digest, and SDK-readiness checks. The scaffold hook alone does
not qualify those checks.

The loader is source-relative, independent of the working directory, and
does not require an `__init__.py`. It executes the helper's source directly,
without creating or trusting bytecode caches inside the locked artifact.
The scaffold does not run `asyncio.run()` or create a temporary event loop.
Bundle readiness uses a worker loop only for its uncached, filesystem-only
provider, which retains no asynchronous sessions or loop-bound resources.
The MCP helper retains its portal, owning loop and session until context exit;
it does not attach an asynchronous session to a temporary or closed loop.

Existing Python applications must explicitly adopt the same lifecycle in
their own entry point; synchronization must not replace `main.py`:

```python
import importlib.util
from pathlib import Path
import sys

source_root = Path(__file__).resolve().parent
manifest_path = source_root / "fam_skills" / "manifest.json"
helper_path = manifest_path.with_name("fam_skills_runtime.py")
spec = importlib.util.spec_from_file_location("_fam_skills_runtime", helper_path)
if spec is None or spec.loader is None or spec.name in sys.modules:
    raise RuntimeError("FAM Skills helper cannot be loaded or is already active")
helper = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = helper
try:
    # Avoid generating or trusting bytecode inside the locked artifact.
    exec(compile(helper_path.read_bytes(), str(helper_path), "exec"), helper.__dict__)
    with helper.open_runtime(
        manifest_path=manifest_path,
        project_endpoint=project_endpoint,
        credential=credential,
    ) as skills_provider:
        agent = Agent(
            client=client,
            context_providers=[*existing_context_providers, skills_provider],
            # Keep the application's existing instructions, tools and options.
        )
        server_factory(agent).run()
finally:
    sys.modules.pop(spec.name, None)
```

This is an integration fragment, not a standalone application.
`Agent`, `client`, `project_endpoint`, `credential`, `existing_context_providers`,
and `server_factory` come from the existing application and its pinned SDKs.
Keep its existing model options, instructions, and tools when constructing the
agent; use an empty list only if it has no other context providers.
`server_factory` is the application's existing host constructor. Resolve the
manifest relative to the application's source/published directory, not its
current working directory. Existing applications must load/import the helper
explicitly; copying it beside an arbitrary entry point does not integrate it.
Ordinary imports must not create a `__pycache__` inside the managed artifact;
the source-loading pattern above avoids that. App source and runtime helpers
are trusted application code, not executable code supplied by a Skill.

`Files("dotnet")` emits the .NET adapter as `fam_skills\FamSkillsRuntime.cs`.
See the separate `dotnet` example for package references, registration,
qualification commands, and publish rules. Helper generation alone does not
establish that a particular application has registered its provider.
The existing app must register the qualified provider through its actual agent's
`AIContextProviders`, retain any MCP session until host shutdown, and include
the exact manifest/bundle bytes in build and publish output.

For code deployment, include the helper and locked artifacts in the actual
source archive. For containers, also check the Docker context and
`.dockerignore`. Prebuilt images require explicit adapter integration at image
build time; bundle changes require rebuilding the image. Deployment metadata
cannot inject adapters or files into an opaque image.

## Qualification checks

Use Python 3.13 and the pinned real packages for adapter checks. Also exercise
the built FAM CLI's workspace generation and local Skill attach/sync through
the generated `_skills_options` hook, real `Agent`/`SkillsProvider` discovery
and loading, and cleanup. For local bundle checks, use a credential that rejects
authentication so an unintended Azure dependency cannot silently pass.

The separate Linux
[`hosted-skills-runtime` CI gate](../../docs/development-and-releases.md#hosted-skills-runtime-ci-gate)
installs these pinned requirements and runs the real-package suite. It also
tests .NET built and published output in strict symlink mode and compares exact
artifact hashes. It invokes the Python script directly; the script rejects
zero executed tests or any skipped tests. Unittest discovery does not replace
that entry-point check. Rerun the affected checks after runtime fixes, dependency
updates, or packaging changes. Keep pass counts, skips, and exact run/image
identifiers with the tested source revision in PR or release evidence; earlier
results do not qualify changed adapters.

The Go tests cover source-relative helper generation and unsupported targets.
`TestScaffoldPythonSkillsLifecycle` executes generated Python with fake
framework/host dependencies and a fake context manager to exercise actual
registration, host-lifetime cleanup, failure propagation, unchanged tool
approvals, and no-Skills behavior for both host protocols. It skips if Python
is unavailable. Those are lifecycle tests, not real-SDK provider qualification.

`python_runtime_test.py` uses the real pinned core/MCP packages, an actual
`Agent`, a fake model, fake read-resource sessions, and an HTTPX fake transport
driving real MCP initialization/resource reads/termination. It covers discovery and
instruction loading, raw CRLF digests, unchanged unrelated approval, explicit
detachment, mutation after startup, malformed files/frontmatter, and missing,
unsupported, unsafe or digest-mismatched MCP entries. Missing dependencies fail
the script rather than skip qualification. It checks helper integrity,
inventories above 32 Skills and 8 MiB aggregate, and archives above the SDK's
default 1 MiB limit.

Run developer checks from the repository root using an isolated Python 3.13
environment. The following create a scratch environment outside the checkout;
ensure the selected base interpreter is Python 3.13. These are verification
instructions, not a claim that checks run automatically as part of the Go gate.

**PowerShell:**

```powershell
$venv = Join-Path $env:TEMP ('fam-skills-python-' + [guid]::NewGuid().ToString('N'))
python -m venv $venv
$python = Join-Path $venv 'Scripts\python.exe'
& $python -m pip install -r ./internal/skillruntime/testdata/requirements.txt
& $python -B ./internal/skillruntime/testdata/python_runtime_test.py -v
```

**POSIX shell (Python 3.13 installed as `python3.13`):**

```bash
venv_root="$(mktemp -d)"
python3.13 -m venv "$venv_root/venv"
"$venv_root/venv/bin/python" -m pip install -r ./internal/skillruntime/testdata/requirements.txt
"$venv_root/venv/bin/python" -B ./internal/skillruntime/testdata/python_runtime_test.py -v
```

The separate Go checks use the source-build toolchain and a Python interpreter
on `PATH` for scaffold execution:

```text
go test -count=1 ./internal/skillruntime
go test -count=1 ./internal/hosted -run TestScaffold
go vet ./internal/skillruntime
```

Do not restore into a global Python installation. The checked-in requirements
file is also embedded as
`skillruntime.PythonRequirements`, so scaffold and test pins share one source.
The MCP source adapter rejects a mismatched installed MCP version, including
2.x, before constructing the provider.

MCP transport cases check the immutable Toolbox URL, same-project
authentication, token refresh, redirect rejection, bounded reads, cross-loop
host lifetime, and cleanup on normal shutdown and failed readiness. Shutdown
regressions also cover failed DELETE acknowledgements, credential renewal
errors, redirects, deadlines, and preservation of a simultaneous host error. The
fixtures are synthetic protocol fixtures, not captures of a live Foundry
service. These automated checks establish offline SDK/scaffold compatibility,
not live Azure acceptance or readiness of a particular deployed application
or image. The separately authorized live checks below are not part of the
offline test suite.

### Live qualification boundary

On 2026-10-01 (UTC), disposable `eastus2` resources with `gpt-5-mini` verified
FAM-generated Python applications deployed through Foundry's source-code REST
API, using Python 3.13 and the dependency pins above:

| Path | Observed result |
| --- | --- |
| Python Hosted bundle, without MCP | A deployed application returned the unique marker present only in its bundled Skill. |
| Python Hosted immutable Toolbox MCP | A deployed application discovered and loaded the selected Skill and returned its unique marker. |
| MCP version pinning | After publishing Skill v2 and moving its default to v2, a fresh Hosted session still loaded pinned v1 through immutable Toolbox v1. |
| Python local client with real Foundry MCP | The actual provider advertised and loaded the locked Skill, with renewable authentication and acknowledged cleanup; no model service was used. |
| .NET local client with real Foundry MCP | The actual `UseMcpSkills` provider loaded pinned v1 through `skill-md` while the Skill default was v2; a fake model checked the real tool result. |

Hosted MCP initially failed during resource reading under the platform's default
runtime permissions. It became ready and passed after assigning `Foundry User`
to the agent identity at the disposable project scope and allowing propagation.
The human identity's successful synchronization is not proof of runtime access.
For production, assess the required read permissions and use a narrower custom
role where appropriate; FAM does not silently grant runtime roles.

Those first deployments used direct REST. A separate authorized run on the
same UTC date, after installing Docker/WSL and azd, verified the previously
untested paths with another disposable project:

| Additional path | Observed result |
| --- | --- |
| Python and .NET local bundle containers | Non-root Linux `amd64` images advertised and loaded the actual synchronized Skill with networking disabled. The body-only marker was absent from initial model context and present in the real load result. |
| Python azd code deployment | Actual `fam hosted environment create`, preflight, and deployment succeeded; an exact-version model invocation returned the bundled Skill marker. |
| .NET azd code deployment | A real .NET 10 Responses host built remotely, deployed, and returned its bundled Skill marker through an actual model invocation. |
| Python azd container deployment | azd built the Docker context, pushed to a test-owned ACR, and deployed it; the exact-version runtime returned the bundled marker. |
| .NET azd prebuilt image with MCP | Exact embedded artifact hashes were checked before push. FAM bound integration evidence to the immutable registry digest, azd deployed that image without rebuilding, and the runtime loaded Skill v1 through Toolbox v1 and returned its marker. |

The azd run used CLI `1.35.0` and the FAM-pinned `azure.ai.agents`
`1.0.0-beta.13`. Live testing exposed the extension's scalar filename
`entryPoint` contract, distinct from REST command arrays; FAM now projects and
restores that configuration correctly. See [code entry points](../../docs/hosted-agents.md#code-entry-points).
The .NET host additionally used `Microsoft.Agents.AI.Foundry.Hosting`
`1.23.0-preview.260928.1`, `Azure.AI.Projects` `3.0.0-beta.3`,
`Azure.Identity` `1.21.0`, and Responses protocol `2.0.0`.

The ACR used disabled admin authentication, a project managed-identity
`ContainerRegistry` connection, and registry-scoped `AcrPull`. The .NET MCP
agent had project-scoped `Foundry User`. No login cache or tokens were built
into images. All invocation sessions were deleted and their absence checked;
all test Azure resources and identities were subsequently deleted. The examples
contain no reusable test environment or credentials.

This is representative packaging coverage, not every runtime/delivery/packaging
combination or a guarantee for another application. See the [.NET example](dotnet/README.md)
for its adapter contract and [Prompt Skills](../../docs/prompt-agents.md#native-skills-preview)
for the unchanged native runtime limitation. These observations do not certify
later adapter or packaging revisions. Requalify the actual application,
identity, SDK pins, and packaging mode after relevant changes and before
relying on them elsewhere.
