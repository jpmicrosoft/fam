package hostedskills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAbsentAndSelectedDeclaration(t *testing.T) {
	root := t.TempDir()
	config, err := Load(root, "agent")
	if err != nil || config != nil {
		t.Fatalf("absent sidecar: config=%#v err=%v", config, err)
	}
	writeTestFile(t, root, FileName, `apiVersion: foundry-agent-manager/skills/v1
services:
  agent:
    mode: bundle
    language: python
    skills:
      - path: skills/greeting
  other:
    mode: mcp
    language: dotnet
    skills: []
`)
	config, err = Load(root, "agent")
	if err != nil || config == nil || config.Skills[0].Path != "skills/greeting" {
		t.Fatalf("selected declaration: config=%#v err=%v", config, err)
	}
	config, err = Load(root, "missing")
	if err != nil || config != nil {
		t.Fatalf("absent service: config=%#v err=%v", config, err)
	}
	config, err = Load(root, "other")
	if err != nil || config == nil || len(config.Skills) != 0 {
		t.Fatalf("explicit detachment: config=%#v err=%v", config, err)
	}
}

func TestParseRejectsInvalidDeclarations(t *testing.T) {
	base := `apiVersion: foundry-agent-manager/skills/v1
services:
  agent:
    mode: bundle
    language: python
    skills:
      - name: greeting
        version: "1"
`
	tests := map[string]string{
		"unknown root":          base + "unexpected: true\n",
		"unknown service field": base + "    instructions: ignored\n",
		"missing version":       strings.Replace(base, `        version: "1"`+"\n", "", 1),
		"numeric version":       strings.Replace(base, `version: "1"`, "version: 1", 1),
		"moving version":        strings.Replace(base, `version: "1"`, `version: "latest"`, 1),
		"case alias":            strings.Replace(base, `version: "1"`, `version: "DEFAULT"`, 1),
		"unknown language":      strings.Replace(base, "python", "javascript", 1),
		"unknown mode":          strings.Replace(base, "bundle", "inline", 1),
		"unknown api":           strings.Replace(base, "skills/v1", "skills/v2", 1),
		"duplicate key":         base + "    mode: mcp\n",
		"multiple documents":    base + "---\nservices: {}\n",
		"mcp without toolbox":   strings.Replace(base, "bundle", "mcp", 1),
		"bundle toolbox":        base + "    toolbox: {name: operations, version: \"1\"}\n",
		"mixed source":          base + "        path: skills/greeting\n",
		"duplicate identity":    base + "      - name: greeting\n        version: \"2\"\n",
		"traversal":             strings.Replace(base, "name: greeting\n        version: \"1\"", "path: ../greeting", 1),
		"absolute":              strings.Replace(base, "name: greeting\n        version: \"1\"", "path: C:/skills/greeting", 1),
		"empty document":        "",
		"null skills":           strings.Replace(base, "skills:\n      - name: greeting\n        version: \"1\"", "skills: null", 1),
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateDocument([]byte(content)); err == nil {
				t.Fatal("invalid document was accepted")
			}
			if _, err := Parse([]byte(content)); err == nil {
				t.Fatal("Parse accepted an invalid document")
			}
			for _, service := range []string{"agent", "missing"} {
				if _, err := ParseService([]byte(content), service); err == nil {
					t.Fatalf("invalid document was accepted when selecting %q", service)
				}
			}
		})
	}
}

func TestDeclarationHashTracksOnlySelectedSemantics(t *testing.T) {
	first, err := ValidateDocument([]byte(`apiVersion: foundry-agent-manager/skills/v1
services:
  agent: {mode: bundle, language: python, skills: []}
`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ValidateDocument([]byte(`# comments are not artifact identity
services:
  other: {mode: bundle, language: dotnet, skills: []}
  agent:
    skills: []
    language: python
    mode: bundle
apiVersion: foundry-agent-manager/skills/v1
`))
	if err != nil {
		t.Fatal(err)
	}
	a, err := DeclarationHash("agent", first.Services["agent"])
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeclarationHash("agent", second.Services["agent"])
	if err != nil || a != b {
		t.Fatalf("formatting/unrelated service changed identity: %s %s %v", a, b, err)
	}
	changed := first.Services["agent"]
	changed.Language = "dotnet"
	c, err := DeclarationHash("agent", changed)
	if err != nil || a == c {
		t.Fatalf("language change omitted from identity: %s %s %v", a, c, err)
	}
}

func TestLoadRejectsSidecarSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(target, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, FileName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Load(root, "agent"); err == nil {
		t.Fatal("sidecar symlink accepted")
	}
}

func TestParseAndParseServiceDoNotReadSources(t *testing.T) {
	const input = `apiVersion: foundry-agent-manager/skills/v1
services:
  agent:
    mode: bundle
    language: python
    skills: [{path: not-created/greeting}]
  other:
    mode: mcp
    language: dotnet
    skills: []
`
	data := []byte(input)
	document, err := Parse(data)
	if err != nil || len(document.Services) != 2 || document.APIVersion != APIVersion {
		t.Fatalf("parsed document: %#v %v", document, err)
	}
	config, err := ParseService(data, "agent")
	if err != nil || config == nil || config.Language != "python" ||
		len(config.Skills) != 1 || config.Skills[0].Path != "not-created/greeting" {
		t.Fatalf("selected local declaration: %#v %v", config, err)
	}
	config, err = ParseService(data, "other")
	if err != nil || config == nil || config.Mode != ModeMCP ||
		config.Language != "dotnet" || len(config.Skills) != 0 {
		t.Fatalf("selected detached declaration: %#v %v", config, err)
	}
	config, err = ParseService(data, "missing")
	if err != nil || config != nil {
		t.Fatalf("absent declaration: %#v %v", config, err)
	}
	if string(data) != input {
		t.Fatal("offline validation changed proposed sidecar bytes")
	}
}

func TestParseAndLoadRejectInvalidUnselectedService(t *testing.T) {
	const input = `apiVersion: foundry-agent-manager/skills/v1
services:
  agent: {mode: bundle, language: python, skills: []}
  other:
    mode: mcp
    language: dotnet
    toolbox: {name: operations, version: "1"}
    skills: [{name: greeting, version: DEFAULT}]
`
	if _, err := Parse([]byte(input)); err == nil {
		t.Fatal("Parse ignored invalid declarations in another service")
	}
	if _, err := ParseService([]byte(input), "agent"); err == nil {
		t.Fatal("ParseService ignored invalid declarations in another service")
	}
	root := t.TempDir()
	writeTestFile(t, root, FileName, input)
	if _, err := Load(root, "agent"); err == nil {
		t.Fatal("Load did not apply whole-document validation")
	}
}

func TestRequiresRemote(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
		want   bool
	}{
		{"absent", nil, false},
		{"empty bundle", &Config{Mode: ModeBundle}, false},
		{"empty mcp", &Config{Mode: ModeMCP, Toolbox: &Toolbox{Name: "operations", Version: "1"}}, false},
		{"local bundle", &Config{Mode: ModeBundle, Skills: []Source{{Path: "skills/greeting"}}}, false},
		{"remote bundle", &Config{Mode: ModeBundle, Skills: []Source{{Name: "greeting", Version: "1"}}}, true},
		{"mixed bundle", &Config{Mode: ModeBundle, Skills: []Source{{Path: "skills/local"}, {Name: "greeting", Version: "1"}}}, true},
		{"mcp", &Config{Mode: ModeMCP, Skills: []Source{{Name: "greeting", Version: "1"}}}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.config.RequiresRemote(); got != test.want {
				t.Fatalf("RequiresRemote() = %v, want %v", got, test.want)
			}
		})
	}
}

func writeTestFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
