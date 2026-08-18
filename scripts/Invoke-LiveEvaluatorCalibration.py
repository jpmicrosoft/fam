import argparse
import hashlib
import importlib.util
import json
import time
from datetime import datetime, timezone
from pathlib import Path

from azure.ai.projects import AIProjectClient
from azure.identity import AzureCliCredential
from openai.types.eval_create_params import DataSourceConfigCustom
from openai.types.evals.create_eval_jsonl_run_data_source_param import (
    CreateEvalJSONLRunDataSourceParam,
    SourceFileContent,
    SourceFileContentContent,
)


TERMINAL_STATUSES = {"completed", "failed", "cancelled"}


def serialize(value):
    if hasattr(value, "model_dump"):
        return value.model_dump(mode="json")
    if hasattr(value, "as_dict"):
        return value.as_dict()
    if hasattr(value, "to_dict"):
        return value.to_dict()
    return value


def load_jsonl(path):
    items = []
    with path.open(encoding="utf-8-sig") as handle:
        for line_number, line in enumerate(handle, start=1):
            if not line.strip():
                continue
            try:
                items.append(json.loads(line))
            except json.JSONDecodeError as exc:
                raise ValueError(f"{path}:{line_number}: {exc}") from exc
    return items


def definition_hash(definition):
    encoded = json.dumps(definition, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(encoded).hexdigest()


def get_case_id(output_item):
    datasource_item = output_item.get("datasource_item") or {}
    item = datasource_item.get("item", datasource_item)
    return str(item.get("id", ""))


def result_reason(result):
    if result.get("reason"):
        return str(result["reason"])
    sample = result.get("sample")
    if isinstance(sample, dict):
        for key in ("reason", "output_text", "response"):
            if sample.get(key):
                return str(sample[key])
    return ""


def load_contract_evaluator(path):
    spec = importlib.util.spec_from_file_location("smoke_core_contract", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load deterministic evaluator from {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.evaluate_contract


def to_calibration_result(
    output_item,
    contract_name,
    requirement_name,
    contract_results,
):
    case_id = get_case_id(output_item)
    status = str(output_item.get("status", "")).lower()
    sample = output_item.get("sample") or {}
    error = sample.get("error") if isinstance(sample, dict) else None
    results = {
        result.get("name"): result
        for result in output_item.get("results") or []
        if result.get("name")
    }

    missing = [] if requirement_name in results else [requirement_name]
    if status != "completed" or error or missing:
        details = []
        if status != "completed":
            details.append(f"status={status or 'unknown'}")
        if error:
            details.append(f"error={json.dumps(error, default=str)}")
        if missing:
            details.append(f"missing evaluators={','.join(missing)}")
        return {
            "id": case_id,
            "actual_label": "ERROR",
            "reason": "; ".join(details),
        }

    contract_pass = contract_results.get(case_id)
    if contract_pass is None:
        return {
            "id": case_id,
            "actual_label": "ERROR",
            "reason": "No deterministic contract result was produced.",
        }

    requirement = results[requirement_name]
    requirement_pass = requirement.get("passed") is True
    passed = contract_pass and requirement_pass
    requirement_reason = result_reason(requirement)
    criteria = {
        contract_name: {
            "passed": contract_pass,
            "score": 1.0 if contract_pass else 0.0,
            "reason": "Local deterministic requirement gates.",
        },
        requirement_name: {
            "passed": requirement_pass,
            "score": requirement.get("score"),
            "reason": requirement_reason,
        },
    }
    reasons = [
        f"{contract_name}={'PASS' if contract_pass else 'FAIL'}: Local deterministic requirement gates.",
        f"{requirement_name}={'PASS' if requirement_pass else 'FAIL'}: {requirement_reason}",
    ]

    return {
        "id": case_id,
        "actual_label": "PASS" if passed else "FAIL",
        "reason": " | ".join(reasons),
        "criteria": criteria,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--endpoint", required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--fixture", type=Path, required=True)
    parser.add_argument("--contract-code", type=Path, required=True)
    parser.add_argument("--requirement-prompt", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--runs", type=int, default=3)
    parser.add_argument("--expected-case-count", type=int, default=15)
    parser.add_argument("--poll-seconds", type=int, default=10)
    parser.add_argument("--timeout-seconds", type=int, default=1800)
    parser.add_argument("--contract-name", default="smoke-core-contract-v3")
    parser.add_argument("--requirement-name", default="smoke-core-requirements-v3")
    parser.add_argument("--purpose", default="evaluator-calibration")
    parser.add_argument(
        "--evaluation-name-prefix",
        default="smoke-core-v3-calibration",
    )
    args = parser.parse_args()

    fixture = load_jsonl(args.fixture)
    if len(fixture) != args.expected_case_count:
        raise ValueError(
            f"Expected {args.expected_case_count} cases, got {len(fixture)}"
        )

    args.output_dir.mkdir(parents=True, exist_ok=True)
    project_client = AIProjectClient(
        endpoint=args.endpoint,
        credential=AzureCliCredential(),
    )
    openai_client = project_client.get_openai_client()

    evaluate_contract = load_contract_evaluator(args.contract_code)
    contract_results = {
        item["id"]: bool(evaluate_contract(item["response"], item["validation_rules"]))
        for item in fixture
    }
    contract_version = definition_hash(
        {
            "code": args.contract_code.read_text(encoding="utf-8"),
            "rules": {
                item["id"]: item["validation_rules"]
                for item in fixture
            },
        }
    )
    requirement_prompt = args.requirement_prompt.read_text(encoding="utf-8").strip()

    requirement_criterion = {
        "type": "label_model",
        "name": args.requirement_name,
        "model": args.model,
        "input": [
            {
                "role": "developer",
                "content": requirement_prompt,
            },
            {
                "role": "user",
                "content": (
                    "Query:\n{{item.query}}\n\n"
                    "Expected behavior:\n{{item.expected_behavior}}\n\n"
                    "Response:\n{{item.response}}"
                ),
            },
        ],
        "labels": ["PASS", "FAIL"],
        "passing_labels": ["PASS"],
    }
    testing_criteria = [requirement_criterion]

    item_schema = {
        "type": "object",
        "properties": {
            "id": {"type": "string"},
            "query": {"type": "string"},
            "response": {"type": "string"},
            "expected_behavior": {"type": "string"},
        },
        "required": [
            "id",
            "query",
            "response",
            "expected_behavior",
        ],
    }
    evaluation = openai_client.evals.create(
        name=(
            f"{args.evaluation_name_prefix}-"
            f"{datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')}"
        ),
        data_source_config=DataSourceConfigCustom(
            type="custom",
            item_schema=item_schema,
            include_sample_schema=True,
        ),
        testing_criteria=testing_criteria,
        metadata={"purpose": args.purpose, "design": "hybrid-v3"},
    )

    metadata = {
        "endpoint": args.endpoint,
        "model": args.model,
        "eval_id": evaluation.id,
        "created_at": datetime.now(timezone.utc).isoformat(),
        "evaluators": [
            {
                "name": args.contract_name,
                "version": f"local-{contract_version[:12]}",
            },
            {
                "name": args.requirement_name,
                "version": f"label-model-{definition_hash(requirement_criterion)[:12]}",
            },
        ],
        "runs": [],
    }
    source_items = []
    for item in fixture:
        source_items.append(
            {
                "id": item["id"],
                "query": item["query"],
                "response": item["response"],
                "expected_behavior": item["expected_behavior"],
            }
        )

    source = SourceFileContent(
        type="file_content",
        content=[SourceFileContentContent(item=item) for item in source_items],
    )

    for run_number in range(1, args.runs + 1):
        run = openai_client.evals.runs.create(
            eval_id=evaluation.id,
            name=f"hybrid-calibration-run-{run_number}",
            data_source=CreateEvalJSONLRunDataSourceParam(
                type="jsonl",
                source=source,
            ),
        )
        started = time.monotonic()
        while str(run.status).lower() not in TERMINAL_STATUSES:
            if time.monotonic() - started > args.timeout_seconds:
                raise TimeoutError(f"Evaluation run {run.id} exceeded {args.timeout_seconds} seconds")
            time.sleep(args.poll_seconds)
            run = openai_client.evals.runs.retrieve(
                eval_id=evaluation.id,
                run_id=run.id,
            )

        output_items = [
            serialize(item)
            for item in openai_client.evals.runs.output_items.list(
                eval_id=evaluation.id,
                run_id=run.id,
                limit=args.expected_case_count,
            )
        ]
        raw_path = args.output_dir / f"raw-run-{run_number}.json"
        raw_path.write_text(json.dumps(output_items, indent=2, default=str), encoding="utf-8")

        by_id = {
            item["id"]: item
            for item in (
                to_calibration_result(
                    output_item,
                    args.contract_name,
                    args.requirement_name,
                    contract_results,
                )
                for output_item in output_items
            )
            if item["id"]
        }
        result_path = args.output_dir / f"run-{run_number}.jsonl"
        with result_path.open("w", encoding="utf-8") as handle:
            for fixture_item in fixture:
                case_id = fixture_item["id"]
                result = by_id.get(
                    case_id,
                    {
                        "id": case_id,
                        "actual_label": "ERROR",
                        "reason": "No output item returned for this calibration case.",
                    },
                )
                handle.write(json.dumps(result, separators=(",", ":")) + "\n")

        metadata["runs"].append(
            {
                "number": run_number,
                "run_id": run.id,
                "status": str(run.status),
                "error": serialize(getattr(run, "error", None)),
                "report_url": getattr(run, "report_url", None),
                "result_counts": serialize(getattr(run, "result_counts", None)),
                "output_items": len(output_items),
                "result_file": str(result_path),
                "raw_file": str(raw_path),
            }
        )
        print(
            json.dumps(
                {
                    "run": run_number,
                    "run_id": run.id,
                    "status": str(run.status),
                    "output_items": len(output_items),
                }
            ),
            flush=True,
        )

    metadata_path = args.output_dir / "live-evaluation.json"
    metadata_path.write_text(json.dumps(metadata, indent=2, default=str), encoding="utf-8")
    print(json.dumps(metadata, indent=2, default=str))


if __name__ == "__main__":
    main()
