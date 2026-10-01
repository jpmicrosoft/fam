package hostedskills

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestImageSkillDetachmentRequiresRebuild(t *testing.T) {
	for _, mode := range []string{ModeBundle, ModeMCP} {
		t.Run(mode, func(t *testing.T) {
			options, _ := remoteOptions(t, mode)
			options.Image = "registry.example/agent@sha256:" + strings.Repeat("a", 64)
			options.Config.Image = &ImageEvidence{Reference: options.Image, IntegrationEvidence: "build-evidence.txt"}
			options.RuntimeFiles = map[string]string{"fam_skills/runtime.py": "VERSION = 1\n"}
			writeTestFile(t, options.Root, "build-evidence.txt", "Explicit image integration record.\n")
			original := mustSync(t, options)
			before, err := os.ReadFile(original.ManifestPath)
			if err != nil {
				t.Fatal(err)
			}
			options.Config.Skills = []Source{}
			if _, err := Sync(context.Background(), options); err == nil || !strings.Contains(err.Error(), "rebuild") {
				t.Fatalf("clearing Skills reused the immutable image: %v", err)
			}
			after, err := os.ReadFile(original.ManifestPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected detach replaced the prior artifact: %v", err)
			}
			options.Image = "registry.example/agent@sha256:" + strings.Repeat("b", 64)
			options.Config.Image.Reference = options.Image
			detached := mustSync(t, options)
			if detached.Manifest.ImageReference != options.Image || len(detached.Manifest.Skills) != 0 ||
				detached.RuntimeVerified || detached.State != "operator-integration-evidence" {
				t.Fatalf("image detach lost its evidence-only binding: %#v", detached)
			}
			options.RuntimeFiles["fam_skills/runtime.py"] = "VERSION = 2\n"
			if _, err := Sync(context.Background(), options); err == nil || !strings.Contains(err.Error(), "rebuild") {
				t.Fatalf("detached image accepted changed helper without rebuilding: %v", err)
			}
		})
	}
}

func TestImageEmptyDeclarationStillRequiresEvidence(t *testing.T) {
	options := localOptions(t)
	options.Config.Skills = []Source{}
	options.Image = "registry.example/agent@sha256:" + strings.Repeat("a", 64)
	if _, err := Sync(context.Background(), options); err == nil {
		t.Fatal("empty image declaration bypassed integration evidence")
	}
}

func TestImageSyncRejectsStaleBaselineAfterConcurrentDetachment(t *testing.T) {
	for _, mode := range []string{ModeBundle, ModeMCP} {
		t.Run(mode, func(t *testing.T) {
			options, client := remoteOptions(t, mode)
			options.Image = "registry.example/agent@sha256:" + strings.Repeat("a", 64)
			options.Config.Image = &ImageEvidence{Reference: options.Image, IntegrationEvidence: "build-evidence.txt"}
			options.RuntimeFiles = map[string]string{"fam_skills/runtime.py": "VERSION = 1\n"}
			writeTestFile(t, options.Root, "build-evidence.txt", "Explicit image integration record.\n")
			mustSync(t, options)

			options.Image = "registry.example/agent@sha256:" + strings.Repeat("b", 64)
			options.Config.Image.Reference = options.Image
			detach := options
			config := *options.Config
			config.Skills = []Source{}
			detach.Config, detach.Client = &config, nil
			var inner *Artifact
			client.calls = nil
			client.afterDownload = func() {
				inner = mustSync(t, detach)
			}
			result, err := Sync(context.Background(), options)
			if err == nil || !strings.Contains(err.Error(), "changed during synchronization") || result != nil {
				t.Fatalf("outer sync accepted a stale image baseline: %#v %v", result, err)
			}
			if inner == nil || len(inner.Manifest.Skills) != 0 || inner.Manifest.ImageReference != options.Image {
				t.Fatalf("inner image detachment did not complete: %#v", inner)
			}
			wantCalls := 2
			if mode == ModeMCP {
				wantCalls++
			}
			if len(client.calls) != wantCalls {
				t.Fatalf("conflicting sync retried provider reads: %#v", client.calls)
			}
			preserved, err := ValidateArtifact(validationOptions(detach))
			if err != nil || preserved.SHA256 != inner.SHA256 {
				t.Fatalf("outer sync changed inner detachment or left a transaction: %#v %v", preserved, err)
			}
			client.afterDownload = nil
			if _, err := Sync(context.Background(), options); err == nil || !strings.Contains(err.Error(), "rebuild") {
				t.Fatalf("reattaching Skills reused the detached immutable image: %v", err)
			}
		})
	}
}
