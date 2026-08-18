package qa

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type calibrationCase struct {
	ID               string `json:"id"`
	Category         string `json:"category"`
	Query            string `json:"query"`
	Response         string `json:"response"`
	ExpectedBehavior string `json:"expected_behavior"`
	ValidationRules  any    `json:"validation_rules"`
	ExpectedLabel    string `json:"expected_label"`
	Rationale        string `json:"rationale"`
}

func TestSmokeCoreEvaluatorCalibrationFixture(t *testing.T) {
	path := filepath.Join("evaluator-calibration", "smoke-core.calibration.jsonl")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	counts := map[string]int{}
	ids := map[string]bool{}
	requirementsByQuery := map[string]struct {
		expectedBehavior string
		validationRules  any
	}{}
	total := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		total++
		var item calibrationCase
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			t.Fatalf("line %d: %v", total, err)
		}
		if item.ID == "" || item.Query == "" || item.Response == "" ||
			item.ExpectedBehavior == "" || item.ValidationRules == nil || item.Rationale == "" {
			t.Errorf("line %d has empty required fields: %#v", total, item)
		}
		if existing, ok := requirementsByQuery[item.Query]; ok {
			if existing.expectedBehavior != item.ExpectedBehavior ||
				!reflect.DeepEqual(existing.validationRules, item.ValidationRules) {
				t.Errorf("%s changes requirements for an existing query", item.ID)
			}
		} else {
			requirementsByQuery[item.Query] = struct {
				expectedBehavior string
				validationRules  any
			}{
				expectedBehavior: item.ExpectedBehavior,
				validationRules:  item.ValidationRules,
			}
		}
		if ids[item.ID] {
			t.Errorf("duplicate calibration id %q", item.ID)
		}
		ids[item.ID] = true
		switch item.Category {
		case "clear-good":
			if item.ExpectedLabel != "PASS" {
				t.Errorf("%s must expect PASS", item.ID)
			}
		case "clear-bad":
			if item.ExpectedLabel != "FAIL" {
				t.Errorf("%s must expect FAIL", item.ID)
			}
		case "borderline":
			if item.ExpectedLabel != "PASS" && item.ExpectedLabel != "FAIL" {
				t.Errorf("%s has invalid borderline label %q", item.ID, item.ExpectedLabel)
			}
		default:
			t.Errorf("%s has unknown category %q", item.ID, item.Category)
		}
		counts[item.Category]++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if total != 15 {
		t.Fatalf("expected 15 calibration cases, got %d", total)
	}
	for _, category := range []string{"clear-good", "clear-bad", "borderline"} {
		if counts[category] != 5 {
			t.Errorf("expected 5 %s cases, got %d", category, counts[category])
		}
	}
}

func TestEvaluatorCalibrationRunnerContract(t *testing.T) {
	script := repositoryFile(t, "scripts", "Test-EvaluatorCalibration.ps1")
	requireText(t, script,
		"MinimumRuns = 3",
		"MinimumBorderlineAccuracy = 0.8",
		`"PASS", "FAIL", "ERROR"`,
		"clearAccuracy",
		"borderlineAccuracy",
		"unstableCases",
		"stableAcrossRuns",
		"calibration-report.json",
	)

	liveScript := repositoryFile(t, "scripts", "Invoke-LiveEvaluatorCalibration.py")
	requireText(t, liveScript,
		"smoke-core-contract-v3",
		"smoke-core-requirements-v3",
		`"type": "label_model"`,
		"{{item.expected_behavior}}",
		"passed = contract_pass and requirement_pass",
		"limit=args.expected_case_count",
		`--purpose`,
		`--evaluation-name-prefix`,
	)

	contract := repositoryFile(t, "qa", "evaluator-calibration", "smoke_core_contract.py")
	requireText(t, contract,
		"word_count",
		"json_exact",
		"required_phrases",
		"forbidden_phrases",
	)

	prompt := repositoryFile(t, "qa", "evaluator-calibration", "smoke-core.requirement-prompt.txt")
	requireText(t, prompt,
		"Classify whether",
		"Explicit constraints are vetoes",
		"Do not average a critical failure",
	)

	acceptanceScript := repositoryFile(t, "scripts", "Invoke-LiveAgentAcceptance.ps1")
	requireText(t, acceptanceScript,
		`"smoke"`,
		`"--prompt"`,
		`--purpose "agent-acceptance"`,
		`"Test-AgentAcceptance.ps1"`,
		`"agent-responses.jsonl"`,
	)

	acceptanceGate := repositoryFile(t, "scripts", "Test-AgentAcceptance.ps1")
	requireText(t, acceptanceGate,
		"requiredCases = 15",
		"requiredPasses = 15",
		"errorsAllowed = 0",
		"agent-acceptance-report.json",
	)

	requirements := repositoryFile(t, "qa", "evaluator-calibration", "requirements.txt")
	requireText(t, requirements,
		"azure-ai-projects==2.3.0",
		"azure-identity==1.25.3",
		"openai==2.45.0",
	)
}
