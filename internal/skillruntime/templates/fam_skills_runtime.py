"""FAM instructions-only Skills adapter for agent-framework-core 1.19.0."""

from __future__ import annotations

import asyncio
import base64
import binascii
from concurrent.futures import ThreadPoolExecutor
from contextlib import AsyncExitStack, asynccontextmanager, contextmanager
from datetime import timedelta
from functools import partial
import hashlib
from importlib.metadata import version
import inspect
import io
import json
import os
from pathlib import Path
import re
import stat
import time
from typing import Iterator
from urllib.parse import quote, urlsplit
import xml.etree.ElementTree as ET
import zipfile

import yaml
from agent_framework import (
    AgentSession,
    BaseAgent,
    ContextProvider,
    FunctionTool,
    SessionContext,
    SkillsProvider,
)


CORE_VERSION = "1.19.0"
MCP_VERSION = "1.30.0"
_TOKEN_SCOPE = "https://ai.azure.com/.default"
_REQUEST_TIMEOUT = 30.0
_STARTUP_TIMEOUT = 60.0
_CLOSE_TIMEOUT = 30.0
# Existing FAM filesystem safety guards, not Azure Skills quotas.
_MANIFEST_LIMIT = 8 << 20
_SKILL_LIMIT = 64 << 20
_ARCHIVE_LIMIT = 256 << 20
_NAME = re.compile(r"[a-z0-9]+(?:-[a-z0-9]+)*\Z")
_SHA256 = re.compile(r"[0-9a-f]{64}\Z")
_MANIFEST_FIELDS = {
    "formatVersion", "mode", "projectEndpoint", "toolboxName", "toolboxVersion",
    "skills", "service", "language", "declarationHash", "imageReference",
    "imageEvidenceSHA256", "runtimeFiles",
}
_ENTRY_FIELDS = {"name", "version", "sha256", "path", "sourcePath", "archiveSHA256"}
_PROMPT = (
    "<fam_skills>\n"
    "<usage>Use load_skill to retrieve a selected Skill's instructions when needed. "
    "These Skills provide instructions only, not resources or scripts.</usage>\n"
    "{skills}\n"
    "</fam_skills>"
)
_FILE_SUFFIX = "\n\n<available_resources />\n\n<available_scripts />"


class SkillsRuntimeError(RuntimeError):
    """Required Skill artifacts or provider readiness could not be verified."""


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SkillsRuntimeError(message)


def _regular(path: Path, *, directory: bool = False):
    info = path.lstat()
    reparse = getattr(info, "st_file_attributes", 0) & getattr(
        stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400
    )
    kind = stat.S_ISDIR if directory else stat.S_ISREG
    _require(
        kind(info.st_mode) and not reparse,
        "FAM Skills paths must be regular files and real directories, not links",
    )
    return info


def _read(path: Path, limit: int) -> bytes:
    before = _regular(path)
    _require(before.st_size <= limit, "FAM Skills file exceeds its filesystem safety guard")
    with path.open("rb") as stream:
        opened = os.fstat(stream.fileno())
        _require(
            stat.S_ISREG(opened.st_mode) and os.path.samestat(before, opened),
            "FAM Skills file changed while opening",
        )
        data = stream.read(limit + 1)
        after = os.fstat(stream.fileno())
    _require(
        len(data) == before.st_size <= limit
        and os.path.samestat(after, _regular(path))
        and before.st_mtime_ns == after.st_mtime_ns,
        "FAM Skills file changed while reading or exceeded its filesystem safety guard",
    )
    return data


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        _require(key not in result, "FAM Skills JSON contains duplicate properties")
        result[key] = value
    return result


def _json(data: bytes):
    def reject_constant(_):
        raise SkillsRuntimeError("FAM Skills JSON contains a non-finite number")

    try:
        return json.loads(
            data.decode("utf-8"), object_pairs_hook=_unique_object,
            parse_constant=reject_constant,
        )
    except (UnicodeError, json.JSONDecodeError, RecursionError):
        raise SkillsRuntimeError("FAM Skills JSON is malformed UTF-8 JSON") from None


def _text(value) -> bool:
    return isinstance(value, str) and "\x00" not in value and not any(
        0xD800 <= ord(character) <= 0xDFFF for character in value
    )


def _name(value) -> bool:
    return isinstance(value, str) and len(value) <= 64 and _NAME.fullmatch(value) is not None


def _digest(value) -> bool:
    return isinstance(value, str) and _SHA256.fullmatch(value) is not None


def _identifier(value) -> bool:
    return (
        _text(value) and bool(value) and value not in {".", ".."}
        and not any(c.isspace() or not c.isprintable() or c in "/\\:%?#*" for c in value)
    )


def _pin(value) -> bool:
    return _identifier(value) and value.lower() not in {"latest", "default"}


def _endpoint(value: str) -> str:
    _require(_text(value) and bool(value), "FAM Skills requires a trusted project endpoint")
    try:
        parsed = urlsplit(value)
        port = parsed.port
    except ValueError:
        raise SkillsRuntimeError("FAM Skills project endpoint is invalid") from None
    host = parsed.hostname or ""
    suffixes = ("services.ai.azure.com", "cognitiveservices.azure.com", "openai.azure.com")
    _require(
        parsed.scheme == "https" and parsed.username is None and parsed.password is None
        and port is None and not parsed.query and not parsed.fragment
        and not any(c.isspace() or c in "\\%" for c in value)
        and any(host.endswith("." + suffix) for suffix in suffixes),
        "FAM Skills project endpoint must be a trusted Azure HTTPS project endpoint",
    )
    path = parsed.path.rstrip("/")
    parts = path.split("/")
    _require(
        len(parts) == 4 and parts[:3] == ["", "api", "projects"]
        and parts[3] not in {"", ".", ".."},
        "FAM Skills endpoint must identify exactly one Foundry project",
    )
    return "https://" + host.lower() + path


def _manifest(path: Path, project_endpoint: str):
    _require(
        path.name == "manifest.json" and path.parent.name == "fam_skills",
        "FAM Skills manifest must be fam_skills/manifest.json beneath the app source",
    )
    root = path.parent
    _regular(root, directory=True)
    encoded = _read(path, _MANIFEST_LIMIT)
    manifest = _json(encoded)
    _require(isinstance(manifest, dict), "FAM Skills manifest must be an object")
    _require(not manifest.keys() - _MANIFEST_FIELDS, "FAM Skills manifest has unknown fields")
    _require(
        type(manifest.get("formatVersion")) is int and manifest["formatVersion"] == 1,
        "FAM Skills manifest formatVersion must be 1",
    )
    mode = manifest.get("mode")
    _require(isinstance(mode, str) and mode in {"bundle", "mcp"}, "FAM Skills mode must be bundle or mcp")
    entries = manifest.get("skills")
    _require(isinstance(entries, list), "FAM Skills manifest must contain an explicit skills array")
    endpoint = manifest.get("projectEndpoint")
    _require(_text(endpoint), "FAM Skills projectEndpoint must be a string")
    if endpoint:
        _require(
            _endpoint(endpoint) == _endpoint(project_endpoint),
            "FAM Skills manifest belongs to a different Foundry project",
        )
    else:
        _require(mode == "bundle" or not entries, "MCP Skills require an explicit project binding")
    for field in ("service", "language", "imageReference"):
        if field in manifest:
            _require(_text(manifest[field]) and bool(manifest[field]), "FAM Skills provenance is invalid")
    if "language" in manifest:
        _require(manifest["language"] == "python", "FAM Skills manifest is not for Python")
    for field in ("declarationHash", "imageEvidenceSHA256"):
        if field in manifest:
            _require(_digest(manifest[field]), "FAM Skills provenance digest is invalid")
    if "runtimeFiles" in manifest:
        runtime_files = manifest["runtimeFiles"]
        _require(
            isinstance(runtime_files, dict) and not runtime_files.keys() - {"fam_skills_runtime.py"},
            "FAM Skills runtimeFiles must contain only the Python adapter",
        )
        for name, digest in runtime_files.items():
            _require(_digest(digest), "FAM Skills helper digest is invalid")
            _require(
                hashlib.sha256(_read(root / name, _MANIFEST_LIMIT)).hexdigest() == digest,
                "FAM Skills helper digest differs from its lock",
            )
    names = set()
    for entry in entries:
        _validate_entry(entry, mode, endpoint)
        _require(entry["name"] not in names, "FAM Skills inventory contains duplicate names")
        names.add(entry["name"])
    if mode == "mcp" and (entries or "toolboxName" in manifest or "toolboxVersion" in manifest):
        _require(
            _identifier(manifest.get("toolboxName")) and _pin(manifest.get("toolboxVersion")),
            "MCP Skills require an explicit immutable Toolbox name/version",
        )
    elif mode == "bundle":
        _require(
            "toolboxName" not in manifest and "toolboxVersion" not in manifest,
            "Bundle Skills must not configure MCP",
        )
    return manifest, encoded


def _validate_entry(entry, mode: str, endpoint: str) -> None:
    _require(isinstance(entry, dict), "FAM Skills inventory entries must be objects")
    _require(not entry.keys() - _ENTRY_FIELDS, "FAM Skills inventory has unknown fields")
    _require(_name(entry.get("name")), "FAM Skills inventory name is invalid")
    _require(_digest(entry.get("sha256")), "FAM Skills inventory SHA-256 is invalid")
    if "version" in entry:
        _require(_pin(entry["version"]) and bool(endpoint), "Remote Skills require a pinned version/project")
    if mode == "mcp":
        _require("version" in entry and "path" not in entry, "MCP Skills require versions, not bundle paths")
    else:
        _require(
            entry.get("path") == entry["name"] + "/SKILL.md",
            "Bundle Skill paths must be exactly name/SKILL.md",
        )
    if "sourcePath" in entry:
        _require(_text(entry["sourcePath"]) and bool(entry["sourcePath"]), "FAM Skills source provenance is invalid")
    if "archiveSHA256" in entry:
        _require(_digest(entry["archiveSHA256"]), "FAM Skills archive provenance digest is invalid")


def _yaml_mapping(node):
    _require(
        isinstance(node, yaml.MappingNode) and node.tag == "tag:yaml.org,2002:map",
        "SKILL.md frontmatter must contain a YAML string-keyed mapping",
    )
    values = {}
    for key, value in node.value:
        _require(
            isinstance(key, yaml.ScalarNode) and key.tag == "tag:yaml.org,2002:str"
            and key.value not in values,
            "SKILL.md frontmatter has duplicate or non-string keys",
        )
        values[key.value] = value
    return values


def _instruction(data: bytes, expected_name: str) -> tuple[str, str]:
    _require(len(data) <= _SKILL_LIMIT, "SKILL.md exceeds the FAM filesystem safety guard")
    try:
        text = data.decode("utf-8")
    except UnicodeError:
        raise SkillsRuntimeError("SKILL.md must be valid UTF-8") from None
    _require(_text(text), "SKILL.md must not contain NUL or surrogate characters")
    lines = text.splitlines(keepends=True)
    _require(lines and lines[0].rstrip("\r\n") == "---", "SKILL.md must start with YAML frontmatter")
    closing = next((i for i in range(1, len(lines)) if lines[i].rstrip("\r\n") == "---"), None)
    _require(closing is not None, "SKILL.md is missing its closing frontmatter delimiter")
    header = "".join(lines[1:closing])
    _require("".join(lines[closing + 1:]).strip() != "", "SKILL.md instructions must not be empty")
    try:
        for event in yaml.parse(header, Loader=yaml.SafeLoader):
            _require(
                not isinstance(event, yaml.AliasEvent) and not getattr(event, "anchor", None),
                "SKILL.md frontmatter must not use anchors or aliases",
            )
        fields = _yaml_mapping(yaml.compose(header, Loader=yaml.SafeLoader))
    except (yaml.YAMLError, RecursionError, ValueError, OverflowError):
        raise SkillsRuntimeError("SKILL.md frontmatter is malformed") from None
    _require(
        not fields.keys() - {"name", "description", "license", "compatibility", "allowed-tools", "metadata"},
        "SKILL.md frontmatter contains unsupported fields",
    )
    for key in ("name", "description"):
        node = fields.get(key)
        _require(
            isinstance(node, yaml.ScalarNode) and node.style is None
            and _text(node.value) and bool(node.value.strip()),
            "SKILL.md name and description must be plain, unquoted YAML text",
        )
    _require(fields["name"].value == expected_name, "SKILL.md name differs from its locked identity")
    description = fields["description"].value
    _require(len(description) <= 1024, "SKILL.md description exceeds 1024 characters")
    for key in ("license", "compatibility", "allowed-tools"):
        if key in fields:
            node = fields[key]
            _require(
                isinstance(node, yaml.ScalarNode) and node.tag == "tag:yaml.org,2002:str"
                and _text(node.value),
                "SKILL.md optional frontmatter values must be strings",
            )
    if "compatibility" in fields:
        _require(len(fields["compatibility"].value) <= 500, "SKILL.md compatibility exceeds SDK's 500-character limit")
    if "metadata" in fields:
        for node in _yaml_mapping(fields["metadata"]).values():
            _require(
                isinstance(node, yaml.ScalarNode) and node.tag == "tag:yaml.org,2002:str" and _text(node.value),
                "SKILL.md metadata must be a string mapping",
            )
    return text, description


def _bundle(root: Path, manifest):
    names = {entry["name"] for entry in manifest["skills"]}
    allowed = names | {"manifest.json", ".ownership.json", "fam_skills_runtime.py"}
    for path in root.iterdir():
        _require(path.name in allowed, "FAM Skills artifact contains unexpected files or directories")
        _regular(path, directory=path.name in names)
    contents = {}
    descriptions = {}
    for entry in manifest["skills"]:
        directory = root / entry["name"]
        _regular(directory, directory=True)
        _require(
            {path.name for path in directory.iterdir()} == {"SKILL.md"},
            "Instructions-only Skills must contain only SKILL.md; scripts/resources are prohibited",
        )
        data = _read(directory / "SKILL.md", _SKILL_LIMIT)
        _require(hashlib.sha256(data).hexdigest() == entry["sha256"], "SKILL.md digest differs from its lock")
        text, description = _instruction(data, entry["name"])
        # FileSkillsSource uses read_text(), whose universal-newline handling is
        # distinct from the byte-exact digest checked above.
        contents[entry["name"]] = text.replace("\r\n", "\n").replace("\r", "\n") + _FILE_SUFFIX
        descriptions[entry["name"]] = description
    return contents, descriptions


def _archive(data: bytes) -> bytes:
    _require(len(data) <= _ARCHIVE_LIMIT, "MCP Skill ZIP exceeds the FAM archive safety guard")
    try:
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            entries = archive.infolist()
            _require(len(entries) == 1, "Instructions-only MCP Skill archives must contain only SKILL.md")
            entry = entries[0]
            mode = entry.external_attr >> 16
            _require(
                entry.orig_filename == entry.filename == "SKILL.md"
                and not entry.is_dir() and not entry.flag_bits & 1
                and stat.S_IFMT(mode) in {0, stat.S_IFREG}
                and not entry.external_attr & 0x10,
                "MCP Skill archive contains a nonregular, encrypted, or unsafe entry",
            )
            _require(entry.file_size <= _SKILL_LIMIT, "MCP SKILL.md exceeds the FAM filesystem safety guard")
            with archive.open(entry) as stream:
                content = stream.read(_SKILL_LIMIT + 1)
            _require(
                len(content) == entry.file_size <= _SKILL_LIMIT,
                "MCP Skill archive content is incomplete or exceeds its safety guard",
            )
            return content
    except (zipfile.BadZipFile, EOFError, OSError, NotImplementedError):
        raise SkillsRuntimeError("MCP Skill archive is malformed or unsupported") from None


class _MCPResources:
    """Read-only SDK session facade: only a filtered index and locked instructions."""

    def __init__(self, session, manifest):
        self._session = session
        self._expected = {entry["name"]: entry for entry in manifest["skills"]}
        self._catalog = None
        self._uris = {}
        self.contents = {}
        self.descriptions = {}

    async def prime(self):
        from pydantic import AnyUrl

        await self.read_resource(AnyUrl("skill://index.json"))
        for uri in self._uris:
            await self.read_resource(AnyUrl(uri))
        _require(
            self.contents.keys() == self._expected.keys(),
            "MCP discovery is missing selected Skills",
        )

    async def read_resource(self, uri):
        from mcp.types import BlobResourceContents, ReadResourceResult, TextResourceContents

        requested = str(uri)
        _require(
            requested == "skill://index.json" or requested in self._uris,
            "MCP resource is not in the selected instructions-only inventory",
        )
        result = await self._session.read_resource(uri)
        _require(len(result.contents) == 1, "MCP Skill resource must return exactly one content block")
        content = result.contents[0]
        _require(str(content.uri) == requested, "MCP Skill response URI differs from the requested resource")
        if requested == "skill://index.json":
            _require(isinstance(content, TextResourceContents), "MCP Skill index must be JSON text")
            filtered = self._index(content.text.encode("utf-8"))
            return ReadResourceResult(contents=[TextResourceContents(
                uri=uri, mimeType="application/json", text=json.dumps(filtered),
            )])
        selected = self._uris[requested]
        if selected["type"] == "archive":
            _require(isinstance(content, BlobResourceContents), "MCP Skill archive must be a binary resource")
            _require(
                len(content.blob) <= ((_ARCHIVE_LIMIT + 2) // 3) * 4,
                "MCP Skill archive exceeds the FAM archive safety guard",
            )
            try:
                archive = base64.b64decode(content.blob, validate=True)
            except (binascii.Error, ValueError):
                raise SkillsRuntimeError("MCP Skill archive is not valid base64") from None
            if selected.get("digest") is not None:
                _require(
                    selected["digest"] == "sha256:" + hashlib.sha256(archive).hexdigest(),
                    "MCP Skill archive digest does not match its index",
                )
            archive_digest = self._expected[selected["name"]].get("archiveSHA256")
            if archive_digest is not None:
                _require(
                    hashlib.sha256(archive).hexdigest() == archive_digest,
                    "MCP Skill archive digest differs from its lock",
                )
            data = _archive(archive)
            suffix = _FILE_SUFFIX
        else:
            _require(isinstance(content, TextResourceContents), "MCP SKILL.md must be a text resource")
            data = content.text.encode("utf-8")
            suffix = ""
            if selected.get("digest") is not None:
                _require(
                    selected["digest"] == "sha256:" + hashlib.sha256(data).hexdigest(),
                    "MCP SKILL.md digest does not match its index",
                )
        locked = self._expected[selected["name"]]
        _require(hashlib.sha256(data).hexdigest() == locked["sha256"], "MCP SKILL.md digest differs from its lock")
        text, description = _instruction(data, selected["name"])
        _require(
            selected.get("description", description) == description,
            "MCP index description differs from the locked instructions",
        )
        self.contents[selected["name"]] = text + suffix
        self.descriptions[selected["name"]] = description
        return result

    def _index(self, data: bytes):
        from pydantic import AnyUrl, ValidationError

        _require(len(data) <= _MANIFEST_LIMIT, "MCP Skill index exceeds the FAM JSON safety guard")
        index = _json(data)
        _require(
            isinstance(index, dict) and not index.keys() - {"$schema", "skills"}
            and isinstance(index.get("skills"), list),
            "MCP Skill index is malformed or missing",
        )
        selected = []
        names = set()
        uris = {}
        for entry in index["skills"]:
            _require(isinstance(entry, dict) and _name(entry.get("name")), "MCP Skill index identity is invalid")
            name = entry["name"]
            _require(name not in names, "MCP Skill index contains duplicate identities")
            names.add(name)
            if name not in self._expected:
                continue
            _require(
                not entry.keys() - {"name", "type", "description", "url", "digest"}
                and isinstance(entry.get("type"), str) and entry["type"] in {"archive", "skill-md"},
                "Selected MCP Skill has an unsupported discovery format",
            )
            uri = entry.get("url")
            _require(_text(uri) and bool(uri), "Selected MCP Skill is missing its resource URI")
            try:
                parsed = AnyUrl(uri)
            except ValidationError:
                raise SkillsRuntimeError("Selected MCP Skill resource URI is malformed") from None
            _require(
                parsed.username is None and parsed.password is None
                and not parsed.query and not parsed.fragment
                and not any(c.isspace() or not c.isprintable() or c == "\\" for c in uri),
                "Selected MCP Skill has an unsafe or unsupported resource URI",
            )
            # Resource identifiers are opaque MCP parameters, never HTTP targets.
            # Canonicalize exactly as the SDK's AnyUrl-based read_resource does.
            uri = str(parsed)
            _require(uri != "skill://index.json" and uri not in uris, "MCP Skill resource URI is duplicated or reserved")
            entry = dict(entry, url=uri)
            if entry.get("digest") is not None:
                _require(
                    isinstance(entry["digest"], str) and entry["digest"].startswith("sha256:")
                    and _digest(entry["digest"][7:]),
                    "MCP Skill index digest is invalid",
                )
            if entry["type"] == "skill-md" or "description" in entry:
                description = entry.get("description")
                _require(
                    _text(description) and bool(description.strip()) and len(description) <= 1024,
                    "MCP Skill index description is invalid",
                )
            selected.append(entry)
            uris[uri] = entry
        _require({entry["name"] for entry in selected} == self._expected.keys(), "MCP index is missing selected Skills")
        catalog = sorted(selected, key=lambda entry: entry["name"])
        _require(self._catalog is None or catalog == self._catalog, "Pinned MCP Skill discovery changed during runtime")
        self._catalog = catalog
        self._uris = uris
        return {"skills": selected}


class _ReadinessAgent(BaseAgent):
    def run(self, *args, **kwargs):
        raise SkillsRuntimeError("Skill readiness must never invoke a model")


class _InstructionsProvider(ContextProvider):
    def __init__(self, inner, manifest_path, project_endpoint, manifest_bytes, contents, descriptions):
        super().__init__("fam_skills")
        self._inner = inner
        self._manifest_path = manifest_path
        self._project_endpoint = project_endpoint
        self._manifest_bytes = manifest_bytes
        self._contents = contents
        self._descriptions = descriptions
        self._active = True

    def _check_artifact(self):
        _require(self._active, "FAM Skills runtime is closed")
        manifest, encoded = _manifest(self._manifest_path, self._project_endpoint)
        _require(encoded == self._manifest_bytes, "FAM Skills manifest changed after runtime startup")
        if manifest["mode"] == "bundle":
            contents, descriptions = _bundle(self._manifest_path.parent, manifest)
            _require(
                contents == self._contents and descriptions == self._descriptions,
                "FAM Skills instructions changed after runtime startup",
            )
        else:
            _mcp_artifacts(self._manifest_path.parent)

    async def _context(self, agent, session, state):
        self._check_artifact()
        staged = SessionContext(input_messages=[])
        await self._inner.before_run(agent=agent, session=session, context=staged, state=state)
        if not self._contents:
            _require(not staged.instructions and not staged.tools, "Detached Skills unexpectedly produced context")
            return staged, None
        _require(len(staged.instructions) == 1, "SDK did not advertise the expected Skill inventory")
        try:
            advertised = ET.fromstring(staged.instructions[0])
        except ET.ParseError:
            raise SkillsRuntimeError("SDK Skill inventory is malformed") from None
        discovered = [(skill.findtext("name"), skill.findtext("description")) for skill in advertised.findall("skill")]
        _require(
            len(discovered) == len(self._descriptions) and dict(discovered) == self._descriptions,
            "SDK discovery omitted, duplicated, or changed a required Skill",
        )
        _require(
            len(staged.tools) == 3
            and all(isinstance(tool, FunctionTool) for tool in staged.tools)
            and {tool.name for tool in staged.tools} == {"load_skill", "read_skill_resource", "run_skill_script"},
            "SDK Skills tool contract changed; refuse to expose an unqualified tool surface",
        )
        load = next(tool for tool in staged.tools if tool.name == "load_skill")
        for name, expected in self._contents.items():
            actual = await load.invoke(arguments={"skill_name": name}, skip_parsing=True)
            _require(actual == expected, "SDK instruction loading differs from the locked content")
        return staged, load

    async def qualify(self):
        agent = _ReadinessAgent(name="fam-skills-readiness")
        await self._context(agent, AgentSession(), {})

    async def before_run(self, *, agent, session, context, state):
        if self._contents:
            _require(
                not any(getattr(tool, "name", None) == "load_skill" for tool in context.tools),
                "FAM Skills load_skill conflicts with an existing application tool",
            )
        staged, load = await self._context(agent, session, state)
        if load is None:
            return

        async def load_skill(skill_name: str) -> str:
            self._check_artifact()
            _require(skill_name in self._contents, "Requested Skill is not in the locked inventory")
            actual = await load.invoke(arguments={"skill_name": skill_name}, skip_parsing=True)
            _require(actual == self._contents[skill_name], "SDK instruction loading differs from the locked content")
            return actual

        context.extend_instructions(self.source_id, staged.instructions)
        context.extend_tools(self.source_id, [FunctionTool(
            name="load_skill", description="Load the locked instructions for a selected Skill.",
            func=load_skill, approval_mode="never_require",
        )])

    def close(self):
        self._active = False


def _mcp_artifacts(root: Path):
    for path in root.iterdir():
        _require(
            path.name in {"manifest.json", ".ownership.json", "fam_skills_runtime.py"},
            "MCP Skills must not contain bundled scripts, resources, or instructions",
        )
        _regular(path)


def _toolbox_mcp_url(manifest, project_endpoint: str) -> str:
    _require(manifest["mode"] == "mcp", "MCP requires an explicit MCP manifest")
    endpoint = _endpoint(project_endpoint)
    _require(endpoint == _endpoint(manifest["projectEndpoint"]), "MCP project binding differs from its lock")
    return (
        endpoint + "/toolboxes/" + quote(manifest["toolboxName"], safe="")
        + "/versions/" + quote(manifest["toolboxVersion"], safe="")
        + "/mcp?api-version=v1"
    )


def _mcp_http_client(url: str, credential, *, transport=None):
    import httpx
    from anyio import to_thread

    target = httpx.URL(url)

    def require_target(request):
        _require(request.url == target, "MCP request left the pinned same-project Toolbox endpoint")

    class ProjectIdentityAuth(httpx.Auth):
        async def async_auth_flow(self, request):
            require_target(request)
            async with asyncio.timeout(_REQUEST_TIMEOUT):
                token = await to_thread.run_sync(
                    partial(credential.get_token, _TOKEN_SCOPE), abandon_on_cancel=True,
                )
            if inspect.iscoroutine(token):
                token.close()
            value = getattr(token, "token", None)
            expires = getattr(token, "expires_on", None)
            _require(
                isinstance(value, str) and bool(value) and value.isascii()
                and not any(c.isspace() or not c.isprintable() for c in value)
                and type(expires) is int and expires > time.time(),
                "MCP identity returned an empty, invalid, or expired access token",
            )
            request.headers["Authorization"] = "Bearer " + value
            yield request

    async def check_request(request):
        require_target(request)
        request.headers["Accept-Encoding"] = "identity"

    class BoundedResponseStream(httpx.AsyncByteStream):
        def __init__(self, stream, limit):
            self.stream = stream
            self.limit = limit

        async def __aiter__(self):
            total = 0
            async for chunk in self.stream:
                total += len(chunk)
                _require(total <= self.limit, "MCP response exceeded its transport safety guard")
                yield chunk

        async def aclose(self):
            await self.stream.aclose()

    async def check_response(response):
        _require(not 300 <= response.status_code < 400, "MCP redirects are not allowed")
        _require(
            response.headers.get("Content-Encoding", "identity").strip().lower() in {"", "identity"},
            "MCP encoded responses are not allowed",
        )
        limit = _MANIFEST_LIMIT if response.request.method == "DELETE" else _ARCHIVE_LIMIT
        length = response.headers.get("Content-Length")
        _require(
            length is None or (
                len(length) <= 20 and length.isascii() and length.isdecimal() and int(length) <= limit
            ),
            "MCP response Content-Length exceeds its transport safety guard or is invalid",
        )
        if response.is_stream_consumed:
            _require(len(response.content) <= limit, "MCP response exceeded its transport safety guard")
        else:
            response.stream = BoundedResponseStream(response.stream, limit)

    return httpx.AsyncClient(
        auth=ProjectIdentityAuth(),
        timeout=httpx.Timeout(_REQUEST_TIMEOUT, read=300.0),
        follow_redirects=False,
        event_hooks={"request": [check_request], "response": [check_response]},
        transport=transport,
    )


async def _terminate_mcp_session(client, url: str, streams, initialized):
    session_id = streams[2]()
    if not session_id:
        return
    headers = {
        "Accept": "application/json, text/event-stream",
        "Content-Type": "application/json",
        "Mcp-Session-Id": session_id,
    }
    if initialized is not None:
        headers["Mcp-Protocol-Version"] = initialized.protocolVersion
    async with client.stream("DELETE", url, headers=headers) as response:
        _require(
            response.status_code in {200, 204},
            f"MCP session termination was not acknowledged (HTTP {response.status_code})",
        )
        async for _ in response.aiter_bytes():
            pass


@asynccontextmanager
async def _mcp_session(url: str, credential):
    from mcp import ClientSession
    from mcp.client.streamable_http import streamable_http_client

    stack = AsyncExitStack()
    session_stack = AsyncExitStack()
    streams = None
    initialized = None
    try:
        async with asyncio.timeout(_STARTUP_TIMEOUT):
            client = await stack.enter_async_context(_mcp_http_client(url, credential))
            streams = await stack.enter_async_context(streamable_http_client(
                url, http_client=client, terminate_on_close=False,
            ))
            session = await session_stack.enter_async_context(ClientSession(
                streams[0], streams[1], read_timeout_seconds=timedelta(seconds=_REQUEST_TIMEOUT),
            ))
            initialized = await session.initialize()
            _require(
                initialized.capabilities.resources is not None,
                "Pinned Toolbox MCP server does not advertise resources",
            )
        yield session
    finally:
        # The SDK swallows DELETE failures. Terminate explicitly between local
        # session and transport disposal, without passing errors into SDK task groups.
        async with asyncio.timeout(_CLOSE_TIMEOUT):
            try:
                await session_stack.aclose()
            finally:
                try:
                    if streams is not None:
                        await _terminate_mcp_session(client, url, streams, initialized)
                finally:
                    await stack.aclose()


class _SessionProxy:
    def __init__(self, session, portal, owner_loop):
        self._session = session
        self._portal = portal
        self._owner_loop = owner_loop
        self._active = True

    async def _read(self, uri):
        _require(self._active, "MCP Skills session is closed")
        async with asyncio.timeout(_REQUEST_TIMEOUT):
            return await self._session.read_resource(uri)

    async def read_resource(self, uri):
        _require(self._active, "MCP Skills session is closed")
        if asyncio.get_running_loop() is self._owner_loop:
            return await self._read(uri)
        future = self._portal.start_task_soon(self._read, uri)
        return await asyncio.wrap_future(future)

    def close(self):
        self._active = False


async def _qualify_mcp(*, manifest_path, project_endpoint, session, manifest_bytes):
    async with asyncio.timeout(_STARTUP_TIMEOUT):
        return await _provider_from_mcp_session(
            manifest_path=manifest_path, project_endpoint=project_endpoint, session=session,
            expected_manifest=manifest_bytes,
        )


@contextmanager
def _open_mcp_runtime(*, manifest_path, project_endpoint, credential, manifest, manifest_bytes):
    from anyio.from_thread import start_blocking_portal

    _require(version("mcp") == MCP_VERSION, "FAM MCP Skills requires mcp==1.30.0, not MCP 2.x")
    _require(
        callable(getattr(credential, "get_token", None))
        and not inspect.iscoroutinefunction(credential.get_token),
        "MCP Skills requires a synchronous Azure identity credential; async credentials may belong to another loop",
    )
    _mcp_artifacts(manifest_path.parent)
    url = _toolbox_mcp_url(manifest, project_endpoint)
    with start_blocking_portal(backend="asyncio", name="fam-skills-mcp") as portal:
        with portal.wrap_async_context_manager(_mcp_session(url, credential)) as session:
            proxy = _SessionProxy(session, portal, portal.call(asyncio.get_running_loop))
            try:
                provider = portal.call(partial(
                    _qualify_mcp, manifest_path=manifest_path,
                    project_endpoint=project_endpoint, session=proxy, manifest_bytes=manifest_bytes,
                ))
                try:
                    yield provider
                finally:
                    provider.close()
            finally:
                proxy.close()


async def _provider_from_mcp_session(*, manifest_path, project_endpoint, session, expected_manifest=None):
    """Internal construction seam; the caller must own the live MCP session."""
    _require(version("agent-framework-core") == CORE_VERSION, "FAM Skills requires agent-framework-core==1.19.0")
    _require(version("mcp") == MCP_VERSION, "FAM MCP Skills requires mcp==1.30.0, not MCP 2.x")
    from agent_framework import MCPSkillsSource

    path = Path(manifest_path).absolute()
    manifest, encoded = _manifest(path, project_endpoint)
    _require(
        expected_manifest is None or encoded == expected_manifest,
        "FAM Skills manifest changed while opening the pinned MCP session",
    )
    _require(manifest["mode"] == "mcp", "An MCP session requires an MCP manifest")
    _mcp_artifacts(path.parent)
    resources = _MCPResources(session, manifest)
    await resources.prime()
    source = MCPSkillsSource(
        client=resources, archive_resource_extensions=(), archive_resource_search_depth=1,
        archive_max_file_count=1, archive_max_size_bytes=_ARCHIVE_LIMIT,
        archive_max_uncompressed_size_bytes=_SKILL_LIMIT,
    )
    inner = SkillsProvider(
        source, instruction_template=_PROMPT, disable_load_skill_approval=True,
        source_id="fam_skills",
    )
    provider = _InstructionsProvider(
        inner, path, project_endpoint, encoded,
        dict(resources.contents), dict(resources.descriptions),
    )
    try:
        await provider.qualify()
    except BaseException:
        provider.close()
        raise
    return provider


@contextmanager
def open_runtime(*, manifest_path, project_endpoint: str, credential) -> Iterator[ContextProvider]:
    """Validate and retain an instructions-only provider for the app-host lifetime.

    Bundle mode never uses credential, MCP, HTTP, or an Azure Skills API.
    An explicit empty skills array yields a no-op provider for detachment.
    MCP requires an app-owned synchronous Azure identity credential; its
    session and owning loop remain alive until this context exits.
    """
    _require(version("agent-framework-core") == CORE_VERSION, "FAM Skills requires agent-framework-core==1.19.0")
    path = Path(manifest_path).absolute()
    manifest, encoded = _manifest(path, project_endpoint)
    if manifest["mode"] == "mcp" and manifest["skills"]:
        with _open_mcp_runtime(
            manifest_path=path, project_endpoint=project_endpoint, credential=credential,
            manifest=manifest, manifest_bytes=encoded,
        ) as provider:
            yield provider
        return
    if manifest["mode"] == "bundle":
        contents, descriptions = _bundle(path.parent, manifest)
    else:
        _mcp_artifacts(path.parent)
        contents, descriptions = {}, {}
    inner = SkillsProvider.from_paths(
        [path.parent / entry["name"] for entry in manifest["skills"]],
        resource_extensions=(), script_extensions=(), search_depth=1,
        resource_filter=lambda *_: False, script_filter=lambda *_: False,
        script_runner=None, disable_caching=True, disable_load_skill_approval=True,
        instruction_template=_PROMPT, source_id="fam_skills",
    )
    provider = _InstructionsProvider(inner, path, project_endpoint, encoded, contents, descriptions)
    try:
        # A filesystem-only, uncached provider retains no event-loop resources.
        # The worker also permits callers that already have a running event loop.
        with ThreadPoolExecutor(max_workers=1) as executor:
            executor.submit(asyncio.run, provider.qualify()).result()
        yield provider
    finally:
        provider.close()
