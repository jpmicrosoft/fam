import argparse
import json
from pathlib import Path

from smoke_core_contract import evaluate_contract


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--fixture", type=Path, required=True)
    args = parser.parse_args()

    failures = []
    cases = 0
    with args.fixture.open(encoding="utf-8-sig") as handle:
        for line_number, line in enumerate(handle, start=1):
            if not line.strip():
                continue
            cases += 1
            item = json.loads(line)
            actual = evaluate_contract(item["response"], item["validation_rules"])
            expected = item["expected_label"] == "PASS"
            if actual != expected:
                failures.append(
                    {
                        "line": line_number,
                        "id": item["id"],
                        "expected": expected,
                        "actual": actual,
                    }
                )

    if failures:
        raise SystemExit(json.dumps({"passed": False, "failures": failures}, indent=2))
    print(json.dumps({"passed": True, "cases": cases}))


if __name__ == "__main__":
    main()
