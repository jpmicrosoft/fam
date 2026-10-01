"""Real-package, offline runtime tests. Run directly with Python 3.13."""

import asyncio
import base64
from contextlib import contextmanager
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import stat
import sys
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import zipfile

from agent_framework import Agent, ChatResponse, FunctionTool, SessionContext, SkillsProvider
import httpx
from mcp.shared.exceptions import McpError
from mcp.types import BlobResourceContents, ErrorData, ReadResourceResult, TextResourceContents
from pydantic import AnyUrl


TEMPLATE = Path(__file__).resolve().parents[1] / "templates" / "fam_skills_runtime.py"
SPEC = importlib.util.spec_from_file_location("fam_skills_runtime_test", TEMPLATE)
runtime = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = runtime
exec(compile(TEMPLATE.read_bytes(), str(TEMPLATE), "exec"), runtime.__dict__)

ENDPOINT = "https://account.services.ai.azure.com/api/projects/project"
CONTENT = (
    b"---\nname: greeting\ndescription: A selected greeting instruction\n"
    b"allowed-tools: Shell(*)\nmetadata:\n  owner: team\n---\n"
    b"Respond with the unique marker FAM_SKILL_BODY_4b753.\n"
)


@contextmanager
def offline_http():
    with (
        patch.object(
            httpx.HTTPTransport, "handle_request",
            side_effect=AssertionError("unexpected real HTTPX request"),
        ),
        patch.object(
            httpx.AsyncHTTPTransport, "handle_async_request",
            side_effect=AssertionError("unexpected real HTTPX request"),
        ),
    ):
        yield


class NoCredential:
    def get_token(self, *args, **kwargs):
        raise AssertionError("bundle mode must not authenticate")


class FakeModel:
    additional_properties = {}

    def __init__(self):
        self.advertised = None
        self.loaded = None
        self.tool_names = None

    def get_response(self, messages, *, stream=False, options=None, **kwargs):
        if stream:
            raise AssertionError("these offline tests request non-streaming responses")

        async def respond():
            self.advertised = str(messages) + str(options)
            tools = options["tools"]
            self.tool_names = [tool.name for tool in tools]
            load = next(tool for tool in tools if tool.name == "load_skill")
            self.loaded = await load.invoke(arguments={"skill_name": "greeting"}, skip_parsing=True)
            return ChatResponse(messages=[], response_id="offline-skills-test")

        return respond()


class OfflineHTTPGuardTests(unittest.TestCase):
    def test_real_http_is_rejected_without_interfering_with_event_loop_creation(self):
        async def exercise():
            async with httpx.AsyncClient(trust_env=False) as client:
                with self.assertRaisesRegex(AssertionError, "unexpected real HTTPX request"):
                    await client.get("https://must-not-contact.invalid/")

        with offline_http():
            with httpx.Client(trust_env=False) as client:
                with self.assertRaisesRegex(AssertionError, "unexpected real HTTPX request"):
                    client.get("https://must-not-contact.invalid/")
            asyncio.run(exercise())


class PythonBundleTests(unittest.TestCase):
    def setUp(self):
        self.enterContext(offline_http())
        self.enterContext(patch.object(
            runtime, "_open_mcp_runtime",
            side_effect=AssertionError("bundle mode must not open an MCP session"),
        ))
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name) / "fam_skills"
        self.root.mkdir()
        self.manifest_path = self.root / "manifest.json"
        self.skill_path = self.root / "greeting" / "SKILL.md"
        self.skill_path.parent.mkdir()
        self.skill_path.write_bytes(CONTENT)
        self.manifest = {
            "formatVersion": 1,
            "mode": "bundle",
            "projectEndpoint": ENDPOINT,
            "skills": [{
                "name": "greeting",
                "sha256": hashlib.sha256(CONTENT).hexdigest(),
                "path": "greeting/SKILL.md",
            }],
        }
        self.write_manifest()
        self.model = FakeModel()
        self.agent = Agent(client=self.model, instructions="Base instructions only.")

    def write_manifest(self):
        self.manifest_path.write_text(json.dumps(self.manifest), encoding="utf-8")

    def replace_content(self, content):
        self.skill_path.write_bytes(content)
        self.manifest["skills"][0]["sha256"] = hashlib.sha256(content).hexdigest()
        self.write_manifest()

    def open(self):
        return runtime.open_runtime(
            manifest_path=self.manifest_path,
            project_endpoint=ENDPOINT,
            credential=NoCredential(),
        )

    def context(self, provider, *, tools=None):
        context = SessionContext(input_messages=[], tools=tools)
        asyncio.run(provider.before_run(
            agent=self.agent, session=self.agent.create_session(), context=context, state={},
        ))
        return context

    def assert_rejected(self, pattern=None):
        with self.assertRaisesRegex((runtime.SkillsRuntimeError, OSError), pattern or "."):
            with self.open():
                self.fail("invalid Skills reached readiness")

    def test_agent_discovers_and_loads_real_sdk_provider_without_network(self):
        with self.open() as provider:
            agent = Agent(client=self.model, context_providers=[provider])
            asyncio.run(agent.run("Use the greeting Skill."))
        self.assertEqual(self.model.tool_names, ["load_skill"])
        self.assertIn("greeting", self.model.advertised)
        self.assertNotIn("FAM_SKILL_BODY_4b753", self.model.advertised)
        self.assertIn("FAM_SKILL_BODY_4b753", self.model.loaded)
        self.assertEqual(self.model.loaded, CONTENT.decode("utf-8") + runtime._FILE_SUFFIX)

    def test_only_load_skill_is_exposed_and_unrelated_approval_is_unchanged(self):
        async def ordinary():
            return "ordinary"

        unrelated = FunctionTool(
            name="ordinary", description="Unrelated tool", func=ordinary,
            approval_mode="always_require",
        )
        with self.open() as provider:
            context = self.context(provider, tools=[unrelated])
        self.assertEqual([tool.name for tool in context.tools], ["ordinary", "load_skill"])
        self.assertIs(context.tools[0], unrelated)
        self.assertEqual(unrelated.approval_mode, "always_require")
        self.assertEqual(context.tools[1].approval_mode, "never_require")
        self.assertNotIn("run_skill_script", "\n".join(context.instructions))
        self.assertNotIn("read_skill_resource", "\n".join(context.instructions))

    def test_existing_load_skill_tool_is_not_shadowed_or_automatically_approved(self):
        async def ordinary(skill_name: str):
            raise AssertionError("unrelated application tool must not execute")

        unrelated = FunctionTool(
            name="load_skill", description="Application tool", func=ordinary,
            approval_mode="always_require",
        )
        with self.open() as provider:
            with self.assertRaisesRegex(runtime.SkillsRuntimeError, "conflicts"):
                self.context(provider, tools=[unrelated])
        self.assertEqual(unrelated.approval_mode, "always_require")

    def test_artifact_is_rechecked_when_loading_after_model_context_was_prepared(self):
        with self.open() as provider:
            context = self.context(provider)
            self.skill_path.write_bytes(CONTENT + b"modified")
            with self.assertRaisesRegex(runtime.SkillsRuntimeError, "digest"):
                asyncio.run(context.tools[0].invoke(arguments={"skill_name": "greeting"}))

    def test_unselected_skill_request_fails(self):
        with self.open() as provider:
            context = self.context(provider)
            with self.assertRaisesRegex(runtime.SkillsRuntimeError, "locked inventory"):
                asyncio.run(context.tools[0].invoke(arguments={"skill_name": "other"}))

    def test_provider_and_tools_refuse_use_after_cleanup(self):
        with self.open() as provider:
            context = self.context(provider)
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "closed"):
            self.context(provider)
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "closed"):
            asyncio.run(context.tools[0].invoke(arguments={"skill_name": "greeting"}))

    def test_runtime_can_be_opened_from_an_existing_event_loop(self):
        async def use_runtime():
            with self.open() as provider:
                context = SessionContext(input_messages=[])
                await provider.before_run(
                    agent=self.agent, session=self.agent.create_session(), context=context, state={},
                )
                return await context.tools[0].invoke(arguments={"skill_name": "greeting"}, skip_parsing=True)

        self.assertIn("FAM_SKILL_BODY_4b753", asyncio.run(use_runtime()))

    def test_crlf_is_hashed_exactly_but_loaded_using_sdk_newline_semantics(self):
        self.replace_content(CONTENT.replace(b"\n", b"\r\n"))
        with self.open() as provider:
            context = self.context(provider)
            loaded = asyncio.run(context.tools[0].invoke(
                arguments={"skill_name": "greeting"}, skip_parsing=True,
            ))
        self.assertEqual(loaded, CONTENT.decode() + runtime._FILE_SUFFIX)
        self.manifest["skills"][0]["sha256"] = hashlib.sha256(CONTENT).hexdigest()
        self.write_manifest()
        self.assert_rejected("digest")

    def test_empty_explicit_inventory_is_noop_detachment(self):
        self.skill_path.unlink()
        self.skill_path.parent.rmdir()
        self.manifest["skills"] = []
        self.write_manifest()
        with self.open() as provider:
            context = self.context(provider)
        self.assertEqual(context.instructions, [])
        self.assertEqual(context.tools, [])

    def test_unpublished_local_bundle_may_have_no_project_binding(self):
        self.manifest["projectEndpoint"] = ""
        self.write_manifest()
        with self.open() as provider:
            self.assertEqual(len(self.context(provider).tools), 1)

    def test_parent_provenance_fields_are_accepted(self):
        self.manifest.update(
            service="agent", language="python", declarationHash="a" * 64,
            imageReference="registry/image@sha256:" + "b" * 64,
            imageEvidenceSHA256="c" * 64,
        )
        self.manifest["skills"][0].update(version="17", archiveSHA256="d" * 64)
        self.write_manifest()
        with self.open() as provider:
            self.assertEqual(len(self.context(provider).tools), 1)

    def test_parent_runtime_helper_hash_is_checked_before_and_after_readiness(self):
        helper = self.root / "fam_skills_runtime.py"
        helper.write_bytes(TEMPLATE.read_bytes())
        self.manifest["runtimeFiles"] = {helper.name: hashlib.sha256(helper.read_bytes()).hexdigest()}
        self.write_manifest()
        with self.open() as provider:
            self.assertEqual(len(self.context(provider).tools), 1)
            helper.write_bytes(helper.read_bytes() + b"\n")
            with self.assertRaisesRegex(runtime.SkillsRuntimeError, "helper digest"):
                self.context(provider)
        self.assert_rejected("helper digest")

    def test_runtime_helper_inventory_cannot_authorize_arbitrary_scripts(self):
        for value in (None, [], {"../script.py": "a" * 64}, {"FamSkillsRuntime.cs": "a" * 64},
                      {"fam_skills_runtime.py": "invalid"}):
            with self.subTest(value=value):
                self.manifest["runtimeFiles"] = value
                self.write_manifest()
                self.assert_rejected()

    def test_missing_locked_runtime_helper_fails(self):
        self.manifest["runtimeFiles"] = {"fam_skills_runtime.py": "a" * 64}
        self.write_manifest()
        self.assert_rejected()

    def test_windows_reparse_flag_is_rejected_without_requiring_symlink_privilege(self):
        attributes = SimpleNamespace(st_mode=stat.S_IFDIR, st_file_attributes=0x400)
        with patch.object(Path, "lstat", return_value=attributes):
            self.assert_rejected("links")

    def test_more_than_32_skills_and_8_mib_aggregate_are_not_feature_limits(self):
        self.skill_path.unlink()
        self.skill_path.parent.rmdir()
        self.manifest["skills"] = []
        aggregate = 0
        for number in range(33):
            name = f"skill-{number:03d}"
            content = (
                f"---\nname: {name}\ndescription: Selected large inventory\n---\n".encode()
                + b"x" * 270000 + b"\n"
            )
            aggregate += len(content)
            directory = self.root / name
            directory.mkdir()
            (directory / "SKILL.md").write_bytes(content)
            self.manifest["skills"].append({
                "name": name, "path": name + "/SKILL.md", "sha256": hashlib.sha256(content).hexdigest(),
            })
        self.assertGreater(len(self.manifest["skills"]), 32)
        self.assertGreater(aggregate, 8 << 20)
        self.write_manifest()
        with self.open() as provider:
            context = self.context(provider)
            self.assertEqual([tool.name for tool in context.tools], ["load_skill"])
            self.assertIn("skill-032", "\n".join(context.instructions))

    def test_resource_and_script_files_fail_before_discovery(self):
        for name in ("notes.txt", "script.py", "resources"):
            with self.subTest(name=name):
                extra = self.skill_path.parent / name
                extra.write_text("never execute this", encoding="utf-8")
                self.assert_rejected("scripts/resources")
                extra.unlink()

    def test_unselected_skill_directory_fails(self):
        (self.root / "other").mkdir()
        self.assert_rejected("unexpected")

    def test_missing_bundle_file_fails(self):
        self.skill_path.unlink()
        self.assert_rejected()

    def test_mismatched_digest_fails(self):
        self.skill_path.write_bytes(CONTENT + b"modified\n")
        self.assert_rejected("digest")

    def test_artifact_changes_after_startup_fail_before_the_next_model_run(self):
        with self.open() as provider:
            self.replace_content(CONTENT + b"changed\n")
            with self.assertRaisesRegex(runtime.SkillsRuntimeError, "manifest changed"):
                self.context(provider)

    def test_sdk_silent_empty_discovery_is_not_readiness(self):
        async def missing(*args, **kwargs):
            return None

        with patch.object(SkillsProvider, "before_run", missing):
            self.assert_rejected("inventory")

    def test_unqualified_core_version_fails_before_provider_creation(self):
        with patch.object(runtime, "version", return_value="1.13.0"):
            self.assert_rejected("1.19.0")

    def test_invalid_manifest_shapes_and_pins_fail(self):
        mutations = [
            lambda m: m.update(formatVersion=True),
            lambda m: m.update(formatVersion=2),
            lambda m: m.update(skills=None),
            lambda m: m.update(extra=True),
            lambda m: m.update(projectEndpoint=ENDPOINT.replace("/project", "/different")),
            lambda m: m["skills"].append(dict(m["skills"][0])),
            lambda m: m["skills"][0].update(path="../SKILL.md"),
            lambda m: m["skills"][0].update(path="greeting\\SKILL.md"),
            lambda m: m["skills"][0].update(version="latest"),
            lambda m: m["skills"][0].update(version=None),
            lambda m: m["skills"][0].update(sha256="z" * 64),
            lambda m: m["skills"][0].update(extra="unsupported"),
        ]
        original = json.dumps(self.manifest)
        for mutate in mutations:
            with self.subTest(mutation=mutate):
                self.manifest = json.loads(original)
                mutate(self.manifest)
                self.write_manifest()
                self.assert_rejected()

    def test_duplicate_json_and_malformed_json_fail(self):
        self.manifest_path.write_text('{"formatVersion":1,"formatVersion":1}', encoding="utf-8")
        self.assert_rejected("duplicate")
        self.manifest_path.write_text("{", encoding="utf-8")
        self.assert_rejected("malformed")

    def test_malformed_frontmatter_fails_even_with_a_matching_digest(self):
        contents = [
            b"no frontmatter\n",
            b"---\nname: greeting\n---\nbody\n",
            b"---\nname: other\ndescription: example\n---\nbody\n",
            b"---\nname: greeting\ndescription: one\ndescription: two\n---\nbody\n",
            b"---\nname: greeting\ndescription: example\nunknown: value\n---\nbody\n",
            b"---\nname: greeting\ndescription: example\n---\n \n",
            b"---\nname: greeting\ndescription: &text example\nlicense: *text\n---\nbody\n",
            b"---\nname: greeting\ndescription: example\nmetadata:\n  x: a\n  x: b\n---\nbody\n",
            b"---\nname: greeting\ndescription: example\nmetadata:\n  x: [a]\n---\nbody\n",
            b"---\nname: greeting\ndescription: example\n---\n\xff\n",
            b"---\nname: greeting\ndescription: example\n---\n\x00\n",
        ]
        for content in contents:
            with self.subTest(content=hashlib.sha256(content).hexdigest()):
                self.replace_content(content)
                self.assert_rejected()

    def test_description_documented_boundary(self):
        self.replace_content(b"---\nname: greeting\ndescription: " + b"a" * 1024 + b"\n---\nbody\n")
        with self.open() as provider:
            self.assertEqual(len(self.context(provider).tools), 1)
        self.replace_content(b"---\nname: greeting\ndescription: " + b"a" * 1025 + b"\n---\nbody\n")
        self.assert_rejected("1024")

    def test_documented_name_boundary(self):
        name = "a" * 64
        directory = self.root / name
        self.skill_path.parent.rename(directory)
        self.skill_path = directory / "SKILL.md"
        self.manifest["skills"][0].update(name=name, path=name + "/SKILL.md")
        self.replace_content(CONTENT.replace(b"name: greeting", b"name: " + name.encode()))
        with self.open() as provider:
            self.assertIn(name, "\n".join(self.context(provider).instructions))
        self.manifest["skills"][0]["name"] += "a"
        self.write_manifest()
        self.assert_rejected("name is invalid")


class FakeMCPSession:
    def __init__(self):
        self.resources = {}
        self.requests = []

    async def read_resource(self, uri):
        key = str(uri)
        self.requests.append(key)
        if key not in self.resources:
            raise McpError(ErrorData(code=-32002, message="Fixture resource missing"))
        return self.resources[key].model_copy(deep=True)


class PythonMCPSourceTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name) / "fam_skills"
        self.root.mkdir()
        self.path = self.root / "manifest.json"
        self.manifest = {
            "formatVersion": 1, "mode": "mcp", "projectEndpoint": ENDPOINT,
            "toolboxName": "operations", "toolboxVersion": "12",
            "skills": [{"name": "greeting", "version": "7", "sha256": hashlib.sha256(CONTENT).hexdigest()}],
        }
        self.path.write_text(json.dumps(self.manifest), encoding="utf-8")
        self.session = FakeMCPSession()
        self.uri = "skill://greeting/SKILL.md"
        self.index = {"skills": [{
            "name": "greeting", "type": "skill-md",
            "description": "A selected greeting instruction", "url": self.uri,
        }]}
        self.put_text(self.uri, CONTENT.decode())
        self.put_index()
        self.agent = Agent(client=FakeModel())

    def put_text(self, uri, text):
        self.session.resources[uri] = ReadResourceResult(
            contents=[TextResourceContents(uri=AnyUrl(uri), text=text)],
        )

    def put_index(self):
        self.put_text("skill://index.json", json.dumps(self.index))

    def put_archive(self, members=None):
        output = io.BytesIO()
        with zipfile.ZipFile(output, "w") as archive:
            for name, content in members or [("SKILL.md", CONTENT)]:
                archive.writestr(name, content)
        self.uri = "skill://greeting/archive.zip"
        self.index["skills"][0].update(
            type="archive", url=self.uri,
            digest="sha256:" + hashlib.sha256(output.getvalue()).hexdigest(),
        )
        self.put_index()
        self.session.resources[self.uri] = ReadResourceResult(contents=[BlobResourceContents(
            uri=AnyUrl(self.uri), mimeType="application/zip",
            blob=base64.b64encode(output.getvalue()).decode("ascii"),
        )])
        self.manifest["skills"][0]["archiveSHA256"] = hashlib.sha256(output.getvalue()).hexdigest()
        self.path.write_text(json.dumps(self.manifest), encoding="utf-8")

    async def provider(self):
        provider = await runtime._provider_from_mcp_session(
            manifest_path=self.path, project_endpoint=ENDPOINT, session=self.session,
        )
        self.addCleanup(provider.close)
        return provider

    async def context(self, provider):
        context = SessionContext(input_messages=[])
        await provider.before_run(
            agent=self.agent, session=self.agent.create_session(), context=context, state={},
        )
        return context

    async def test_real_mcp_source_advertises_and_loads_only_selected_instructions(self):
        self.index["skills"].append({
            "name": "unselected", "type": "skill-md", "description": "Not selected",
            "url": "skill://unselected/SKILL.md",
        })
        self.put_index()
        provider = await self.provider()
        context = await self.context(provider)
        self.assertEqual([tool.name for tool in context.tools], ["load_skill"])
        self.assertNotIn("unselected", "\n".join(context.instructions))
        loaded = await context.tools[0].invoke(arguments={"skill_name": "greeting"}, skip_parsing=True)
        self.assertEqual(loaded, CONTENT.decode())
        self.assertNotIn("skill://unselected/SKILL.md", self.session.requests)
        self.assertEqual(set(self.session.requests), {"skill://index.json", self.uri})

    async def test_real_mcp_archive_provider_loads_instructions(self):
        self.put_archive()
        context = await self.context(await self.provider())
        loaded = await context.tools[0].invoke(arguments={"skill_name": "greeting"}, skip_parsing=True)
        self.assertEqual(loaded, CONTENT.decode() + runtime._FILE_SUFFIX)
        self.assertEqual([tool.name for tool in context.tools], ["load_skill"])

    async def test_missing_index_does_not_become_empty_success(self):
        del self.session.resources["skill://index.json"]
        with self.assertRaises(McpError):
            await self.provider()

    async def test_mcp_2_is_rejected_before_contacting_the_session(self):
        def installed_version(package):
            return "2.2.0" if package == "mcp" else runtime.CORE_VERSION

        with patch.object(runtime, "version", side_effect=installed_version):
            with self.assertRaisesRegex(runtime.SkillsRuntimeError, "mcp==1.30.0"):
                await self.provider()
        self.assertEqual(self.session.requests, [])

    async def test_empty_or_malformed_index_fails(self):
        for text in ("", "{", "null", '{"skills":[]}', '{"skills":null}'):
            with self.subTest(text=text):
                self.put_text("skill://index.json", text)
                with self.assertRaises(runtime.SkillsRuntimeError):
                    await self.provider()

    async def test_unsupported_selected_entry_fails_before_sdk_can_skip_it(self):
        self.index["skills"][0]["type"] = "mcp-resource-template"
        self.put_index()
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "unsupported"):
            await self.provider()
        self.assertEqual(self.session.requests, ["skill://index.json"])

    async def test_digest_mismatch_fails(self):
        self.put_text(self.uri, CONTENT.decode() + "modified")
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "digest"):
            await self.provider()

    async def test_missing_locked_skill_version_fails_without_an_invented_index_version_field(self):
        del self.manifest["skills"][0]["version"]
        self.path.write_text(json.dumps(self.manifest), encoding="utf-8")
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "versions"):
            await self.provider()
        self.assertEqual(self.session.requests, [])

    async def test_description_mismatch_fails(self):
        self.index["skills"][0]["description"] = "Different instructions"
        self.put_index()
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "description"):
            await self.provider()

    async def test_archive_scripts_resources_wrappers_and_traversal_fail(self):
        link = zipfile.ZipInfo("SKILL.md")
        link.create_system = 3
        link.external_attr = (stat.S_IFLNK | 0o777) << 16
        archives = [
            [("SKILL.md", CONTENT), ("script.py", b"must not execute")],
            [("SKILL.md", CONTENT), ("notes.txt", b"must not read")],
            [("greeting/SKILL.md", CONTENT)],
            [("../SKILL.md", CONTENT)],
            [("/SKILL.md", CONTENT)],
            [("C:/SKILL.md", CONTENT)],
            [("SKILL.md", CONTENT), ("SKILL.md", CONTENT)],
            [("SKILL.md", CONTENT), ("skill.md", CONTENT)],
            [(link, CONTENT)],
        ]
        for members in archives:
            with self.subTest(names=[name for name, _ in members]):
                self.put_archive(members)
                with self.assertRaises(runtime.SkillsRuntimeError):
                    await self.provider()

    async def test_missing_archive_does_not_become_empty_success(self):
        self.put_archive()
        del self.session.resources[self.uri]
        with self.assertRaises(McpError):
            await self.provider()

    async def test_archive_digest_mismatch_is_not_a_skipped_skill(self):
        self.put_archive()
        self.index["skills"][0]["digest"] = "sha256:" + "0" * 64
        self.put_index()
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "digest"):
            await self.provider()

    async def test_https_resource_identifier_is_read_through_mcp_not_fetched_as_http(self):
        uri = "https://outside.invalid/SKILL.md"
        self.index["skills"][0]["url"] = uri
        self.put_text(uri, CONTENT.decode())
        self.put_index()
        with offline_http():
            context = await self.context(await self.provider())
            loaded = await context.tools[0].invoke(arguments={"skill_name": "greeting"}, skip_parsing=True)
        self.assertEqual(loaded, CONTENT.decode())
        self.assertEqual(set(self.session.requests), {"skill://index.json", uri})

    async def test_archive_lock_is_checked_independently_of_index_digest(self):
        self.put_archive()
        self.manifest["skills"][0]["archiveSHA256"] = "0" * 64
        self.path.write_text(json.dumps(self.manifest), encoding="utf-8")
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "archive digest differs from its lock"):
            await self.provider()

    async def test_zip_checksum_corruption_is_rejected_before_sdk_extraction(self):
        self.put_archive()
        resource = self.session.resources[self.uri].contents[0]
        archive = bytearray(base64.b64decode(resource.blob))
        archive[30 + len("SKILL.md") + 5] ^= 1
        digest = hashlib.sha256(archive).hexdigest()
        resource.blob = base64.b64encode(archive).decode("ascii")
        self.index["skills"][0]["digest"] = "sha256:" + digest
        self.manifest["skills"][0]["archiveSHA256"] = digest
        self.put_index()
        self.path.write_text(json.dumps(self.manifest), encoding="utf-8")
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "archive is malformed"):
            await self.provider()

    async def test_archive_larger_than_sdk_default_one_mib_is_supported(self):
        content = CONTENT + b"x" * (1 << 20)
        self.assertGreater(len(content), 1 << 20)
        self.manifest["skills"][0]["sha256"] = hashlib.sha256(content).hexdigest()
        self.put_archive([("SKILL.md", content)])
        context = await self.context(await self.provider())
        loaded = await context.tools[0].invoke(arguments={"skill_name": "greeting"}, skip_parsing=True)
        self.assertEqual(loaded, content.decode() + runtime._FILE_SUFFIX)

    async def test_resource_reads_outside_allowlist_fail_without_contacting_session(self):
        resources = runtime._MCPResources(self.session, self.manifest)
        await resources.prime()
        before = list(self.session.requests)
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "instructions-only inventory"):
            await resources.read_resource(AnyUrl("skill://greeting/7/script.py"))
        self.assertEqual(self.session.requests, before)

    async def test_index_change_after_readiness_fails_closed(self):
        provider = await self.provider()
        self.index["skills"][0]["url"] = "skill://greeting/8/SKILL.md"
        self.put_index()
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "changed"):
            await self.context(provider)


class IdentityFixture:
    def __init__(self):
        self.scopes = []
        self.expired = False
        self.failure = None
        self.lock = threading.Lock()

    def get_token(self, scope):
        with self.lock:
            self.scopes.append(scope)
            if self.failure is not None:
                raise self.failure
            return SimpleNamespace(
                token=f"fixture-token-{len(self.scopes)}",
                expires_on=0 if self.expired else 4102444800,
            )


class MCPHTTPFixture:
    """Synthetic protocol fixture, not a captured Foundry service response."""

    def __init__(self, resources):
        self.resources = resources
        self.requests = []
        self.loops = []
        self.reads = []
        self.terminated = False
        self.delete_status = 204
        self.delete_redirect = None
        self.hang_on_delete = False
        self.delete_stream = None
        self.session_id = "offline-session"
        self.redirect = None
        self.hang_on_read = False

    async def __call__(self, request):
        self.requests.append((request.method, str(request.url), dict(request.headers)))
        self.loops.append(asyncio.get_running_loop())
        if self.redirect:
            return httpx.Response(307, headers={"Location": self.redirect})
        if request.method == "GET":
            return httpx.Response(405)
        if request.method == "DELETE":
            if self.hang_on_delete:
                await asyncio.Event().wait()
            if self.delete_redirect:
                return httpx.Response(307, headers={"Location": self.delete_redirect})
            self.terminated = self.delete_status in {200, 204}
            return httpx.Response(self.delete_status, stream=self.delete_stream) if self.delete_stream else httpx.Response(self.delete_status)
        if request.method != "POST":
            raise AssertionError("Unexpected MCP HTTP method")
        message = json.loads(await request.aread())
        method = message["method"]
        if method in {"notifications/initialized", "notifications/cancelled"}:
            return httpx.Response(202)
        response = {"jsonrpc": "2.0", "id": message["id"]}
        if method == "initialize":
            self.initialize = message
            response["result"] = {
                "protocolVersion": message["params"]["protocolVersion"],
                "capabilities": {"resources": {}},
                "serverInfo": {"name": "offline-fixture", "version": "1"},
            }
        elif method == "resources/read":
            uri = message["params"]["uri"]
            self.reads.append(uri)
            if self.hang_on_read:
                await asyncio.Event().wait()
            if uri in self.resources:
                response["result"] = self.resources[uri].model_dump(mode="json")
            else:
                response["error"] = {"code": -32002, "message": "Fixture resource missing"}
        else:
            raise AssertionError(f"Unexpected MCP operation: {method}")
        headers = {"Mcp-Session-Id": self.session_id} if self.session_id else {}
        return httpx.Response(200, json=response, headers=headers)


class ResponseStreamFixture(httpx.AsyncByteStream):
    def __init__(self, chunks):
        self.chunks = chunks
        self.consumed = 0
        self.closed = False

    async def __aiter__(self):
        for chunk in self.chunks:
            self.consumed += len(chunk)
            yield chunk

    async def aclose(self):
        self.closed = True


class PythonMCPResponseBoundsTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.enterContext(offline_http())
        self.url = ENDPOINT + "/toolboxes/operations/versions/12/mcp?api-version=v1"
        self.credential = IdentityFixture()

    async def test_json_sse_and_false_lengths_are_bounded_before_parsing(self):
        for content_type in ("application/json", "text/event-stream"):
            for declared in (None, "1"):
                with self.subTest(content_type=content_type, declared=declared):
                    stream = ResponseStreamFixture([b"x" * 16] * 10)
                    headers = {"Content-Type": content_type}
                    if declared:
                        headers["Content-Length"] = declared
                    transport = httpx.MockTransport(lambda request: httpx.Response(200, headers=headers, stream=stream))
                    with patch.object(runtime, "_ARCHIVE_LIMIT", 32):
                        async with runtime._mcp_http_client(self.url, self.credential, transport=transport) as client:
                            with self.assertRaisesRegex(runtime.SkillsRuntimeError, "transport safety guard"):
                                await client.post(self.url, json={})
                    self.assertLessEqual(stream.consumed, 48)
                    self.assertTrue(stream.closed)

    async def test_declared_overflow_and_encoding_are_rejected_before_body_reads(self):
        for headers in ({"Content-Length": "33"}, {"Content-Length": "invalid"}, {"Content-Encoding": "gzip"}):
            with self.subTest(headers=headers):
                stream = ResponseStreamFixture([b"x" * 16])
                transport = httpx.MockTransport(lambda request: httpx.Response(200, headers=headers, stream=stream))
                with patch.object(runtime, "_ARCHIVE_LIMIT", 32):
                    async with runtime._mcp_http_client(self.url, self.credential, transport=transport) as client:
                        with self.assertRaises(runtime.SkillsRuntimeError):
                            await client.post(self.url, json={})
                self.assertEqual(stream.consumed, 0)
                self.assertTrue(stream.closed)

    async def test_small_identity_response_is_accepted(self):
        stream = ResponseStreamFixture([b"small", b" response"])

        def respond(request):
            self.assertEqual(request.headers["Accept-Encoding"], "identity")
            return httpx.Response(200, stream=stream)

        with patch.object(runtime, "_ARCHIVE_LIMIT", 32):
            async with runtime._mcp_http_client(
                self.url, self.credential, transport=httpx.MockTransport(respond),
            ) as client:
                response = await client.post(self.url, json={})
                self.assertEqual(response.text, "small response")
        self.assertTrue(stream.closed)


class PythonMCPTransportTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name) / "fam_skills"
        self.root.mkdir()
        self.path = self.root / "manifest.json"
        self.manifest = {
            "formatVersion": 1, "mode": "mcp", "projectEndpoint": ENDPOINT,
            "toolboxName": "operations", "toolboxVersion": "12",
            "skills": [{"name": "greeting", "version": "7", "sha256": hashlib.sha256(CONTENT).hexdigest()}],
        }
        self.path.write_text(json.dumps(self.manifest), encoding="utf-8")
        self.uri = "https://opaque-resource.invalid/greeting.zip"
        output = io.BytesIO()
        with zipfile.ZipFile(output, "w") as archive:
            archive.writestr("SKILL.md", CONTENT)
        data = output.getvalue()
        self.manifest["skills"][0]["archiveSHA256"] = hashlib.sha256(data).hexdigest()
        self.path.write_text(json.dumps(self.manifest), encoding="utf-8")
        index = {"skills": [
            {"name": "greeting", "type": "archive", "url": self.uri},
            {"name": "unselected", "type": "archive", "url": "skill://unselected/script.zip"},
        ]}
        self.server = MCPHTTPFixture({
            "skill://index.json": ReadResourceResult(contents=[TextResourceContents(
                uri=AnyUrl("skill://index.json"), text=json.dumps(index),
            )]),
            self.uri: ReadResourceResult(contents=[BlobResourceContents(
                uri=AnyUrl(self.uri), blob=base64.b64encode(data).decode("ascii"),
            )]),
        })
        self.credential = IdentityFixture()
        self.clients = []
        self.client_factory = runtime._mcp_http_client
        self.url = ENDPOINT + "/toolboxes/operations/versions/12/mcp?api-version=v1"

        def client_factory(url, credential):
            client = self.client_factory(url, credential, transport=httpx.MockTransport(self.server))
            self.clients.append(client)
            return client

        replacement = patch.object(runtime, "_mcp_http_client", side_effect=client_factory)
        replacement.start()
        self.addCleanup(replacement.stop)
        self.enterContext(offline_http())

    def open(self):
        return runtime.open_runtime(
            manifest_path=self.path, project_endpoint=ENDPOINT, credential=self.credential,
        )

    def assert_closed(self):
        self.assertTrue(self.clients)
        self.assertTrue(all(client.is_closed for client in self.clients))
        self.assertTrue(self.server.loops)
        self.assertTrue(all(loop.is_closed() for loop in self.server.loops))

    def test_real_transport_retains_owner_loop_through_separate_agent_runs_and_closes(self):
        model = FakeModel()
        with self.open() as provider:
            agent = Agent(client=model, context_providers=[provider])
            for _ in range(2):
                asyncio.run(agent.run("Use the greeting Skill."))
                self.assertEqual(model.loaded, CONTENT.decode() + runtime._FILE_SUFFIX)
                self.assertFalse(self.clients[0].is_closed)
                self.assertFalse(self.server.loops[0].is_closed())
            self.assertTrue(all(loop is self.server.loops[0] for loop in self.server.loops))
            self.assertEqual(model.tool_names, ["load_skill"])
        self.assert_closed()
        self.assertTrue(self.server.terminated)
        self.assertEqual({url for _, url, _ in self.server.requests}, {self.url})
        self.assertEqual(set(self.server.reads), {"skill://index.json", self.uri})
        self.assertEqual(self.credential.scopes, [runtime._TOKEN_SCOPE] * len(self.server.requests))
        authorizations = [headers["authorization"] for _, _, headers in self.server.requests]
        self.assertEqual(len(set(authorizations)), len(authorizations))
        self.assertTrue(all("foundry-features" not in headers for _, _, headers in self.server.requests))
        deletes = [headers for method, _, headers in self.server.requests if method == "DELETE"]
        self.assertEqual(len(deletes), 1)
        self.assertEqual(deletes[0]["mcp-session-id"], "offline-session")
        self.assertEqual(
            deletes[0]["mcp-protocol-version"], self.server.initialize["params"]["protocolVersion"],
        )
        self.assertEqual(deletes[0]["accept"], "application/json, text/event-stream")
        capabilities = self.server.initialize["params"]["capabilities"]
        self.assertNotIn("sampling", capabilities)
        self.assertNotIn("roots", capabilities)
        self.assertNotIn("elicitation", capabilities)

    def test_open_from_running_host_loop_retains_an_independent_owner_loop(self):
        async def use_runtime():
            with self.open() as provider:
                model = FakeModel()
                agent = Agent(client=model, context_providers=[provider])
                await agent.run("Use the greeting Skill.")
                self.assertIsNot(asyncio.get_running_loop(), self.server.loops[0])
                self.assertIn("FAM_SKILL_BODY_4b753", model.loaded)

        asyncio.run(use_runtime())
        self.assert_closed()
        self.assertTrue(self.credential.scopes)

    def test_loop_bound_async_credentials_are_rejected_before_starting_a_session(self):
        class AsyncIdentity:
            async def get_token(self, scope):
                raise AssertionError("async identity must not be moved to another loop")

        self.credential = AsyncIdentity()
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "synchronous Azure identity"):
            with self.open():
                self.fail("loop-bound credential was accepted")
        self.assertEqual(self.clients, [])
        self.assertEqual(self.server.requests, [])

    def test_host_failure_preserves_exception_and_closes_session(self):
        with self.assertRaisesRegex(RuntimeError, "^fixture host failed$"):
            with self.open():
                raise RuntimeError("fixture host failed")
        self.assert_closed()
        self.assertTrue(self.server.terminated)

    def test_delete_requires_acknowledgement_and_still_disposes_local_resources(self):
        for status in (202, 405, 500):
            with self.subTest(status=status):
                self.server.delete_status = status
                before = sum(method == "DELETE" for method, _, _ in self.server.requests)
                with self.assertRaisesRegex(runtime.SkillsRuntimeError, f"termination.*HTTP {status}"):
                    with self.open():
                        pass
                deletes = [url for method, url, _ in self.server.requests if method == "DELETE"]
                self.assertEqual(len(deletes), before + 1)
                self.assertEqual(set(deletes), {self.url})
                self.assertFalse(self.server.terminated)
                self.assert_closed()

    def test_shutdown_identity_failure_is_not_swallowed(self):
        failure = RuntimeError("fixture token renewal failed")
        with self.assertRaisesRegex(RuntimeError, "^fixture token renewal failed$") as caught:
            with self.open():
                before = len(self.credential.scopes)
                self.credential.failure = failure
        self.assertIs(caught.exception, failure)
        self.assertEqual(len(self.credential.scopes), before + 1)
        self.assertEqual(self.credential.scopes[-1], runtime._TOKEN_SCOPE)
        self.assertFalse(any(method == "DELETE" for method, _, _ in self.server.requests))
        self.assertFalse(self.server.terminated)
        self.assert_closed()

    def test_host_failure_is_chained_when_termination_also_fails(self):
        body_error = RuntimeError("fixture host failed")
        self.server.delete_status = 500
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "termination.*HTTP 500") as caught:
            with self.open():
                raise body_error
        self.assertIs(caught.exception.__context__, body_error)
        self.assertFalse(self.server.terminated)
        self.assert_closed()

    def test_shutdown_redirect_is_not_followed(self):
        self.server.delete_redirect = self.url.replace("/versions/12/", "/versions/13/")
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "redirects are not allowed"):
            with self.open():
                pass
        self.assertEqual(
            [url for method, url, _ in self.server.requests if method == "DELETE"], [self.url],
        )
        self.assertFalse(self.server.terminated)
        self.assert_closed()

    def test_hung_delete_observes_cleanup_deadline_and_disposes_locally(self):
        self.server.hang_on_delete = True
        with patch.object(runtime, "_CLOSE_TIMEOUT", 0.5):
            with self.assertRaises(TimeoutError):
                with self.open():
                    pass
        self.assertFalse(self.server.terminated)
        self.assert_closed()

    def test_no_server_session_id_means_no_targeted_delete(self):
        self.server.session_id = None
        with self.open():
            pass
        self.assertFalse(any(method == "DELETE" for method, _, _ in self.server.requests))
        self.assert_closed()

    def test_missing_index_or_archive_readiness_failure_closes_session(self):
        original = dict(self.server.resources)
        for uri in original:
            with self.subTest(uri=uri):
                self.server.resources = {key: value for key, value in original.items() if key != uri}
                self.server.terminated = False
                with self.assertRaises(McpError):
                    with self.open():
                        self.fail("missing resource reached readiness")
                self.assert_closed()
                self.assertTrue(self.server.terminated)

    def test_manifest_cannot_switch_toolbox_pins_during_connection_startup(self):
        qualify = runtime._qualify_mcp

        async def replace_manifest(**kwargs):
            self.manifest["toolboxVersion"] = "13"
            self.path.write_text(json.dumps(self.manifest), encoding="utf-8")
            return await qualify(**kwargs)

        with patch.object(runtime, "_qualify_mcp", new=replace_manifest):
            with self.assertRaisesRegex(runtime.SkillsRuntimeError, "manifest changed while opening"):
                with self.open():
                    self.fail("connection used a different pin than the locked provider")
        self.assertEqual(self.server.reads, [])
        self.assertEqual({url for _, url, _ in self.server.requests}, {self.url})
        self.assert_closed()
        self.assertTrue(self.server.terminated)

    def test_changed_toolbox_pin_is_rejected_before_credentials(self):
        self.manifest["toolboxVersion"] = "latest"
        self.path.write_text(json.dumps(self.manifest), encoding="utf-8")
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "immutable Toolbox"):
            with self.open():
                self.fail("mutable Toolbox reached readiness")
        self.assertEqual(self.credential.scopes, [])
        self.assertEqual(self.server.requests, [])

    def test_exact_target_guard_runs_before_identity_for_each_host_path_and_query(self):
        async def exercise():
            async with self.client_factory(
                self.url, self.credential, transport=httpx.MockTransport(self.server),
            ) as client:
                targets = [
                    self.url.replace("account.services.ai.azure.com", "other.services.ai.azure.com"),
                    self.url.replace("/projects/project/", "/projects/other/"),
                    self.url.replace("/versions/12/", "/versions/13/"),
                    self.url.replace("api-version=v1", "api-version=v2"),
                ]
                for target in targets:
                    with self.subTest(target=target):
                        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "pinned same-project"):
                            await client.post(target, json={})
        asyncio.run(exercise())
        self.assertEqual(self.credential.scopes, [])
        self.assertEqual(self.server.requests, [])

    def test_redirect_is_denied_even_within_the_same_origin(self):
        self.server.redirect = self.url.replace("/versions/12/", "/versions/13/")

        async def exercise():
            async with self.client_factory(
                self.url, self.credential, transport=httpx.MockTransport(self.server),
            ) as client:
                from mcp.shared._httpx_utils import stream_within_origin

                with self.assertRaisesRegex(runtime.SkillsRuntimeError, "redirects"):
                    async with stream_within_origin(client, "POST", self.url, json={}):
                        self.fail("SDK followed a forbidden redirect")
        asyncio.run(exercise())
        self.assertEqual(len(self.server.requests), 1)
        self.assertEqual(self.server.requests[0][1], self.url)

    def test_expired_identity_token_is_not_sent(self):
        self.credential.expired = True

        async def exercise():
            async with self.client_factory(
                self.url, self.credential, transport=httpx.MockTransport(self.server),
            ) as client:
                with self.assertRaisesRegex(runtime.SkillsRuntimeError, "expired access token"):
                    await client.post(self.url, json={})
        asyncio.run(exercise())
        self.assertEqual(self.server.requests, [])

    def test_hung_resource_read_has_a_bounded_timeout_and_cleans_up(self):
        self.server.hang_on_read = True
        with patch.object(runtime, "_REQUEST_TIMEOUT", 0.5):
            with self.assertRaises((TimeoutError, McpError)):
                with self.open():
                    self.fail("hung read reached readiness")
        self.assert_closed()
        self.assertTrue(self.server.terminated)

    def test_delete_body_overflow_is_bounded_and_local_resources_are_closed(self):
        stream = ResponseStreamFixture([b"x" * 16] * 10)
        with self.assertRaisesRegex(runtime.SkillsRuntimeError, "transport safety guard"):
            with self.open():
                self.server.delete_stream = stream
                self.enterContext(patch.object(runtime, "_MANIFEST_LIMIT", 32))
        self.assertLessEqual(stream.consumed, 48)
        self.assertTrue(stream.closed)
        self.assert_closed()

    def test_empty_mcp_inventory_detaches_without_transport_or_authentication(self):
        self.manifest["skills"] = []
        self.manifest["projectEndpoint"] = ""
        del self.manifest["toolboxName"]
        del self.manifest["toolboxVersion"]
        self.path.write_text(json.dumps(self.manifest), encoding="utf-8")
        with self.open() as provider:
            agent = Agent(client=FakeModel())
            context = SessionContext(input_messages=[])
            asyncio.run(provider.before_run(
                agent=agent, session=agent.create_session(), context=context, state={},
            ))
            self.assertEqual(context.tools, [])
            self.assertEqual(context.instructions, [])
        self.assertEqual(self.clients, [])
        self.assertEqual(self.server.requests, [])
        self.assertEqual(self.credential.scopes, [])


if __name__ == "__main__":
    program = unittest.main(exit=False)
    if not program.result.testsRun or program.result.skipped:
        sys.exit("Skills qualification requires a nonempty suite without skipped tests")
    sys.exit(0 if program.result.wasSuccessful() else 1)
