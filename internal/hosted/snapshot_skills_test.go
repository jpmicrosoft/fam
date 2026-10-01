package hosted

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/hostedskills"
)

func syncWorkspaceSkills(t *testing.T, workspace Workspace) *hostedskills.Artifact {
	t.Helper()
	artifact, err := hostedskills.Sync(context.Background(), hostedskills.SyncOptions{
		Root:            workspace.Root,
		SourceDirectory: workspace.Selected.SourceDirectory,
		Service:         workspace.Selected.ServiceName,
		Config:          workspace.Selected.Skills,
		ProjectEndpoint: workspace.Selected.ProjectEndpoint,
		Image:           workspace.Selected.Image,
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func skillWorkspace(t *testing.T) Workspace {
	t.Helper()
	root := validCodeWorkspace(t)
	writeHostedSkillInputs(t, root)
	workspace, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	return workspace
}

func TestCodeArchiveIncludesExactSynchronizedSkillArtifacts(t *testing.T) {
	workspace := skillWorkspace(t)
	if _, err := BuildCodeArchive(workspace); err == nil {
		t.Fatal("archive accepted unsynchronized Skills")
	}
	artifact := syncWorkspaceSkills(t, workspace)
	archive, err := BuildCodeArchive(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Remove()
	reader, err := zip.OpenReader(archive.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	found := make(map[string][]byte)
	for _, file := range reader.File {
		opened, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(opened)
		closeErr := opened.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("read archive: %v %v", err, closeErr)
		}
		found[file.Name] = data
	}
	for _, relative := range artifact.RequiredFiles {
		onDisk, err := os.ReadFile(filepath.Join(workspace.Selected.SourceDirectory, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(found[relative], onDisk) {
			t.Fatalf("archive omitted or changed required artifact %s", relative)
		}
	}
}

func TestSkillPackagingRejectsFilesAddedAfterValidation(t *testing.T) {
	workspace := skillWorkspace(t)
	syncWorkspaceSkills(t, workspace)
	artifact, err := ValidateSkillArtifacts(workspace)
	if err != nil {
		t.Fatal(err)
	}
	const relative = "fam_skills/greeting/late.py"
	payload := []byte("# inert late packaging fixture\n")
	if err := os.WriteFile(filepath.Join(workspace.Selected.SourceDirectory, filepath.FromSlash(relative)), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := hostedSourceFiles(workspace.Selected.SourceDirectory, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireSkillFiles(files, artifact); err == nil || !strings.Contains(err.Error(), relative) {
		t.Fatalf("packaging inventory accepted a file added after validation: %v", err)
	}
	digest := sha256.New()
	if _, err := digest.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := verifySkillFile(artifact, relative, digest); err == nil || !strings.Contains(err.Error(), relative) {
		t.Fatalf("packaging digest check accepted an unknown managed file: %v", err)
	}
}

func TestSkillPackagingInventoryBoundaries(t *testing.T) {
	workspace := skillWorkspace(t)
	artifact := syncWorkspaceSkills(t, workspace)
	files, err := hostedSourceFiles(workspace.Selected.SourceDirectory, true)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(artifact.Directory, "greeting", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	if _, err := digest.Write(content); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		relative string
		reject   bool
	}{
		{"fam_skills/greeting/SKILL.md", false},
		{"FAM_SKILLS/greeting/SKILL.md", true},
		{"fam_skills/GREETING/SKILL.md", true},
		{"fam_skills/greeting/skill.md", true},
		{"fam_skills", true},
		{"main.py", false},
		{"fam_skills_extra/late.py", false},
		{"nested/fam_skills/late.py", false},
	} {
		t.Run(test.relative, func(t *testing.T) {
			// Synthetic enumeration keeps case-collision coverage portable to Windows.
			enumerated := append(append([]hostedSourceFile(nil), files...), hostedSourceFile{relative: filepath.FromSlash(test.relative)})
			if err := requireSkillFiles(enumerated, artifact); (err != nil) != test.reject {
				t.Fatalf("inventory rejection = %v, want %t", err, test.reject)
			}
			if err := verifySkillFile(artifact, filepath.FromSlash(test.relative), digest); (err != nil) != test.reject {
				t.Fatalf("digest rejection = %v, want %t", err, test.reject)
			}
			if err := requireSkillFiles(enumerated, nil); err != nil {
				t.Fatalf("unmanaged inventory was rejected: %v", err)
			}
			if err := verifySkillFile(nil, test.relative, digest); err != nil {
				t.Fatalf("unmanaged file was rejected: %v", err)
			}
		})
	}
}

func TestCodePackagingAcceptsEmptyAndDetachedSkills(t *testing.T) {
	for _, state := range []string{"empty", "detached"} {
		t.Run(state, func(t *testing.T) {
			workspace := skillWorkspace(t)
			if state == "detached" {
				syncWorkspaceSkills(t, workspace)
			}
			workspace.Selected.Skills.Skills = []hostedskills.Source{}
			artifact := syncWorkspaceSkills(t, workspace)
			if artifact.State != "detached" || len(artifact.RequiredFiles) != 2 {
				t.Fatalf("unexpected empty inventory: %#v", artifact)
			}
			if _, err := ValidateSkillArtifacts(workspace); err != nil {
				t.Fatal(err)
			}
			if _, err := ComputeDeploymentSnapshot(workspace, "dev"); err != nil {
				t.Fatal(err)
			}
			archive, err := BuildCodeArchive(workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer archive.Remove()
		})
	}
}

func TestRuntimeHelpersAreArchivedAndChangeDeploymentIdentityWithoutProjectBinding(t *testing.T) {
	workspace := skillWorkspace(t)
	if workspace.Selected.ProjectEndpoint == "" {
		t.Fatal("fixture must declare a project to verify credential-free local artifact handling")
	}
	options := hostedskills.SyncOptions{
		Root:            workspace.Root,
		SourceDirectory: workspace.Selected.SourceDirectory,
		Service:         workspace.Selected.ServiceName,
		Config:          workspace.Selected.Skills,
		RuntimeFiles:    map[string]string{"fam_skills/adapters/support/runtime.py": "VERSION = 1\r\n"},
	}
	first, err := hostedskills.Sync(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := ValidateSkillArtifacts(workspace)
	if err != nil || validated.SHA256 != first.SHA256 {
		t.Fatalf("local-only validation required a project binding or omitted helpers: %#v %v", validated, err)
	}
	before, err := ComputeDeploymentSnapshot(workspace, "dev")
	if err != nil {
		t.Fatalf("local-only snapshot required a project binding: %v", err)
	}
	archive, err := BuildCodeArchive(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Remove()
	reader, err := zip.OpenReader(archive.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	const relative = "fam_skills/adapters/support/runtime.py"
	file, err := reader.Open(relative)
	if err != nil {
		t.Fatalf("runtime helper missing from code archive: %v", err)
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("cannot read archived helper: %v %v", readErr, closeErr)
	}
	if string(data) != options.RuntimeFiles[relative] {
		t.Fatal("archive changed exact runtime helper bytes")
	}
	options.RuntimeFiles[relative] = "VERSION = 2\n"
	second, err := hostedskills.Sync(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ComputeDeploymentSnapshot(workspace, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if before.Hash == after.Hash || first.SHA256 == second.SHA256 {
		t.Fatal("runtime helper change was omitted from artifact or deployment identity")
	}
	if err := os.WriteFile(filepath.Join(workspace.Selected.SourceDirectory, ".agentignore"), []byte(relative+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildCodeArchive(workspace); err == nil || !strings.Contains(err.Error(), "excluded") {
		t.Fatalf("archive accepted excluded required runtime helper: %v", err)
	}
}

func TestSkillArtifactsCannotBeExcludedByAgentIgnore(t *testing.T) {
	for _, rule := range []string{"fam_skills/", "fam_skills/**", "**/SKILL.md", ".ownership.json"} {
		t.Run(rule, func(t *testing.T) {
			workspace := skillWorkspace(t)
			syncWorkspaceSkills(t, workspace)
			if err := os.WriteFile(filepath.Join(workspace.Selected.SourceDirectory, ".agentignore"), []byte(rule+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := BuildCodeArchive(workspace); err == nil || !strings.Contains(err.Error(), "excluded") {
				t.Fatalf("archive silently omitted required artifact: %v", err)
			}
			if _, err := ComputeDeploymentSnapshot(workspace, "dev"); err == nil {
				t.Fatal("snapshot accepted excluded Skill output")
			}
			if _, err := ValidateSkillArtifacts(workspace); err == nil {
				t.Fatal("preflight validation accepted excluded Skill output")
			}
		})
	}
}

type snapshotSkillReader struct {
	archive []byte
}

func (reader snapshotSkillReader) GetSkillVersionContext(_ context.Context, name, version string) (*foundry.SkillVersion, error) {
	return &foundry.SkillVersion{Name: name, Version: version}, nil
}

func (reader snapshotSkillReader) DownloadSkillContext(context.Context, string, string) ([]byte, error) {
	return reader.archive, nil
}

func (reader snapshotSkillReader) GetToolboxVersionContext(context.Context, string, string) (*foundry.ToolboxVersion, error) {
	return nil, errors.New("unexpected Toolbox read in remote bundle validation fixture")
}

func TestValidateSkillArtifactsDefersRemoteBindingUntilEndpointIsResolved(t *testing.T) {
	root := writeWorkspace(t, `name: hosted-project
services:
  agent:
    host: azure.ai.agent
    kind: hosted
    project: src
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
`, map[string]string{
		"src/main.py": "pass\n",
		hostedskills.FileName: `apiVersion: foundry-agent-manager/skills/v1
services:
  agent:
    mode: bundle
    language: python
    skills: [{name: greeting, version: "1"}]
`,
	})
	workspace, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Selected.ProjectEndpoint != "" || !workspace.Selected.Skills.RequiresRemote() {
		t.Fatal("fixture must require a remote Skill without a declared project endpoint")
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(hostedTestSkill)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	const endpoint = "https://account.services.ai.azure.com/api/projects/resolved"
	artifact, err := hostedskills.Sync(context.Background(), hostedskills.SyncOptions{
		Root:            root,
		SourceDirectory: workspace.Selected.SourceDirectory,
		Service:         workspace.Selected.ServiceName,
		Config:          workspace.Selected.Skills,
		ProjectEndpoint: endpoint,
		Client:          snapshotSkillReader{archive: archive.Bytes()},
	})
	if err != nil {
		t.Fatal(err)
	}
	offline, err := ValidateSkillArtifacts(workspace)
	if err != nil || offline.SHA256 != artifact.SHA256 || offline.Manifest.ProjectEndpoint != endpoint {
		t.Fatalf("offline validation failed to preserve deferred project binding: %#v %v", offline, err)
	}
	workspace.Selected.ProjectEndpoint = endpoint
	if _, err := ValidateSkillArtifacts(workspace); err != nil {
		t.Fatalf("matching resolved endpoint was rejected: %v", err)
	}
	workspace.Selected.ProjectEndpoint = "https://account.services.ai.azure.com/api/projects/other"
	if _, err := ValidateSkillArtifacts(workspace); err == nil {
		t.Fatal("online validation accepted a different resolved project")
	}
}

func TestValidateSkillArtifactsWithoutDeclarationPreservesUnmanagedBehavior(t *testing.T) {
	artifact, err := ValidateSkillArtifacts(Workspace{})
	if err != nil || artifact != nil {
		t.Fatalf("unmanaged workspace unexpectedly required source or project configuration: %#v %v", artifact, err)
	}
}

func TestSkillContentParticipatesInDeploymentSnapshot(t *testing.T) {
	workspace := skillWorkspace(t)
	syncWorkspaceSkills(t, workspace)
	before, err := ComputeDeploymentSnapshot(workspace, "dev")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace.Root, "skills", "greeting", "SKILL.md")
	if err := os.WriteFile(path, []byte(hostedTestSkill+"Changed content.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ComputeDeploymentSnapshot(workspace, "dev"); err == nil {
		t.Fatal("snapshot accepted stale synchronization")
	}
	syncWorkspaceSkills(t, workspace)
	after, err := ComputeDeploymentSnapshot(workspace, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if before.Hash == after.Hash {
		t.Fatal("changed Skill content did not change deployment identity")
	}
}

func TestImageSnapshotBindsEvidenceAndValidatesLocalArtifacts(t *testing.T) {
	workspace := skillWorkspace(t)
	workspace.Selected.Mode = DeploymentModeImage
	workspace.Selected.Code = nil
	workspace.Selected.Image = "registry.example/agent@sha256:" + strings.Repeat("a", 64)
	workspace.Selected.Skills.Image = &hostedskills.ImageEvidence{
		Reference: workspace.Selected.Image, IntegrationEvidence: "image-evidence.txt",
	}
	evidence := filepath.Join(workspace.Root, "image-evidence.txt")
	if err := os.WriteFile(evidence, []byte("Operator build/integration evidence, not runtime qualification.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := syncWorkspaceSkills(t, workspace)
	before, err := ComputeDeploymentSnapshot(workspace, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if before.FileCount != 0 || artifact.RuntimeVerified {
		t.Fatal("local image evidence must not imply files were injected or the provider is ready")
	}
	if err := os.WriteFile(evidence, []byte("Updated operator integration evidence.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ComputeDeploymentSnapshot(workspace, "dev"); err == nil {
		t.Fatal("image snapshot accepted stale evidence")
	}
	syncWorkspaceSkills(t, workspace)
	after, err := ComputeDeploymentSnapshot(workspace, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if before.Hash == after.Hash {
		t.Fatal("image evidence did not affect deployment identity")
	}
	if err := os.WriteFile(filepath.Join(artifact.Directory, "greeting", "SKILL.md"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ComputeDeploymentSnapshot(workspace, "dev"); err == nil {
		t.Fatal("image snapshot bypassed managed artifact integrity")
	}
}

func TestContainerSkillContextRejectsExcludedAndOutOfContextArtifacts(t *testing.T) {
	for _, setup := range []string{"root-ignore", "root-ignore-bom", "globstar-root", "normalized-root", "dockerfile-ignore", "dockerfile-ignore-bom", "nested-context", "unsupported-negation", "unsupported-negation-bom"} {
		t.Run(setup, func(t *testing.T) {
			root := writeWorkspace(t, `name: skill-container
services:
  agent:
    host: azure.ai.agent
    kind: hosted
    project: src
`, map[string]string{"src/Dockerfile": "FROM example.invalid/base\nCOPY . /app\n", "src/nested/keep": "keep"})
			writeHostedSkillInputs(t, root)
			workspace, err := LoadWorkspace(root, "")
			if err != nil {
				t.Fatal(err)
			}
			syncWorkspaceSkills(t, workspace)
			switch setup {
			case "globstar-root", "normalized-root":
				rule := "**/fam_skills\n"
				if setup == "normalized-root" {
					rule = "unused/../fam_skills\n"
				}
				if err := os.WriteFile(filepath.Join(root, "src", ".dockerignore"), []byte(rule), 0o600); err != nil {
					t.Fatal(err)
				}
			case "root-ignore", "root-ignore-bom":
				rule := "fam_skills\n"
				if setup == "root-ignore-bom" {
					rule = "\ufeff" + rule
				}
				if err := os.WriteFile(filepath.Join(root, "src", ".dockerignore"), []byte(rule), 0o600); err != nil {
					t.Fatal(err)
				}
			case "dockerfile-ignore", "dockerfile-ignore-bom":
				rule := "**/SKILL.md\n"
				if setup == "dockerfile-ignore-bom" {
					rule = "\ufefffam_skills\n"
				}
				if err := os.WriteFile(filepath.Join(root, "src", "Dockerfile.dockerignore"), []byte(rule), 0o600); err != nil {
					t.Fatal(err)
				}
			case "nested-context":
				services, _ := asMap(workspace.resolvedDocument["services"])
				service, _ := asMap(services["agent"])
				service["docker"] = map[string]any{"context": "nested"}
			case "unsupported-negation", "unsupported-negation-bom":
				rule := "*\n!fam_skills\n"
				if setup == "unsupported-negation-bom" {
					rule = "\ufeff!fam_skills\n"
				}
				if err := os.WriteFile(filepath.Join(root, "src", ".dockerignore"), []byte(rule), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := ComputeDeploymentSnapshot(workspace, "dev"); err == nil {
				t.Fatal("snapshot accepted unverified Docker packaging")
			}
			if _, err := ValidateSkillArtifacts(workspace); err == nil {
				t.Fatal("preflight validation accepted unverified Docker packaging")
			}
		})
	}
}

func TestSourceIgnoreBOMHandling(t *testing.T) {
	for _, test := range []struct {
		name     string
		data     string
		docker   bool
		patterns int
		ignored  bool
		wantErr  string
	}{
		{name: "docker-leading", data: "\ufefffam_skills\n", docker: true, patterns: 1, ignored: true},
		{name: "docker-comment", data: "\ufeff# comment\nfam_skills\n", docker: true, patterns: 1, ignored: true},
		{name: "docker-negation", data: "\ufeff!fam_skills\n", docker: true, wantErr: "unsupported negation"},
		{name: "docker-containment", data: "\ufeff../fam_skills\n", docker: true, wantErr: "unsafe path"},
		{name: "docker-only-one", data: "\ufeff\ufefffam_skills\n", docker: true, patterns: 1},
		{name: "docker-not-leading", data: "\n\ufefffam_skills\n", docker: true, patterns: 1},
		{name: "agent-leading-preserved", data: "\ufefffam_skills\n", patterns: 1},
		{name: "agent-ordinary", data: "fam_skills\n", patterns: 1, ignored: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			patterns, err := parseSourceIgnore([]byte(test.data), test.docker)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("expected %s, got %v", test.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(patterns) != test.patterns || archivePathIgnored("fam_skills", true, patterns) != test.ignored {
				t.Fatalf("unexpected BOM interpretation: %#v", patterns)
			}
		})
	}
}
