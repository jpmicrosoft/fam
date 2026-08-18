package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkillFilesPreservesContainedRelativePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("# Skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "references", "example.txt"),
		[]byte("example"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	files, err := loadSkillFiles(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, file := range files {
		names[file.Name] = true
	}
	if !names["SKILL.md"] || !names["references/example.txt"] {
		t.Fatalf("unexpected skill file names: %#v", names)
	}
}

func TestLoadSkillFilesRequiresRootManifest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSkillFiles(root, "."); err == nil {
		t.Fatal("expected a directory without root SKILL.md to fail")
	}
}

func TestConnectionCredentialsFileIsParsedWithoutOutputShape(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "credentials.json")
	if err := os.WriteFile(
		path,
		[]byte(`{"client_secret":"sensitive-value","nested":{"token":"other-secret"}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	command, _, err := rootCmd().Find([]string{"connection-create"})
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"credentials-file": path,
		"auth-type":        "OAuth2",
	} {
		if err := command.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	credentials, secrets, err := connectionCredentialsFromFlags(command, base)
	if err != nil {
		t.Fatal(err)
	}
	if credentials["client_secret"] != "sensitive-value" || len(secrets) != 2 {
		t.Fatalf("unexpected parsed credentials or secret registration: %#v %#v", credentials, secrets)
	}
}

func TestManagedConnectorCommandsRequirePreview(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := execute(
		[]string{"connector-list", "-f", manifest, "--output", "json"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 3 || !strings.Contains(stderr.String(), "preview") {
		t.Fatalf("expected preview gate, code=%d stderr=%s", code, stderr.String())
	}

}
