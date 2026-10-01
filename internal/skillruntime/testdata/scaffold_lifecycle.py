"""Exercise generated main.py with fake dependencies, not SDK qualification."""

import json
import os
from pathlib import Path
import runpy
import sys
import tempfile
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import patch


SOURCE = Path(sys.argv.pop(1)).read_text(encoding="utf-8")
EXPECT_TOOLS = sys.argv.pop(1) == "tools"
PROJECT_ENDPOINT = "https://account.services.ai.azure.com/api/projects/project"

HELPER = """
from contextlib import contextmanager
import json
from fam_scaffold_state import state

@contextmanager
def open_runtime(*, manifest_path, project_endpoint, credential):
    assert manifest_path == state.manifest_path
    assert project_endpoint == state.project_endpoint
    assert credential is state.credential
    state.events.append("enter")
    state.active = True
    try:
        with manifest_path.open(encoding="utf-8") as stream:
            json.load(stream)
        if state.readiness_error:
            raise RuntimeError("provider readiness rejected")
        yield None if state.empty_provider else state.provider
    finally:
        state.active = False
        state.events.append("exit")
"""


def module(name, **members):
    result = ModuleType(name)
    result.__dict__.update(members)
    return result


class ScaffoldLifecycleTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.main = self.root / "main.py"
        self.main.write_text(SOURCE, encoding="utf-8")
        self.skills = self.root / "fam_skills"
        self.state = SimpleNamespace(
            events=[],
            active=False,
            expect_skills=False,
            provider=object(),
            credential=object(),
            manifest_path=self.skills / "manifest.json",
            project_endpoint=PROJECT_ENDPOINT,
            readiness_error=False,
            empty_provider=False,
            agent_error=False,
            host_error=False,
            options=None,
        )
        state = self.state

        class Agent:
            def __init__(self, **options):
                state.events.append("agent")
                state.options = options
                if state.expect_skills:
                    assert state.active
                    assert options["context_providers"] == [state.provider]
                else:
                    assert "context_providers" not in options
                assert options["default_options"] == {"store": False}
                if EXPECT_TOOLS:
                    assert len(options["tools"]) == 3
                    assert options["tools"][0].approval_mode == os.getenv(
                        "TOOLBOX_APPROVAL_MODE", "always_require"
                    )
                else:
                    assert "tools" not in options
                if state.agent_error:
                    raise RuntimeError("agent construction rejected")

        class Host:
            def __init__(self, agent):
                self.agent = agent

            def run(self):
                state.events.append("host")
                assert state.active == state.expect_skills
                if state.host_error:
                    raise RuntimeError("host rejected")

        class FoundryChatClient:
            def __init__(self, **options):
                assert options["credential"] is state.credential
                state.events.append("client")

            @staticmethod
            def get_bing_grounding_tool(**options):
                return options

            @staticmethod
            def get_bing_custom_search_tool(**options):
                return options

        class FoundryToolbox:
            def __init__(self, credential):
                assert credential is state.credential

        def project_client(**options):
            return SimpleNamespace(
                connections=SimpleNamespace(
                    get=lambda name: SimpleNamespace(id="connection-" + name)
                )
            )

        modules = {
            "agent_framework": module("agent_framework", Agent=Agent),
            "agent_framework.foundry": module(
                "agent_framework.foundry", FoundryChatClient=FoundryChatClient
            ),
            "agent_framework_foundry_hosting": module(
                "agent_framework_foundry_hosting",
                ResponsesHostServer=Host,
                InvocationsHostServer=Host,
                FoundryToolbox=FoundryToolbox,
            ),
            "azure": module("azure"),
            "azure.ai": module("azure.ai"),
            "azure.ai.projects": module("azure.ai.projects", AIProjectClient=project_client),
            "azure.identity": module(
                "azure.identity", DefaultAzureCredential=lambda: state.credential
            ),
            "dotenv": module("dotenv", load_dotenv=lambda: None),
            "fam_scaffold_state": module("fam_scaffold_state", state=state),
        }
        self.enterContext(patch.dict(sys.modules, modules))
        self.enterContext(
            patch.dict(
                os.environ,
                {
                    "FOUNDRY_PROJECT_ENDPOINT": PROJECT_ENDPOINT,
                    "AZURE_AI_MODEL_DEPLOYMENT_NAME": "fake-model",
                    "BING_GROUNDING_CONNECTION_NAME": "bing",
                    "BING_CUSTOM_SEARCH_CONNECTION_NAME": "custom",
                    "BING_CUSTOM_SEARCH_INSTANCE_NAME": "instance",
                    "TOOLBOX_APPROVAL_MODE": "always_require",
                },
            )
        )
        self.start = runpy.run_path(str(self.main), run_name="fam_scaffold_test")["main"]
        self.addCleanup(self.assertNotIn, "_fam_skills_runtime", sys.modules)

    def install_helper(self):
        self.skills.mkdir()
        self.state.manifest_path.write_text('{"formatVersion": 1}', encoding="utf-8")
        (self.skills / "fam_skills_runtime.py").write_text(HELPER, encoding="utf-8")
        self.state.expect_skills = True

    def test_no_skills_preserves_agent_options(self):
        self.start()
        self.assertEqual(self.state.events, ["client", "agent", "host"])

    def test_provider_lives_until_host_shutdown(self):
        self.install_helper()
        self.start()
        self.assertEqual(self.state.events, ["enter", "client", "agent", "host", "exit"])
        self.assertFalse(self.state.active)
        self.assertEqual(
            sorted(path.name for path in self.skills.iterdir()),
            ["fam_skills_runtime.py", "manifest.json"],
        )

    def test_host_failure_closes_provider(self):
        self.install_helper()
        self.state.host_error = True
        with self.assertRaisesRegex(RuntimeError, "host rejected"):
            self.start()
        self.assertEqual(self.state.events, ["enter", "client", "agent", "host", "exit"])
        self.assertFalse(self.state.active)

    def test_agent_failure_closes_provider(self):
        self.install_helper()
        self.state.agent_error = True
        with self.assertRaisesRegex(RuntimeError, "agent construction rejected"):
            self.start()
        self.assertEqual(self.state.events, ["enter", "client", "agent", "exit"])

    def test_readiness_failure_prevents_agent_and_host_start(self):
        self.install_helper()
        self.state.readiness_error = True
        with self.assertRaisesRegex(RuntimeError, "provider readiness rejected"):
            self.start()
        self.assertEqual(self.state.events, ["enter", "exit"])

    def test_empty_provider_is_not_ready(self):
        self.install_helper()
        self.state.empty_provider = True
        with self.assertRaisesRegex(RuntimeError, "ready context provider"):
            self.start()
        self.assertEqual(self.state.events, ["enter", "exit"])

    def test_missing_helper_fails_before_client_creation(self):
        self.skills.mkdir()
        self.state.manifest_path.write_text("{}", encoding="utf-8")
        with self.assertRaisesRegex(RuntimeError, "artifacts are incomplete"):
            self.start()
        self.assertEqual(self.state.events, [])

    def test_missing_manifest_does_not_silently_disable_skills(self):
        self.skills.mkdir()
        with self.assertRaisesRegex(RuntimeError, "artifacts are incomplete"):
            self.start()
        self.assertEqual(self.state.events, [])

    def test_malformed_manifest_error_is_not_suppressed(self):
        self.install_helper()
        self.state.manifest_path.write_text("{", encoding="utf-8")
        with self.assertRaises(json.JSONDecodeError):
            self.start()
        self.assertEqual(self.state.events, ["enter", "exit"])

    def test_manifest_must_be_a_regular_file(self):
        self.skills.mkdir()
        self.state.manifest_path.mkdir()
        with self.assertRaisesRegex(RuntimeError, "regular files"):
            self.start()
        self.assertEqual(self.state.events, [])

    def test_skills_root_must_be_a_directory(self):
        self.skills.write_text("not a directory", encoding="utf-8")
        with self.assertRaisesRegex(RuntimeError, "real directory"):
            self.start()
        self.assertEqual(self.state.events, [])

    def test_helper_must_be_a_regular_file(self):
        self.skills.mkdir()
        self.state.manifest_path.write_text("{}", encoding="utf-8")
        (self.skills / "fam_skills_runtime.py").mkdir()
        with self.assertRaisesRegex(RuntimeError, "regular files"):
            self.start()
        self.assertEqual(self.state.events, [])

    def test_helper_import_failure_is_not_suppressed(self):
        self.install_helper()
        (self.skills / "fam_skills_runtime.py").write_text(
            'raise RuntimeError("helper import rejected")\n', encoding="utf-8"
        )
        with self.assertRaisesRegex(RuntimeError, "helper import rejected"):
            self.start()
        self.assertEqual(self.state.events, [])

    def test_existing_tool_approval_opt_out_is_preserved(self):
        if not EXPECT_TOOLS:
            self.skipTest("this scaffold does not configure a Toolbox")
        self.install_helper()
        with patch.dict(os.environ, {"TOOLBOX_APPROVAL_MODE": "never_require"}):
            self.start()
        self.assertEqual(self.state.options["tools"][0].approval_mode, "never_require")


if __name__ == "__main__":
    unittest.main()
