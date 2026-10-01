package hostedskills

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"foundry-agent-manager/internal/foundry"
)

const testEndpoint = "https://account.services.ai.azure.com/api/projects/project"
const testSkill = "---\nname: greeting\ndescription: A greeting skill\n---\nSay hello from the skill.\n"

func localOptions(t *testing.T) SyncOptions {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, root, "skills/greeting/SKILL.md", testSkill)
	writeTestFile(t, root, "src/main.py", "pass\n")
	return SyncOptions{
		Root: root, SourceDirectory: filepath.Join(root, "src"), Service: "agent",
		Config: &Config{Mode: ModeBundle, Language: "python", Skills: []Source{{Path: "skills/greeting"}}},
	}
}

func validationOptions(options SyncOptions) ValidateOptions {
	return ValidateOptions{
		Root: options.Root, SourceDirectory: options.SourceDirectory,
		Service: options.Service, Config: options.Config,
		ProjectEndpoint: options.ProjectEndpoint, Image: options.Image,
	}
}

func mustSync(t *testing.T, options SyncOptions) *Artifact {
	t.Helper()
	result, err := Sync(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLocalSyncPreservesExactBytesAndIsDeterministic(t *testing.T) {
	for _, language := range []string{"python", "dotnet"} {
		t.Run(language, func(t *testing.T) {
			options := localOptions(t)
			options.Config.Language = language
			original := strings.ReplaceAll(testSkill, "\n", "\r\n")
			writeTestFile(t, options.Root, "skills/greeting/SKILL.md", original)
			first := mustSync(t, options)
			if first.RuntimeVerified || first.State != "artifacts-verified" {
				t.Fatalf("artifact validation was mislabeled as runtime readiness: %#v", first)
			}
			entry := first.Manifest.Skills[0]
			if entry.Path != "greeting/SKILL.md" || entry.SHA256 != digest([]byte(original)) || entry.Version != "" {
				t.Fatalf("unexpected runtime inventory: %#v", entry)
			}
			content := readTestFile(t, filepath.Join(first.Directory, "greeting", "SKILL.md"))
			if string(content) != original {
				t.Fatal("original SKILL.md bytes were changed")
			}
			second := mustSync(t, options)
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("repeat synchronization is not deterministic: first=%#v second=%#v", first, second)
			}
			if len(first.RequiredFiles) != 3 || len(first.FileSHA256) != 3 {
				t.Fatalf("incomplete packaging inventory: %#v", first)
			}
		})
	}
}

func TestLocalChangesRequireExplicitSync(t *testing.T) {
	options := localOptions(t)
	before := mustSync(t, options)
	writeTestFile(t, options.Root, "skills/greeting/SKILL.md", testSkill+"New instructions.\n")
	if _, err := ValidateArtifact(validationOptions(options)); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("changed local instructions not detected: %v", err)
	}
	after := mustSync(t, options)
	if before.SHA256 == after.SHA256 || before.Manifest.Skills[0].SHA256 == after.Manifest.Skills[0].SHA256 {
		t.Fatal("local content change omitted from artifact identity")
	}
}

func TestDeclarationChangesAreStale(t *testing.T) {
	options := localOptions(t)
	mustSync(t, options)
	options.Config.Language = "dotnet"
	if _, err := ValidateArtifact(validationOptions(options)); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("changed declaration not detected: %v", err)
	}
	mustSync(t, options)
}

func TestSyncNeverOverwritesUnownedOrModifiedFiles(t *testing.T) {
	for _, change := range []string{"instruction", "manifest", "missing", "unowned-file", "unowned-directory", "other-service", "ownership", "ownership-format"} {
		t.Run(change, func(t *testing.T) {
			options := localOptions(t)
			artifact := mustSync(t, options)
			switch change {
			case "instruction":
				writeTestFile(t, artifact.Directory, "greeting/SKILL.md", "user-edited output\n")
			case "manifest":
				writeTestFile(t, artifact.Directory, ManifestFileName, "{}\n")
			case "missing":
				if err := os.Remove(filepath.Join(artifact.Directory, "greeting", "SKILL.md")); err != nil {
					t.Fatal(err)
				}
			case "unowned-file":
				writeTestFile(t, artifact.Directory, "notes.txt", "user data\n")
			case "unowned-directory":
				if err := os.Mkdir(filepath.Join(artifact.Directory, "user-data"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "other-service":
				options.Service = "other"
			case "ownership":
				writeTestFile(t, artifact.Directory, ownershipFile, "{}\n")
			case "ownership-format":
				data := readTestFile(t, filepath.Join(artifact.Directory, ownershipFile))
				writeTestFile(t, artifact.Directory, ownershipFile, string(data)+"\n")
			}
			if _, err := ValidateArtifact(validationOptions(options)); err == nil {
				t.Fatal("invalid managed output accepted by validation")
			}
			if _, err := Sync(context.Background(), options); err == nil {
				t.Fatal("modified or unowned output was overwritten")
			}
			if got := string(readTestFile(t, filepath.Join(options.Root, "skills", "greeting", "SKILL.md"))); got != testSkill {
				t.Fatal("original local source was modified")
			}
			if change == "unowned-file" && string(readTestFile(t, filepath.Join(artifact.Directory, "notes.txt"))) != "user data\n" {
				t.Fatal("unowned file was changed")
			}
		})
	}
}

func TestExplicitEmptyDeclarationPrunesOnlyOwnedOutputWithoutProvider(t *testing.T) {
	for _, mode := range []string{ModeBundle, ModeMCP} {
		t.Run(mode, func(t *testing.T) {
			options := localOptions(t)
			mustSync(t, options)
			options.Config.Mode = mode
			options.Config.Skills = []Source{}
			result := mustSync(t, options)
			if result.State != "detached" || result.Manifest.Skills == nil || len(result.Manifest.Skills) != 0 || len(result.RequiredFiles) != 2 {
				t.Fatalf("unexpected detached artifact: %#v", result)
			}
			if _, err := os.Stat(filepath.Join(result.Directory, "greeting")); !os.IsNotExist(err) {
				t.Fatalf("obsolete owned Skill directory was not pruned: %v", err)
			}
			if got := string(readTestFile(t, filepath.Join(options.Root, "skills", "greeting", "SKILL.md"))); got != testSkill {
				t.Fatal("detachment changed original local source")
			}
			if got := string(readTestFile(t, filepath.Join(options.SourceDirectory, "main.py"))); got != "pass\n" {
				t.Fatal("detachment changed application source")
			}
		})
	}
}

func TestMissingArtifactAndInterruptedSyncFailClosed(t *testing.T) {
	options := localOptions(t)
	if _, err := ValidateArtifact(validationOptions(options)); err == nil {
		t.Fatal("missing synchronization accepted")
	}
	mustSync(t, options)
	for _, transaction := range []string{lockDirectory, transactionPrefix + "backup-interrupted"} {
		path := filepath.Join(options.SourceDirectory, transaction)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateArtifact(validationOptions(options)); err == nil {
			t.Fatal("interrupted sync accepted")
		}
		if _, err := Sync(context.Background(), options); err == nil {
			t.Fatal("interrupted sync silently discarded")
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

type readClient struct {
	versions      map[string]*foundry.SkillVersion
	archives      map[string][]byte
	toolbox       *foundry.ToolboxVersion
	calls         []string
	downloadError error
	afterDownload func()
}

func (c *readClient) GetSkillVersionContext(_ context.Context, name, version string) (*foundry.SkillVersion, error) {
	key := name + ":" + version
	c.calls = append(c.calls, "skill:"+key)
	return c.versions[key], nil
}

func (c *readClient) DownloadSkillContext(_ context.Context, name, version string) ([]byte, error) {
	key := name + ":" + version
	c.calls = append(c.calls, "download:"+key)
	if c.afterDownload != nil {
		c.afterDownload()
	}
	return c.archives[key], c.downloadError
}

func (c *readClient) GetToolboxVersionContext(_ context.Context, name, version string) (*foundry.ToolboxVersion, error) {
	c.calls = append(c.calls, "toolbox:"+name+":"+version)
	return c.toolbox, nil
}

func skillArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		part, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func remoteOptions(t *testing.T, mode string) (SyncOptions, *readClient) {
	t.Helper()
	options := localOptions(t)
	options.Config.Mode = mode
	options.Config.Skills = []Source{{Name: "greeting", Version: "17"}}
	options.ProjectEndpoint = testEndpoint
	client := &readClient{
		versions: map[string]*foundry.SkillVersion{"greeting:17": {Name: "greeting", Version: "17"}},
		archives: map[string][]byte{"greeting:17": skillArchive(t, map[string]string{"SKILL.md": testSkill})},
	}
	options.Client = client
	if mode == ModeMCP {
		options.Config.Toolbox = &Toolbox{Name: "operations", Version: "5"}
		client.toolbox = &foundry.ToolboxVersion{
			Name: "operations", Version: "5",
			Skills: []any{map[string]any{"type": "skill_reference", "name": "greeting", "version": "17"}},
		}
	}
	return options, client
}

func TestRemoteSyncReadsOnlyExactImmutableVersions(t *testing.T) {
	for _, mode := range []string{ModeBundle, ModeMCP} {
		t.Run(mode, func(t *testing.T) {
			options, client := remoteOptions(t, mode)
			result := mustSync(t, options)
			expected := []string{"skill:greeting:17", "download:greeting:17"}
			if mode == ModeMCP {
				expected = append([]string{"toolbox:operations:5"}, expected...)
				if result.Manifest.ToolboxName != "operations" || result.Manifest.ToolboxVersion != "5" ||
					result.Manifest.Skills[0].Path != "" || len(result.RequiredFiles) != 2 {
					t.Fatalf("incorrect MCP inventory: %#v", result)
				}
			}
			if !reflect.DeepEqual(client.calls, expected) {
				t.Fatalf("unexpected provider calls: %#v", client.calls)
			}
			if result.Manifest.ProjectEndpoint != testEndpoint || result.Manifest.Skills[0].SHA256 != digest([]byte(testSkill)) ||
				result.Manifest.Skills[0].ArchiveSHA256 != digest(client.archives["greeting:17"]) {
				t.Fatalf("missing exact source provenance: %#v", result.Manifest)
			}
			offline := validationOptions(options)
			offline.ProjectEndpoint = ""
			if _, err := ValidateArtifact(offline); err != nil {
				t.Fatalf("optional project binding failed: %v", err)
			}
			offline.ProjectEndpoint = strings.TrimSuffix(testEndpoint, "/project") + "/other"
			if _, err := ValidateArtifact(offline); err == nil {
				t.Fatal("different project binding accepted")
			}
			if !reflect.DeepEqual(client.calls, expected) {
				t.Fatal("offline artifact validation contacted the provider")
			}
		})
	}
}

func TestMCPRejectsUnpinnedMissingAndMismatchedToolboxReferences(t *testing.T) {
	for _, change := range []string{"missing-toolbox", "wrong-toolbox-version", "empty", "unselected-unpinned", "wrong-skill-version", "duplicate", "unsupported-type", "local"} {
		t.Run(change, func(t *testing.T) {
			options, client := remoteOptions(t, ModeMCP)
			switch change {
			case "missing-toolbox":
				client.toolbox = nil
			case "wrong-toolbox-version":
				client.toolbox.Version = "6"
			case "empty":
				client.toolbox.Skills = nil
			case "unselected-unpinned":
				client.toolbox.Skills = append(client.toolbox.Skills, map[string]any{"type": "skill_reference", "name": "other"})
			case "wrong-skill-version":
				client.toolbox.Skills[0].(map[string]any)["version"] = "18"
			case "duplicate":
				client.toolbox.Skills = append(client.toolbox.Skills, client.toolbox.Skills[0])
			case "unsupported-type":
				client.toolbox.Skills[0].(map[string]any)["type"] = "mcp"
			case "local":
				options.Config.Skills = []Source{{Path: "skills/greeting"}}
			}
			if _, err := Sync(context.Background(), options); err == nil {
				t.Fatal("invalid MCP allowlist accepted")
			}
			if _, err := os.Stat(filepath.Join(options.SourceDirectory, DirectoryName)); !os.IsNotExist(err) {
				t.Fatalf("failed sync wrote output: %v", err)
			}
		})
	}
}

func TestInvalidRemoteSourceLeavesExistingOutputUntouched(t *testing.T) {
	for _, change := range []string{"deleted", "wrong-version", "wrong-name", "supporting-files", "name-mismatch", "download-error"} {
		t.Run(change, func(t *testing.T) {
			options := localOptions(t)
			original := mustSync(t, options)
			originalManifest := readTestFile(t, original.ManifestPath)
			remote, client := remoteOptions(t, ModeBundle)
			remote.Root, remote.SourceDirectory = options.Root, options.SourceDirectory
			switch change {
			case "deleted":
				delete(client.versions, "greeting:17")
			case "wrong-version":
				client.versions["greeting:17"].Version = "18"
			case "wrong-name":
				client.versions["greeting:17"].Name = "other"
			case "supporting-files":
				client.archives["greeting:17"] = skillArchive(t, map[string]string{"SKILL.md": testSkill, "run.py": "pass"})
			case "name-mismatch":
				client.archives["greeting:17"] = skillArchive(t, map[string]string{"SKILL.md": strings.Replace(testSkill, "greeting", "other", 1)})
			case "download-error":
				client.downloadError = errors.New("download unavailable")
			}
			if _, err := Sync(context.Background(), remote); err == nil {
				t.Fatal("invalid remote source accepted")
			}
			if !bytes.Equal(originalManifest, readTestFile(t, original.ManifestPath)) {
				t.Fatal("failed validation replaced existing output")
			}
		})
	}
}

func TestCanceledSyncDoesNotReplaceOutput(t *testing.T) {
	options, client := remoteOptions(t, ModeBundle)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.afterDownload = cancel
	if _, err := Sync(ctx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	entries, err := os.ReadDir(options.SourceDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "main.py" {
		t.Fatalf("canceled sync left generated output: %#v", entries)
	}
}

func TestSyncRejectsManagedPresenceChangesDuringDownload(t *testing.T) {
	for _, transition := range []string{"absent-to-present", "present-to-absent"} {
		t.Run(transition, func(t *testing.T) {
			options, client := remoteOptions(t, ModeBundle)
			var original *Artifact
			if transition == "present-to-absent" {
				original = mustSync(t, options)
			}
			source, err := os.OpenRoot(options.SourceDirectory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := source.Close(); err != nil {
					t.Error(err)
				}
			})
			var expected *managedFiles
			client.calls = nil
			client.afterDownload = func() {
				if transition == "absent-to-present" {
					detach := options
					config := *options.Config
					config.Skills = []Source{}
					detach.Config, detach.Client = &config, nil
					mustSync(t, detach)
				} else if err := source.Rename(DirectoryName, "preserved-skills"); err != nil {
					t.Fatal(err)
				}
				var err error
				expected, err = readManaged(source, DirectoryName, options.Service)
				if err != nil {
					t.Fatal(err)
				}
			}
			result, err := Sync(context.Background(), options)
			if err == nil || !strings.Contains(err.Error(), "changed during synchronization") || result != nil {
				t.Fatalf("sync accepted a changed presence baseline: %#v %v", result, err)
			}
			current, err := readManaged(source, DirectoryName, options.Service)
			if err != nil || !sameManaged(expected, current) {
				t.Fatalf("conflicting sync replaced output: %#v %v", current, err)
			}
			if original != nil {
				preserved, err := readManaged(source, "preserved-skills", options.Service)
				if err != nil || preserved == nil || preserved.identity != original.SHA256 {
					t.Fatalf("conflicting sync changed preserved output: %#v %v", preserved, err)
				}
			}
			if err := checkTransactions(source); err != nil {
				t.Fatal(err)
			}
			if len(client.calls) != 2 {
				t.Fatalf("conflicting sync retried provider reads: %#v", client.calls)
			}
		})
	}
}

func TestNoInventedSkillCountCap(t *testing.T) {
	options := localOptions(t)
	options.Config.Skills = nil
	for index := range 40 {
		name := fmt.Sprintf("skill-%d", index)
		writeTestFile(t, options.Root, "skills/"+name+"/SKILL.md", strings.Replace(testSkill, "name: greeting", "name: "+name, 1))
		options.Config.Skills = append(options.Config.Skills, Source{Path: "skills/" + name})
	}
	result := mustSync(t, options)
	if len(result.Manifest.Skills) != 40 {
		t.Fatal("Skill inventory was truncated")
	}
}

func TestSyncRejectsOverlappingAndExcludedArtifactLocations(t *testing.T) {
	for _, location := range []string{"outside", "excluded", "managed-input", "case-collision", "unowned"} {
		t.Run(location, func(t *testing.T) {
			options := localOptions(t)
			switch location {
			case "outside":
				options.SourceDirectory = t.TempDir()
			case "excluded":
				options.SourceDirectory = filepath.Join(options.Root, ".foundry-agent-manager")
				if err := os.Mkdir(options.SourceDirectory, 0o700); err != nil {
					t.Fatal(err)
				}
			case "managed-input":
				options.Config.Skills[0].Path = "src/fam_skills/greeting"
				writeTestFile(t, options.Root, "src/fam_skills/greeting/SKILL.md", testSkill)
			case "case-collision":
				if err := os.Mkdir(filepath.Join(options.SourceDirectory, "FAM_SKILLS"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "unowned":
				writeTestFile(t, options.Root, "src/fam_skills/greeting/SKILL.md", testSkill)
			}
			if _, err := Sync(context.Background(), options); err == nil {
				t.Fatal("unsafe output location accepted")
			}
		})
	}
}

func TestImageArtifactsRequireImmutableBindingAndHonestEvidence(t *testing.T) {
	options := localOptions(t)
	options.Image = "registry.example/agent@sha256:" + strings.Repeat("a", 64)
	if _, err := Sync(context.Background(), options); err == nil {
		t.Fatal("image without integration evidence accepted")
	}
	options.Config.Image = &ImageEvidence{Reference: options.Image, IntegrationEvidence: "build-evidence.txt"}
	writeTestFile(t, options.Root, "build-evidence.txt", "Operator recorded provider registration and bundle inclusion in this image build.\n")
	artifact := mustSync(t, options)
	if artifact.State != "operator-integration-evidence" || artifact.RuntimeVerified || artifact.Manifest.ImageReference != options.Image {
		t.Fatalf("image artifacts were mislabeled: %#v", artifact)
	}
	writeTestFile(t, options.Root, "build-evidence.txt", "Updated build record.\n")
	if _, err := ValidateArtifact(validationOptions(options)); err == nil {
		t.Fatal("changed image evidence was not detected")
	}
	updated := mustSync(t, options)
	if updated.SHA256 == artifact.SHA256 {
		t.Fatal("image evidence did not participate in artifact identity")
	}
	writeTestFile(t, options.Root, "skills/greeting/SKILL.md", testSkill+"Rebuilt instructions.\n")
	if _, err := Sync(context.Background(), options); err == nil || !strings.Contains(err.Error(), "rebuild") {
		t.Fatalf("bundle changes reused an immutable image without rebuild: %v", err)
	}
	options.Image = "registry.example/agent@sha256:" + strings.Repeat("b", 64)
	if _, err := ValidateArtifact(validationOptions(options)); err == nil {
		t.Fatal("different immutable image accepted")
	}
	options.Config.Image.Reference = options.Image
	mustSync(t, options)
	options.Image = "registry.example/agent:latest"
	options.Config.Image.Reference = options.Image
	if _, err := Sync(context.Background(), options); err == nil {
		t.Fatal("moving image tag accepted")
	}
}

func TestSyncRejectsDuplicateResolvedLocalAndRemoteIdentity(t *testing.T) {
	options, _ := remoteOptions(t, ModeBundle)
	options.Config.Skills = append(options.Config.Skills, Source{Path: "skills/greeting"})
	if _, err := Sync(context.Background(), options); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate resolved Skill identity accepted: %v", err)
	}
}

func TestNoDeclarationPreservesUnmanagedBehavior(t *testing.T) {
	artifact, err := ValidateArtifact(ValidateOptions{})
	if err != nil || artifact != nil {
		t.Fatalf("absent declaration must be unmanaged: %#v %v", artifact, err)
	}
	if _, err := Sync(context.Background(), SyncOptions{}); err == nil {
		t.Fatal("implicit detachment accepted without an explicit declaration")
	}
}

func TestSyncRequiresEndpointAndClientOnlyForRemoteSources(t *testing.T) {
	for _, missing := range []string{"client", "endpoint"} {
		t.Run(missing, func(t *testing.T) {
			options, _ := remoteOptions(t, ModeBundle)
			if missing == "client" {
				options.Client = nil
			} else {
				options.ProjectEndpoint = ""
			}
			if _, err := Sync(context.Background(), options); err == nil {
				t.Fatalf("remote source accepted without %s", missing)
			}
		})
	}
	for _, endpoint := range []string{
		"https://attacker.invalid/api/projects/project",
		testEndpoint + "?api-version=v1",
		testEndpoint + "#fragment",
		"https://account.services.ai.azure.com",
		"https://account.services.ai.azure.com/api/projects/project/skills",
	} {
		options, client := remoteOptions(t, ModeMCP)
		options.ProjectEndpoint = endpoint
		if _, err := Sync(context.Background(), options); err == nil || len(client.calls) != 0 {
			t.Fatalf("invalid endpoint contacted provider or succeeded: %q %v %#v", endpoint, err, client.calls)
		}
	}
}

func TestSyncRejectsSourceAndOutputSymlinks(t *testing.T) {
	for _, location := range []string{"source", "artifact"} {
		t.Run(location, func(t *testing.T) {
			options := localOptions(t)
			target := t.TempDir()
			name := filepath.Join(options.SourceDirectory, DirectoryName)
			if location == "source" {
				name = filepath.Join(options.Root, "linked-source")
				options.SourceDirectory = name
			}
			if err := os.Symlink(target, name); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if _, err := Sync(context.Background(), options); err == nil {
				t.Fatal("symlinked managed output accepted")
			}
			entries, err := os.ReadDir(target)
			if err != nil || len(entries) != 0 {
				t.Fatalf("sync modified link destination: %v %#v", err, entries)
			}
		})
	}
}

func TestCleanupRefusesModifiedAndUnownedTransactionFiles(t *testing.T) {
	for _, change := range []string{"modified", "unowned"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			stage := transactionPrefix + "stage-recovery"
			writeTestFile(t, root, stage+"/manifest.json", "expected\n")
			expected := map[string][]byte{"manifest.json": []byte("expected\n")}
			if change == "modified" {
				writeTestFile(t, root, stage+"/manifest.json", "user edit\n")
			} else {
				writeTestFile(t, root, stage+"/user.txt", "user data\n")
			}
			directory, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			if err := removeVerified(directory, stage, expected); err == nil {
				t.Fatal("cleanup deleted modified or unowned transaction data")
			}
			if _, err := os.Stat(filepath.Join(root, stage, "manifest.json")); err != nil {
				t.Fatalf("cleanup partially removed transaction despite validation failure: %v", err)
			}
		})
	}
}
