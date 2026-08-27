# Evaluator calibration

This calibration validates whether an evaluator can reliably distinguish
clearly good, clearly bad, and deliberately borderline responses before its
score is used to optimize an agent.

## Files

- `smoke-core.calibration.jsonl`: 15 human-labeled cases: five clear-good,
  five clear-bad, and five borderline. Every row also includes the same
  `expected_behavior` and `validation_rules` for every repeated query.
- `smoke_core_contract.py`: deterministic hard gates for exact requirements
  such as word counts, exact JSON, known text, required content, and forbidden
  content.
- `test_smoke_core_contract.py`: local fixture-to-contract self-check kept out
  of the sandboxed evaluator source.
- `smoke-core.requirement-prompt.txt`: strict binary Foundry judge for factual,
  safety, and requirement adherence that cannot be reduced to exact checks.
- `results.example.jsonl`: a correctly shaped example evaluator result file.
- `scripts/Test-EvaluatorCalibration.ps1`: validates result completeness,
  accuracy, errors, and classification stability across repeated runs.
- `scripts/Invoke-LiveEvaluatorCalibration.ps1`: configures a native Foundry
  `label_model` grader, executes three live runs, combines it with the local
  deterministic verdict, and
  invokes the calibration gate.
- `scripts/Invoke-LiveAgentAcceptance.ps1`: invokes a deployed sample agent for
  all 15 requirements and requires every real response to pass the calibrated
  hybrid evaluator.
- `scripts/Test-AgentAcceptance.ps1`: enforces the 15/15, zero-error product
  acceptance threshold.
- `requirements.txt`: exact Python SDK versions used by local and CI live
  qualification.

The hybrid result is `PASS` only when both evaluators pass:

1. The deterministic contract gate validates every objective requirement.
2. The requirement judge evaluates the row-specific expected behavior.

Neither evaluator uses `expected_label` or `rationale`; those fields remain
human-owned gold labels used only by the final agreement gate.

Each real result file must contain one JSON object per calibration case:

```json
{"id":"clear-good-01","actual_label":"PASS","reason":"Meets the constraint."}
```

`actual_label` must be `PASS`, `FAIL`, or `ERROR`. A boolean or string `result`
field is also accepted for direct adapters from evaluators that emit
`result`/`reason`.

## Run

Evaluate the same 15 fixed responses at least three times, save each run in the
result format above, then run:

```powershell
.\scripts\Test-EvaluatorCalibration.ps1 `
  -Results @(
    "path\to\run-1.jsonl",
    "path\to\run-2.jsonl",
    "path\to\run-3.jsonl"
  )
```

The calibration passes only when:

- every run classifies all ten clear cases correctly;
- every run classifies at least four of five borderline cases correctly;
- no case returns `ERROR`; and
- every case receives the same classification across repeated runs.

Use `results.example.jsonl` only to validate the local runner:

```powershell
.\scripts\Test-EvaluatorCalibration.ps1 `
  -Results qa\evaluator-calibration\results.example.jsonl `
  -MinimumRuns 1
```

Do not tune an agent against an evaluator that fails this calibration. Review
the evaluator rubric, resolve contradictions, and repeat the same fixed cases
until the result is stable.

## Live Foundry calibration

The live workflow requires an authenticated Azure CLI plus the pinned Python
packages:

```powershell
python -m pip install -r qa\evaluator-calibration\requirements.txt
```

Run it against a disposable Foundry project that contains the selected model
deployment:

```powershell
.\scripts\Invoke-LiveEvaluatorCalibration.ps1 `
  -ProjectEndpoint "https://<account>.services.ai.azure.com/api/projects/<project>" `
  -Model "gpt-5-mini" `
  -OutputDirectory ".\.release-qualification\evaluator-calibration\live"
```

The command runs the versioned local `smoke-core-contract-v3` gate, configures
the `smoke-core-requirements-v3` Foundry `label_model` grader, executes the
fixed dataset three times, and fails unless the existing accuracy and
stability thresholds are met.

## Sample-agent acceptance

Calibration proves that the judge is reliable. It does not prove that an agent
meets the requirements. After calibration succeeds, run the separate
acceptance gate against a deployed disposable agent:

```powershell
.\scripts\Invoke-LiveAgentAcceptance.ps1 `
  -Manifest "path\to\disposable-agent.yaml" `
  -ProjectEndpoint "https://<account>.services.ai.azure.com/api/projects/<project>" `
  -Model "gpt-5-mini"
```

The runner invokes the stable agent endpoint once for each of the 15
requirements, sends only each query, real response, and expected behavior to
the Foundry judge, and combines that verdict with the deterministic contract
gate. All 15 cases must pass and no evaluator error is allowed.

## Product boundary

These scripts remain release tooling rather than a Foundry Agent Manager
command. They depend on Python SDKs, consume billed evaluation capacity, and
use a fixed maintainer-owned gold set. The supported CLI remains a
single-binary deployment and lifecycle manager. A future user-facing
evaluation command should be designed separately around user-owned datasets,
evaluators, thresholds, receipts, and cost controls rather than exposing this
internal release gate.
