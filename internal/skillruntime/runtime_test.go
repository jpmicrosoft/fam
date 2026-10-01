package skillruntime

import (
	"embed"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestPythonRequirementsUseCompatibleSDKPins(t *testing.T) {
	want := []string{
		"agent-framework-core==1.19.0",
		"agent-framework-foundry==1.13.1",
		"agent-framework-foundry-hosting==1.0.0b260918",
		"mcp==1.30.0",
	}
	got := strings.Fields(PythonRequirements)
	if len(got) != len(want) {
		t.Fatalf("Python requirements = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Python requirements = %v, want %v", got, want)
		}
	}
}

func TestFilesAreSourceRelativeAndIndependent(t *testing.T) {
	for _, test := range []struct {
		language string
		filename string
	}{
		{"python", "fam_skills_runtime.py"},
		{" Python ", "fam_skills_runtime.py"},
		{"dotnet", "FamSkillsRuntime.cs"},
		{"DOTNET", "FamSkillsRuntime.cs"},
	} {
		template, templateErr := runtimeTemplates.ReadFile("templates/" + test.filename)
		files, err := Files(test.language)
		if errors.Is(templateErr, fs.ErrNotExist) || templateErr == nil && strings.TrimSpace(string(template)) == "" {
			if strings.TrimSpace(strings.ToLower(test.language)) == "python" {
				t.Fatal("the implemented Python template must be available")
			}
			if !errors.Is(err, ErrProviderUnverified) || files != nil {
				t.Fatalf("Files(%q) did not fail closed for an unavailable template: %v", test.language, err)
			}
			continue
		}
		if templateErr != nil {
			t.Fatal(templateErr)
		}
		if err != nil {
			t.Fatal(err)
		}
		path := "fam_skills/" + test.filename
		if len(files) != 1 || files[path] != string(template) {
			t.Fatalf("Files(%q) did not return the embedded source-relative helper", test.language)
		}
		delete(files, path)
		again, err := Files(test.language)
		if err != nil || len(again) != 1 {
			t.Fatalf("callers can mutate the shared runtime inventory: %v", err)
		}
	}
}

func TestMissingTemplatesFailClosed(t *testing.T) {
	original := runtimeTemplates
	runtimeTemplates = embed.FS{}
	t.Cleanup(func() { runtimeTemplates = original })
	for _, language := range []string{"python", "dotnet"} {
		files, err := Files(language)
		if !errors.Is(err, ErrProviderUnverified) || files != nil {
			t.Fatalf("Files(%q) with no embedded template = %v, %v", language, files, err)
		}
	}
}

func TestFilesRejectsUnsupportedLanguage(t *testing.T) {
	for _, language := range []string{"", "go", "javascript", "python_3_13", "dotnet_10"} {
		t.Run(language, func(t *testing.T) {
			files, err := Files(language)
			if !errors.Is(err, ErrUnsupportedLanguage) {
				t.Fatalf("Files(%q) error = %v, want ErrUnsupportedLanguage", language, err)
			}
			if files != nil {
				t.Fatalf("Files(%q) returned artifacts for an unsupported language", language)
			}
		})
	}
}
