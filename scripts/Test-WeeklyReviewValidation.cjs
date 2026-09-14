"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { execFileSync } = require("node:child_process");
const { createRequire } = require("node:module");

const root = path.resolve(__dirname, "..");
const workflowFile = path.join(
  root,
  ".github",
  "workflows",
  "weekly-foundry-capability-review.lock.yml",
);
const setupAction = "jpmicrosoft/gh-aw/actions/setup";

function pinnedRuntime() {
  const firstLine = fs.readFileSync(workflowFile, "utf8").split(/\r?\n/, 1)[0];
  const prefix = "# gh-aw-metadata: ";
  assert.ok(
    firstLine.startsWith(prefix),
    "Compiled workflow metadata is missing",
  );
  const metadata = JSON.parse(firstLine.slice(prefix.length));
  assert.equal(
    metadata.strict,
    true,
    "Weekly review must retain strict compilation",
  );
  assert.match(
    metadata.compiler_version,
    /^[0-9a-f]{40}$/,
    "Compiler must be pinned by full revision",
  );
  const lock = JSON.parse(
    fs.readFileSync(
      path.join(root, ".github", "aw", "actions-lock.json"),
      "utf8",
    ),
  );
  const entry = lock.entries[`${setupAction}@${metadata.compiler_version}`];
  assert.ok(entry, "Matching setup runtime is missing from the action lock");
  assert.equal(entry.repo, setupAction);
  assert.equal(entry.version, metadata.compiler_version);
  assert.equal(entry.sha, metadata.compiler_version);
  return entry.sha;
}

function command(directory, executable, args) {
  return execFileSync(executable, args, {
    cwd: directory,
    encoding: "utf8",
    timeout: 30_000,
    maxBuffer: 1024 * 1024,
    stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env, GOTOOLCHAIN: "local", GOWORK: "off", GOENV: "off" },
  }).trim();
}

async function validate(runtimePath) {
  const revision = pinnedRuntime();
  const runtimeRoot = fs.realpathSync(runtimePath);
  const relative = path.relative(root, runtimeRoot);
  assert.ok(
    relative === ".." ||
      relative.startsWith(`..${path.sep}`) ||
      path.isAbsolute(relative),
    "Runtime dependencies must stay outside the FAM checkout",
  );
  assert.equal(
    command(runtimeRoot, "git", ["rev-parse", "HEAD"]),
    revision,
    "Runtime source does not match the compiled pin",
  );
  assert.equal(
    command(runtimeRoot, "git", ["status", "--porcelain"]),
    "",
    "Runtime source must be clean",
  );
  assert.equal(
    command(root, "git", ["status", "--porcelain"]),
    "",
    "FAM candidate must be clean",
  );

  const load = createRequire(
    path.join(
      runtimeRoot,
      "actions",
      "setup",
      "js",
      "copilot_sdk_repo_tools.cjs",
    ),
  );
  const { defineTool } = load("@github/copilot-sdk");
  const { parseCopilotSDKToolConfig } = load("./copilot_sdk_tool_config.cjs");
  const { createCopilotSDKRepositoryRuntime } = load(
    "./copilot_sdk_repo_tools.cjs",
  );
  const prefix = "GH_AW_COPILOT_SDK_TOOL_CONFIG: '";
  const lines = fs
    .readFileSync(workflowFile, "utf8")
    .split(/\r?\n/)
    .map((line) => line.trim());
  const configs = lines.filter((line) => line.startsWith(prefix));
  assert.equal(
    configs.length,
    1,
    "Expected exactly one compiled repository tool configuration",
  );
  assert.ok(
    configs[0].endsWith("'"),
    "Unsupported compiled tool configuration quoting",
  );
  const config = JSON.parse(
    configs[0].slice(prefix.length, -1).replaceAll("''", "'"),
  );
  config.profile.repositoryDefaultBranch = "main";
  const profile = parseCopilotSDKToolConfig(JSON.stringify(config)).profile;
  assert.ok(profile, "Compiled Go repository profile is missing");
  const source = command(root, "git", ["rev-parse", "HEAD"]);
  const go = JSON.parse(
    command(root, "go", ["env", "-json", "GOROOT", "GOCACHE", "GOMODCACHE"]),
  );
  const runtime = createCopilotSDKRepositoryRuntime(defineTool, profile, {
    env: {
      ...process.env,
      ...go,
      GITHUB_WORKSPACE: root,
      GITHUB_REPOSITORY: "jpmicrosoft/fam",
      GITHUB_SHA: source,
    },
  });
  const timer = setTimeout(() => runtime.abort(), 300_000);
  const failures = [];
  try {
    await runtime.initialize();
    // Exercise the real fixed validation path without creating an inference session.
    const value = await runtime.tool.handler(
      { action: "validate" },
      {
        sessionId: "no-inference-ci",
        toolCallId: "validate-fam",
        toolName: "go_repository",
        arguments: { action: "validate" },
      },
    );
    if (typeof value !== "string") {
      assert.ok(
        value && value.resultType === "failure",
        "Unexpected repository tool result",
      );
      throw new Error(
        value.error ||
          value.textResultForLlm ||
          "Repository validation failed without diagnostics",
      );
    }
    const result = JSON.parse(value);
    assert.equal(result.action, "validate");
    for (const phase of ["test", "vet", "build"])
      assert.equal(result[phase].exitCode, 0, `${phase} failed`);
    console.log(
      JSON.stringify({
        source,
        runtime: revision,
        validation: "passed",
        projectedTree: "unchanged",
        inference: "not invoked",
      }),
    );
  } catch (error) {
    failures.push(error);
  } finally {
    clearTimeout(timer);
    try {
      await runtime.close();
    } catch (error) {
      failures.push(error);
    }
  }
  if (failures.length)
    throw new AggregateError(failures, "Weekly repository validation failed");
}

async function main() {
  const [mode, runtimePath, ...extra] = process.argv.slice(2);
  assert.equal(extra.length, 0, "Too many arguments");
  if (mode === "pin" && !runtimePath) {
    console.log(pinnedRuntime());
  } else if (mode === "validate" && runtimePath) {
    await validate(path.resolve(runtimePath));
  } else {
    throw new Error(
      "Usage: node scripts/Test-WeeklyReviewValidation.cjs pin | validate <clean-gh-aw-checkout>",
    );
  }
}

main().catch((error) => {
  const errors = error instanceof AggregateError ? error.errors : [error];
  console.error(
    JSON.stringify({
      validation: "failed",
      errors: errors.map((value) =>
        value instanceof Error ? value.message : "Non-Error validation failure",
      ),
    }),
  );
  process.exitCode = 1;
});
