package hostedskills

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeFilesAreOwnedAndValidatedWithLocalBundle(t *testing.T) {
	for _, language := range []string{"python", "dotnet"} {
		t.Run(language, func(t *testing.T) {
			options := localOptions(t)
			options.Config.Language = language
			options.RuntimeFiles = map[string]string{
				"fam_skills/runtime.py":                  "def configure():\r\n    return None\r\n",
				"fam_skills/adapters/support/runtime.py": "VERSION = 1\n",
			}
			if language == "dotnet" {
				options.RuntimeFiles = map[string]string{
					"fam_skills/Runtime.cs":                   "internal static class Runtime {}\r\n",
					"fam_skills/Adapters/Support/Provider.cs": "internal static class Provider {}\n",
				}
			}
			artifact := mustSync(t, options)
			if artifact.Manifest.ProjectEndpoint != "" || artifact.RuntimeVerified {
				t.Fatal("local helper synchronization required a project or asserted runtime readiness")
			}
			if len(artifact.RequiredFiles) != 3+len(options.RuntimeFiles) {
				t.Fatalf("helpers omitted from required artifact inventory: %#v", artifact.RequiredFiles)
			}
			for name, content := range options.RuntimeFiles {
				relative := strings.TrimPrefix(name, DirectoryName+"/")
				actual := readTestFile(t, filepath.Join(options.SourceDirectory, filepath.FromSlash(name)))
				expectedHash := digest([]byte(content))
				if string(actual) != content || artifact.Manifest.RuntimeFiles[relative] != expectedHash ||
					artifact.FileSHA256[name] != expectedHash {
					t.Fatalf("runtime helper bytes or hashes changed: %s", name)
				}
				if _, err := os.Stat(filepath.Join(options.SourceDirectory, filepath.FromSlash(relative))); !os.IsNotExist(err) {
					t.Fatalf("helper written outside fam_skills: %s: %v", name, err)
				}
			}
			if _, err := os.Stat(filepath.Join(artifact.Directory, DirectoryName)); !os.IsNotExist(err) {
				t.Fatalf("source-root prefix was duplicated inside the artifact: %v", err)
			}
			validated, err := ValidateArtifact(validationOptions(options))
			if err != nil || validated.SHA256 != artifact.SHA256 {
				t.Fatalf("helper-bearing artifact did not validate offline: %#v %v", validated, err)
			}
			repeated := mustSync(t, options)
			if repeated.SHA256 != artifact.SHA256 {
				t.Fatal("identical runtime templates changed artifact identity")
			}
		})
	}
}

func TestRuntimeFilesPreserveWhenUnspecifiedAndPruneOnlyWhenExplicit(t *testing.T) {
	options := localOptions(t)
	options.RuntimeFiles = map[string]string{
		"fam_skills/runtime.py":                  "VERSION = 1\n",
		"fam_skills/adapters/support/runtime.py": "VERSION = 1\n",
	}
	writeTestFile(t, options.Root, "src/user-adapter.py", "user code\n")
	first := mustSync(t, options)
	options.RuntimeFiles = nil
	preserved := mustSync(t, options)
	if first.SHA256 != preserved.SHA256 || len(preserved.Manifest.RuntimeFiles) != 2 {
		t.Fatal("unspecified runtime templates were pruned or changed")
	}
	options.Config.Skills = []Source{}
	detached := mustSync(t, options)
	if detached.State != "detached" || len(detached.Manifest.RuntimeFiles) != 2 {
		t.Fatal("detachment did not preserve the owned adapters")
	}
	options.RuntimeFiles = map[string]string{}
	pruned := mustSync(t, options)
	if len(pruned.Manifest.RuntimeFiles) != 0 || len(pruned.RequiredFiles) != 2 {
		t.Fatalf("explicit helper removal left helper inventory: %#v", pruned)
	}
	for _, name := range []string{"runtime.py", "adapters", "greeting"} {
		if _, err := os.Stat(filepath.Join(pruned.Directory, name)); !os.IsNotExist(err) {
			t.Fatalf("obsolete owned file or directory was not pruned: %s: %v", name, err)
		}
	}
	if got := string(readTestFile(t, filepath.Join(options.SourceDirectory, "user-adapter.py"))); got != "user code\n" {
		t.Fatal("helper removal changed unowned application code")
	}
	if got := string(readTestFile(t, filepath.Join(options.Root, "skills", "greeting", "SKILL.md"))); got != testSkill {
		t.Fatal("helper removal changed original Skill source")
	}
}

func TestDetachStagesSuppliedAdapterAndPrunesObsoleteOwnedScripts(t *testing.T) {
	for _, mode := range []string{ModeBundle, ModeMCP} {
		t.Run(mode, func(t *testing.T) {
			options := localOptions(t)
			options.RuntimeFiles = map[string]string{
				"fam_skills/fam_skills_runtime.py": "old adapter fixture\n",
				"fam_skills/legacy/loader.py":      "obsolete helper fixture\n",
			}
			mustSync(t, options)
			options.Config.Mode = mode
			options.Config.Skills = []Source{}
			const adapter = "current adapter fixture supporting empty inventories\n"
			options.RuntimeFiles = map[string]string{"fam_skills/fam_skills_runtime.py": adapter}
			artifact := mustSync(t, options)
			if options.Config.RequiresRemote() || artifact.State != "detached" ||
				artifact.Manifest.Skills == nil || len(artifact.Manifest.Skills) != 0 ||
				len(artifact.Manifest.RuntimeFiles) != 1 || len(artifact.RequiredFiles) != 3 {
				t.Fatalf("detachment did not emit an empty inventory with its owned adapter: %#v", artifact)
			}
			if got := string(readTestFile(t, filepath.Join(artifact.Directory, "fam_skills_runtime.py"))); got != adapter {
				t.Fatal("detachment did not stage the supplied current adapter beside the manifest")
			}
			if artifact.Manifest.RuntimeFiles["fam_skills_runtime.py"] != digest([]byte(adapter)) {
				t.Fatal("detachment omitted the adapter digest")
			}
			for _, obsolete := range []string{"greeting", "legacy", DirectoryName} {
				if _, err := os.Stat(filepath.Join(artifact.Directory, obsolete)); !os.IsNotExist(err) {
					t.Fatalf("detachment retained obsolete or double-prefixed output %q: %v", obsolete, err)
				}
			}
			if _, err := ValidateArtifact(validationOptions(options)); err != nil {
				t.Fatal(err)
			}
			if got := string(readTestFile(t, filepath.Join(options.Root, "skills", "greeting", "SKILL.md"))); got != testSkill {
				t.Fatal("detachment changed original local Skill source")
			}
		})
	}
}

func TestRuntimeTemplateChangeChangesArtifactNotDeclarationHash(t *testing.T) {
	options := localOptions(t)
	options.RuntimeFiles = map[string]string{"fam_skills/runtime.py": "VERSION = 1\n"}
	before := mustSync(t, options)
	options.RuntimeFiles["fam_skills/runtime.py"] = "VERSION = 2\n"
	after := mustSync(t, options)
	if before.SHA256 == after.SHA256 || before.Manifest.RuntimeFiles["runtime.py"] == after.Manifest.RuntimeFiles["runtime.py"] {
		t.Fatal("updated runtime template omitted from artifact identity")
	}
	if before.Manifest.DeclarationHash != after.Manifest.DeclarationHash {
		t.Fatal("runtime template change modified the declaration-only hash")
	}
}

func TestModifiedOrMissingRuntimeHelpersBlockValidationAndSync(t *testing.T) {
	for _, change := range []string{"modified", "missing", "unowned"} {
		t.Run(change, func(t *testing.T) {
			options := localOptions(t)
			options.RuntimeFiles = map[string]string{"fam_skills/adapters/support/runtime.py": "VERSION = 1\n"}
			artifact := mustSync(t, options)
			manifest := readTestFile(t, artifact.ManifestPath)
			path := filepath.Join(artifact.Directory, "adapters", "support", "runtime.py")
			switch change {
			case "modified":
				if err := os.WriteFile(path, []byte("user edit\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "unowned":
				writeTestFile(t, artifact.Directory, "adapters/support/user.txt", "user data\n")
			}
			if _, err := ValidateArtifact(validationOptions(options)); err == nil {
				t.Fatal("runtime helper integrity failure was accepted")
			}
			if _, err := Sync(context.Background(), options); err == nil {
				t.Fatal("sync silently replaced modified or unowned helper output")
			}
			if !bytes.Equal(manifest, readTestFile(t, artifact.ManifestPath)) {
				t.Fatal("failed helper synchronization changed the manifest")
			}
		})
	}
}

func TestUnsafeRuntimeFilesLeaveExistingArtifactsUntouched(t *testing.T) {
	tests := map[string]map[string]string{
		"traversal":                {"fam_skills/../outside.py": "unsafe"},
		"absolute":                 {"/fam_skills/outside.py": "unsafe"},
		"drive":                    {"C:/fam_skills/outside.py": "unsafe"},
		"backslash":                {`fam_skills/adapters\loader.py`: "unsafe"},
		"empty path":               {"fam_skills/": "unsafe"},
		"missing prefix":           {"runtime.py": "unsafe"},
		"prefix sibling":           {"fam_skills_extra/runtime.py": "unsafe"},
		"manifest":                 {"fam_skills/manifest.json": "unsafe"},
		"case manifest":            {"fam_skills/MANIFEST.JSON": "unsafe"},
		"manifest parent":          {"fam_skills/manifest.json/loader.py": "unsafe"},
		"ownership":                {"fam_skills/.ownership.json": "unsafe"},
		"ownership parent":         {"fam_skills/.ownership.json/loader.py": "unsafe"},
		"skill instructions":       {"fam_skills/greeting/SKILL.md": "unsafe"},
		"skill supporting file":    {"fam_skills/greeting/loader.py": "unsafe"},
		"case skill directory":     {"fam_skills/GREETING/loader.py": "unsafe"},
		"file case collision":      {"fam_skills/runtime.py": "one", "fam_skills/RUNTIME.py": "two"},
		"directory case collision": {"fam_skills/helpers/one.py": "one", "fam_skills/Helpers/two.py": "two"},
		"file directory collision": {"fam_skills/helpers": "one", "fam_skills/helpers/runtime.py": "two"},
	}
	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			options := localOptions(t)
			options.RuntimeFiles = map[string]string{"fam_skills/runtime.py": "VERSION = 1\n"}
			original := mustSync(t, options)
			options.RuntimeFiles = files
			if _, err := Sync(context.Background(), options); err == nil {
				t.Fatal("unsafe runtime helper layout accepted")
			}
			validated, err := ValidateArtifact(validationOptions(options))
			if err != nil || validated.SHA256 != original.SHA256 {
				t.Fatalf("failed helper sync changed existing artifact or left a transaction: %#v %v", validated, err)
			}
		})
	}
}

func TestMCPRuntimeFilesRemainOwnedWithoutBundledInstructions(t *testing.T) {
	options, _ := remoteOptions(t, ModeMCP)
	options.RuntimeFiles = map[string]string{"fam_skills/Runtime.cs": "internal static class Runtime {}\n"}
	artifact := mustSync(t, options)
	if artifact.Manifest.Skills[0].Path != "" || len(artifact.RequiredFiles) != 3 {
		t.Fatalf("MCP runtime helpers changed instruction delivery mode: %#v", artifact)
	}
	if _, err := ValidateArtifact(validationOptions(options)); err != nil {
		t.Fatal(err)
	}
}

func TestImageHelperChangesRequireRebuildInBothDeliveryModes(t *testing.T) {
	for _, mode := range []string{ModeBundle, ModeMCP} {
		t.Run(mode, func(t *testing.T) {
			options, _ := remoteOptions(t, mode)
			options.Image = "registry.example/agent@sha256:" + strings.Repeat("a", 64)
			options.Config.Image = &ImageEvidence{Reference: options.Image, IntegrationEvidence: "image-evidence.txt"}
			writeTestFile(t, options.Root, "image-evidence.txt", "Operator integration record; not runtime readiness.\n")
			options.RuntimeFiles = map[string]string{"fam_skills/runtime.py": "VERSION = 1\n"}
			before := mustSync(t, options)
			options.RuntimeFiles["fam_skills/runtime.py"] = "VERSION = 2\n"
			if _, err := Sync(context.Background(), options); err == nil || !strings.Contains(err.Error(), "rebuild") {
				t.Fatalf("immutable image accepted helper change without rebuild: %v", err)
			}
			unchanged, err := ValidateArtifact(validationOptions(options))
			if err != nil || unchanged.SHA256 != before.SHA256 {
				t.Fatalf("rejected image helper update changed artifact: %#v %v", unchanged, err)
			}
			options.Image = "registry.example/agent@sha256:" + strings.Repeat("b", 64)
			options.Config.Image.Reference = options.Image
			after := mustSync(t, options)
			if before.SHA256 == after.SHA256 {
				t.Fatal("new image and runtime helper omitted from identity")
			}
		})
	}
}
