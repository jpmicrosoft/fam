package grounding

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/netcheck"
)

func writeGroundingFile(t *testing.T, base, relative, contents string) {
	t.Helper()
	path := filepath.Join(base, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBuildHashesFilesAndStableDesiredState(t *testing.T) {
	base := t.TempDir()
	writeGroundingFile(t, base, "docs/guide.md", "grounding guide")
	writeGroundingFile(t, base, "docs/faq.txt", "frequently asked questions")
	raw := []map[string]interface{}{{
		"name":        "product-docs",
		"description": "Product documentation.",
		"files": []interface{}{
			map[string]interface{}{"path": "docs/guide.md"},
			map[string]interface{}{"path": "docs/faq.txt"},
		},
	}}

	definitions, err := Build(raw, base, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 || len(definitions[0].Files) != 2 {
		t.Fatalf("unexpected definitions: %#v", definitions)
	}
	sum := sha256.Sum256([]byte("grounding guide"))
	if definitions[0].Files[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("unexpected file hash: %#v", definitions[0].Files[0])
	}
	if definitions[0].Files[0].Path != "docs/guide.md" ||
		definitions[0].Files[0].Filename != "guide.md" ||
		definitions[0].Files[0].Size != int64(len("grounding guide")) {
		t.Fatalf("unexpected file metadata: %#v", definitions[0].Files[0])
	}

	reordered := []map[string]interface{}{{
		"name":        "product-docs",
		"description": "Product documentation.",
		"files": []interface{}{
			map[string]interface{}{"path": "docs/faq.txt"},
			map[string]interface{}{"path": "docs/guide.md"},
		},
	}}
	other, err := Build(reordered, base, true)
	if err != nil {
		t.Fatal(err)
	}
	if definitions[0].DesiredHash != other[0].DesiredHash {
		t.Fatalf(
			"desired hash must not depend on file order: %s != %s",
			definitions[0].DesiredHash,
			other[0].DesiredHash,
		)
	}
}

func TestBuildRejectsUnsafeAndInvalidFiles(t *testing.T) {
	tests := []struct {
		name string
		raw  []map[string]interface{}
		kind string
	}{
		{
			name: "parent traversal",
			raw: []map[string]interface{}{{
				"name":  "docs",
				"files": []interface{}{map[string]interface{}{"path": "../outside.txt"}},
			}},
			kind: "security",
		},
		{
			name: "absolute path",
			raw: []map[string]interface{}{{
				"name":  "docs",
				"files": []interface{}{map[string]interface{}{"path": `C:\outside.txt`}},
			}},
			kind: "security",
		},
		{
			name: "unsupported extension",
			raw: []map[string]interface{}{{
				"name":  "docs",
				"files": []interface{}{map[string]interface{}{"path": "payload.exe"}},
			}},
			kind: "manifest",
		},
		{
			name: "duplicate path",
			raw: []map[string]interface{}{{
				"name": "docs",
				"files": []interface{}{
					map[string]interface{}{"path": "guide.txt"},
					map[string]interface{}{"path": "guide.txt"},
				},
			}},
			kind: "manifest",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			if tt.name == "duplicate path" {
				writeGroundingFile(t, base, "guide.txt", "duplicate")
			}
			_, err := Build(tt.raw, base, true)
			if err == nil || !errs.IsKind(err, tt.kind) {
				t.Fatalf("expected %s error, got %v", tt.kind, err)
			}
		})
	}
}

func TestBuildRejectsOversizeFileBeforeReadingIt(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "large.pdf")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(netcheck.MaxGroundingFileBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Build([]map[string]interface{}{{
		"name":  "docs",
		"files": []interface{}{map[string]interface{}{"path": "large.pdf"}},
	}}, base, true)
	if err == nil || !errs.IsKind(err, "manifest") ||
		!strings.Contains(err.Error(), "exceeds the 536870912 byte limit") {
		t.Fatalf("expected size-limit error, got %v", err)
	}
}

func TestBuildRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "linked.txt")); err != nil {
		t.Skipf("symlinks are unavailable on %s: %v", runtime.GOOS, err)
	}
	_, err := Build([]map[string]interface{}{{
		"name":  "docs",
		"files": []interface{}{map[string]interface{}{"path": "linked.txt"}},
	}}, base, true)
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected symlink containment error, got %v", err)
	}
}

func TestMetadataOwnershipAndDesiredState(t *testing.T) {
	definition := VectorStore{Name: "docs", DesiredHash: "desired"}
	metadata := Metadata(definition, definition.DesiredHash)
	if !ManagedStore(metadata, definition.Name) ||
		!DesiredStore(metadata, definition) {
		t.Fatalf("manager metadata was not recognized: %#v", metadata)
	}
	metadata[MetadataDesiredHash] = "other"
	if DesiredStore(metadata, definition) {
		t.Fatal("mismatched desired hash must not be considered synchronized")
	}
	if !ManagedStore(map[string]interface{}{
		MetadataManagedBy:   "true",
		MetadataLogicalName: "other",
	}, "other") {
		t.Fatal("manager ownership metadata was not recognized")
	}
}

func TestManagedUploadFilenameRoundTripsFileIdentity(t *testing.T) {
	file := File{
		Filename: "qualification-document.md",
		PathHash: strings.Repeat("a", 64),
		SHA256:   strings.Repeat("b", 64),
	}
	filename := ManagedUploadFilename(file)
	attributes, ok := FileAttributesFromUploadFilename(filename)
	if !ok {
		t.Fatalf("managed upload filename was not recognized: %q", filename)
	}
	if attributes[AttributePathHash] != file.PathHash ||
		attributes[AttributeSHA256] != file.SHA256 ||
		attributes[AttributeFilename] != file.Filename ||
		!ManagedFile(attributes) {
		t.Fatalf("unexpected reconstructed attributes: %#v", attributes)
	}
}

func TestManagedUploadFilenamePreservesExtensionWhenTruncated(t *testing.T) {
	file := File{
		Filename: strings.Repeat("long-name-", 30) + "guide.md",
		PathHash: strings.Repeat("c", 64),
		SHA256:   strings.Repeat("d", 64),
	}
	filename := ManagedUploadFilename(file)
	if len([]byte(filename)) > len([]byte(managedUploadPrefix))+128+2+maxManagedUploadBaseBytes {
		t.Fatalf("managed upload filename exceeded its bound: %d", len([]byte(filename)))
	}
	if filepath.Ext(filename) != ".md" {
		t.Fatalf("managed upload filename lost its extension: %q", filename)
	}
	if _, ok := FileAttributesFromUploadFilename(filename); !ok {
		t.Fatalf("truncated managed upload filename was not recognized: %q", filename)
	}
}
