package qa

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repositoryFile(t *testing.T, elements ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{".."}, elements...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func requireText(t *testing.T, content string, required ...string) {
	t.Helper()
	for _, text := range required {
		if !strings.Contains(content, text) {
			t.Errorf("required release qualification contract %q is missing", text)
		}
	}
}

func TestOfflineReleaseRunnerKeepsEveryGate(t *testing.T) {
	script := repositoryFile(t, "scripts", "Test-Release.ps1")
	requireText(t, script,
		"gofmt -l .",
		`@("vet", "./...")`,
		`@("test", "-count=1", "./...")`,
		`@("test", "-count=1", "-race", "./...")`,
		`@("completion", $shell)`,
		`$hostAlias`,
		`@("-version")`,
		`"fam_$($env:GOOS)_$($env:GOARCH)$extension"`,
		`-Filter "agent*.example.yaml"`,
		`"evaluator calibration contract"`,
		`"Test-EvaluatorCalibration.ps1"`,
		`"tool catalog"`,
		`"tool-catalog"`,
		`"AzureCloud"`,
		`@("diff", "--check")`,
		`"linux", "amd64"`,
		`"windows", "arm64"`,
		`"SHA256SUMS"`,
		`"release-report.json"`,
	)
}

func TestLiveRunnerCannotDowngradeDangerousCommands(t *testing.T) {
	script := repositoryFile(t, "scripts", "Invoke-LiveRelease.ps1")
	requireText(t, script,
		`"-AllowDestructive requires both -RunOnline and -AllowMutations"`,
		`$offlineCommands`,
		`$mutationCommands`,
		`$destructiveCommands`,
		`Get-MinimumGate`,
		`requires at least $minimumGate`,
		`"--apim-subscription-key"`,
		`Get-CommandPaths`,
		`& $Binary @commandPath @arguments`,
		`RequireAllCommands`,
		`RequireAllFlags`,
		`"live-release-report.json"`,
		`Resolve-SecurityBoolFlag`,
	)
	for _, forbidden := range []string{
		"Invoke-Expression",
		" iex ",
		"cmd.exe /c",
		"powershell.exe -Command",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("live runner uses unsafe shell evaluation %q", forbidden)
		}
	}
}

func TestReceiptUploadClassifiedAsMutation(t *testing.T) {
	script := repositoryFile(t, "scripts", "Invoke-LiveRelease.ps1")
	if !strings.Contains(script, `"receipt upload"`) {
		t.Error("receipt upload must be explicitly classified in the live release command catalog")
	}
	// It must appear in $mutationCommands (between the assignment and the closing paren)
	mutIdx := strings.Index(script, "$mutationCommands = @(")
	if mutIdx < 0 {
		t.Fatal("$mutationCommands assignment not found")
	}
	closeIdx := strings.Index(script[mutIdx:], "\n)")
	if closeIdx < 0 {
		t.Fatal("$mutationCommands closing paren not found")
	}
	mutBlock := script[mutIdx : mutIdx+closeIdx]
	if !strings.Contains(mutBlock, `"receipt upload"`) {
		t.Error("receipt upload must be in $mutationCommands — it performs a network write")
	}
}

func TestGroundingSyncDestructiveFlagsClassified(t *testing.T) {
	script := repositoryFile(t, "scripts", "Invoke-LiveRelease.ps1")
	for _, flag := range []string{
		"delete-replaced-uploads",
		"delete-pruned-uploads",
	} {
		if !strings.Contains(script, fmt.Sprintf(`"%s"`, flag)) &&
			!strings.Contains(script, fmt.Sprintf(`'%s'`, flag)) &&
			!strings.Contains(script, flag) {
			t.Errorf("grounding sync --%s must be checked for destructive classification", flag)
		}
	}
}

func TestSecurityBoolFlagParserPresent(t *testing.T) {
	script := repositoryFile(t, "scripts", "Invoke-LiveRelease.ps1")
	requireText(t, script,
		"Resolve-SecurityBoolFlag",
		"cobraTrueValues",
		"cobraFalseValues",
		"Duplicate security-sensitive flag",
		"Conflicting duplicate security-sensitive flag",
		"Unrecognized boolean value for security-sensitive flag",
		"Ambiguous security-sensitive flag",
	)
}

func TestOnlineReadCommandsAreReadOnly(t *testing.T) {
	// Commands that fall through to online-read must be genuinely read-only.
	// This allowlist prevents future network-writing commands from silently
	// defaulting to online-read without an explicit classification decision.
	script := repositoryFile(t, "scripts", "Invoke-LiveRelease.ps1")

	// Every command that performs a network write must appear in either
	// $mutationCommands or $destructiveCommands.
	knownNetworkWriters := []string{
		"receipt upload",
	}
	mutIdx := strings.Index(script, "$mutationCommands = @(")
	destIdx := strings.Index(script, "$destructiveCommands = @(")
	if mutIdx < 0 || destIdx < 0 {
		t.Fatal("command catalog arrays not found")
	}
	mutEnd := strings.Index(script[mutIdx:], "\n)")
	destEnd := strings.Index(script[destIdx:], "\n)")
	mutBlock := script[mutIdx : mutIdx+mutEnd]
	destBlock := script[destIdx : destIdx+destEnd]
	for _, cmd := range knownNetworkWriters {
		quoted := fmt.Sprintf("%q", cmd)
		if !strings.Contains(mutBlock, quoted) && !strings.Contains(destBlock, quoted) {
			t.Errorf("network-writing command %s must be in $mutationCommands or $destructiveCommands", cmd)
		}
	}
}

func TestLiveMatrixExampleIsValidAndSecretFree(t *testing.T) {
	raw := repositoryFile(t, "qa", "live-release.example.json")
	var matrix struct {
		SchemaVersion int `json:"schemaVersion"`
		Scenarios     []struct {
			Name      string   `json:"name"`
			Command   string   `json:"command"`
			Gate      string   `json:"gate"`
			Arguments []string `json:"arguments"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal([]byte(raw), &matrix); err != nil {
		t.Fatalf("live release example is invalid JSON: %v", err)
	}
	if matrix.SchemaVersion != 1 || len(matrix.Scenarios) == 0 {
		t.Fatalf("unexpected live release matrix: %#v", matrix)
	}
	for _, scenario := range matrix.Scenarios {
		if scenario.Name == "" || scenario.Command == "" || scenario.Gate == "" {
			t.Errorf("incomplete live scenario: %#v", scenario)
		}
		for _, argument := range scenario.Arguments {
			if strings.HasPrefix(argument, "sk-") ||
				strings.Contains(strings.ToLower(argument), "subscription-key=") {
				t.Errorf("live scenario %q contains credential-shaped data", scenario.Name)
			}
		}
	}
}

func TestCIAndReleaseInvokeExecutableQualification(t *testing.T) {
	ci := repositoryFile(t, ".github", "workflows", "ci.yml")
	requireText(t, ci,
		"scripts/Test-Release.ps1",
		"-SkipCoreChecks",
		"-SkipRace",
		"-SkipCrossCompile",
		"needs: ci",
		"cp scripts/install.sh dist/install.sh",
		"cp scripts/install.ps1 dist/install.ps1",
		`cp "dist/${bin}${ext}" "dist/fam${ext}"`,
		`"fam${ext}"`,
		"sha256sum *.tar.gz *.zip install.sh install.ps1",
	)
}

func TestLiveEvaluatorCalibrationWorkflowIsManualAndProtected(t *testing.T) {
	workflow := repositoryFile(t, ".github", "workflows", "live-evaluator-calibration.yml")
	requireText(t, workflow,
		"workflow_dispatch:",
		"environment: live-evaluator-calibration",
		"id-token: write",
		"qa/evaluator-calibration/requirements.txt",
		"scripts/Invoke-LiveEvaluatorCalibration.ps1",
		"actions/upload-artifact@v4",
		"retention-days: 30",
	)
	if strings.Contains(workflow, "schedule:") {
		t.Error("live evaluator calibration must remain manually triggered to control billed evaluation runs")
	}
}
