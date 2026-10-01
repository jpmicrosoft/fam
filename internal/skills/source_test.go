package skills

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	errs "foundry-agent-manager/internal/errors"
)

type archiveEntry struct {
	name    string
	content string
	mode    os.FileMode
}

func skillArchive(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	return skillArchiveMethod(t, zip.Store, entries...)
}

func skillArchiveMethod(t *testing.T, method uint16, entries ...archiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: method}
		header.SetMode(entry.mode)
		part, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, entry.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestReadArchiveRootLayoutAndProvenance(t *testing.T) {
	expected, err := Parse([]byte(testSkill))
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []uint16{zip.Store, zip.Deflate} {
		content := skillArchiveMethod(t, method, archiveEntry{"SKILL.md", testSkill, 0o644})
		pkg, err := ReadArchive(content)
		if err != nil {
			t.Fatal(err)
		}
		if pkg.Name != expected.Name || pkg.Description != expected.Description ||
			pkg.Instructions != expected.Instructions || pkg.SHA256 != expected.SHA256 ||
			!bytes.Equal(pkg.Content, expected.Content) {
			t.Fatal("archive provenance must describe SKILL.md, not the ZIP wrapper")
		}
	}
}

func TestReadArchiveRejectsUnsafeEntries(t *testing.T) {
	for _, name := range []string{
		"", "../SKILL.md", "a/../SKILL.md", "/SKILL.md", "//server/share/SKILL.md",
		`C:\SKILL.md`, "C:/SKILL.md", "C:SKILL.md", `\\server\share\SKILL.md`,
		`docs\SKILL.md`, `..\SKILL.md`, "SKILL.md:stream", "./SKILL.md",
		"docs//SKILL.md", "docs./SKILL.md", "SKILL.md ", "SKILL.md\x00",
		string([]byte{0xff}),
	} {
		t.Run(name, func(t *testing.T) {
			content := skillArchive(t, archiveEntry{name, testSkill, 0o644})
			if _, err := ReadArchive(content); !errs.IsKind(err, "security") {
				t.Fatalf("unsafe archive entry must fail with a security error: %v", err)
			}
		})
	}
	for _, mode := range []os.FileMode{os.ModeSymlink, os.ModeNamedPipe, os.ModeSocket, os.ModeDevice, os.ModeDir} {
		t.Run(mode.String(), func(t *testing.T) {
			content := skillArchive(t, archiveEntry{"SKILL.md", testSkill, mode | 0o644})
			if _, err := ReadArchive(content); !errs.IsKind(err, "security") {
				t.Fatalf("nonregular ZIP entry must fail: %v", err)
			}
		})
	}
	for _, second := range []string{"SKILL.md", "skill.md", "Skill.MD"} {
		content := skillArchive(t,
			archiveEntry{"SKILL.md", testSkill, 0o644},
			archiveEntry{second, testSkill, 0o644},
		)
		if _, err := ReadArchive(content); !errs.IsKind(err, "security") {
			t.Fatalf("duplicate/case collision %q must fail: %v", second, err)
		}
	}
}

func TestReadArchiveRejectsUnsupportedLayouts(t *testing.T) {
	for name, entries := range map[string][]archiveEntry{
		"empty":       nil,
		"lowercase":   {{"skill.md", testSkill, 0o644}},
		"wrapped":     {{"docs/SKILL.md", testSkill, 0o644}},
		"mismatched":  {{"other/SKILL.md", testSkill, 0o644}},
		"extra file":  {{"SKILL.md", testSkill, 0o644}, {"references/example.txt", "example", 0o644}},
		"script":      {{"SKILL.md", testSkill, 0o644}, {"run.py", "print('example')", 0o755}},
		"empty dir":   {{"SKILL.md", testSkill, 0o644}, {"scripts/", "", os.ModeDir | 0o755}},
		"bad content": {{"SKILL.md", "# No frontmatter", 0o644}},
	} {
		t.Run(name, func(t *testing.T) {
			pkg, err := ReadArchive(skillArchive(t, entries...))
			if err == nil || pkg.Content != nil {
				t.Fatalf("unsupported layout must not return content: %v", err)
			}
		})
	}
}

func TestReadArchiveRejectsCorruption(t *testing.T) {
	content := skillArchive(t, archiveEntry{"SKILL.md", testSkill, 0o644})
	corrupt := bytes.Clone(content)
	index := bytes.Index(corrupt, []byte("Read and summarize."))
	if index < 0 {
		t.Fatal("stored ZIP test fixture has no instruction body")
	}
	corrupt[index] = 'X'
	wrongSize := bytes.Clone(content)
	central := bytes.Index(wrongSize, []byte("PK\x01\x02"))
	if central < 0 {
		t.Fatal("ZIP test fixture has no central directory")
	}
	binary.LittleEndian.PutUint32(wrongSize[central+24:], uint32(len(testSkill)-1))
	for name, data := range map[string][]byte{
		"not ZIP":    []byte("invalid"),
		"truncated":  content[:len(content)-1],
		"checksum":   corrupt,
		"size lie":   wrongSize,
		"empty data": nil,
	} {
		t.Run(name, func(t *testing.T) {
			pkg, err := ReadArchive(data)
			if !errs.IsKind(err, "config") || pkg.Content != nil {
				t.Fatalf("corrupt ZIP accepted or wrong error: %v", err)
			}
		})
	}
}

func TestReadArchiveBounds(t *testing.T) {
	content := skillArchive(t, archiveEntry{"SKILL.md", testSkill, 0o644})
	if _, err := readArchive(content, int64(len(content)), int64(len(testSkill))); err != nil {
		t.Fatalf("exact compressed and decompressed bounds must pass: %v", err)
	}
	for _, limits := range [][2]int64{
		{int64(len(content) - 1), int64(len(testSkill))},
		{int64(len(content)), int64(len(testSkill) - 1)},
	} {
		if pkg, err := readArchive(content, limits[0], limits[1]); !errs.IsKind(err, "config") || pkg.Content != nil {
			t.Fatalf("one byte beyond a bound must fail: %v", err)
		}
	}
	central := bytes.Index(content, []byte("PK\x01\x02"))
	if central < 0 {
		t.Fatal("ZIP test fixture has no central directory")
	}
	binary.LittleEndian.PutUint32(content[central+24:], maxSkillFileBytes+1)
	if _, err := ReadArchive(content); !errs.IsKind(err, "config") {
		t.Fatalf("declared oversized output must fail before decompression: %v", err)
	}
}

func TestReadArchiveBoundsDecompression(t *testing.T) {
	text := testSkill + strings.Repeat("x", 32<<10)
	content := skillArchiveMethod(t, zip.Deflate, archiveEntry{"SKILL.md", text, 0o644})
	if len(content) >= len(text) {
		t.Fatal("synthetic fixture must expand when decompressed")
	}
	archiveLimit, fileLimit := int64(len(content)), int64(len(text))
	pkg, err := readArchive(content, archiveLimit, fileLimit)
	if err != nil || string(pkg.Content) != text {
		t.Fatalf("exact decompression bound must preserve all content: %v", err)
	}
	if pkg, err := readArchive(content, archiveLimit, fileLimit-1); !errs.IsKind(err, "config") || pkg.Content != nil {
		t.Fatalf("oversized decompressed content must fail: %v", err)
	}
	central := bytes.Index(content, []byte("PK\x01\x02"))
	if central < 0 {
		t.Fatal("ZIP test fixture has no central directory")
	}
	binary.LittleEndian.PutUint32(content[central+24:], uint32(fileLimit-1))
	if pkg, err := readArchive(content, archiveLimit, fileLimit-1); !errs.IsKind(err, "config") || pkg.Content != nil {
		t.Fatalf("a forged header must not hide oversized decompressed content: %v", err)
	}
}

func TestReadContentExactStreamingBounds(t *testing.T) {
	const limit = 32
	for _, count := range []int{limit - 1, limit, limit + 1, limit + 10} {
		reader := strings.NewReader(strings.Repeat("x", count))
		content, err := readContent(reader, limit)
		if count <= limit {
			if err != nil || len(content) != count {
				t.Fatalf("%d bytes must be read completely: %v", count, err)
			}
		} else {
			if !errs.IsKind(err, "config") || content != nil {
				t.Fatalf("%d bytes must be rejected, not truncated: %v", count, err)
			}
			if reader.Len() != count-limit-1 {
				t.Fatal("overflow detection must read only the bound plus one byte")
			}
		}
	}
	failure := errors.New("synthetic read failure")
	if content, err := readContent(iotest.ErrReader(failure), limit); !errs.IsKind(err, "config") || content != nil {
		t.Fatalf("read failure must not return content: %v", err)
	}
}

func writeLocalSkill(t *testing.T, base, name string) string {
	t.Helper()
	directory := filepath.Join(base, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(testSkill), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestLoadDirectory(t *testing.T) {
	base := t.TempDir()
	directory := writeLocalSkill(t, base, "docs")
	for _, source := range []struct{ base, path string }{
		{base, "docs"}, {base, filepath.Join(".", "docs")}, {directory, "."},
	} {
		pkg, err := LoadDirectory(source.base, source.path)
		if err != nil || pkg.Name != "docs" || string(pkg.Content) != testSkill {
			t.Fatalf("contained skill directory must load: %v", err)
		}
	}
}

func TestLoadDirectoryRejectsPathsAndNameMismatch(t *testing.T) {
	base := t.TempDir()
	writeLocalSkill(t, base, "docs")
	for _, path := range []string{
		"../docs", `..\docs`, "/docs", `C:\docs`, "C:docs", `\\server\share\docs`,
		"docs:stream", filepath.Join(base, "docs"),
	} {
		if _, err := LoadDirectory(base, path); !errs.IsKind(err, "security") {
			t.Fatalf("unsafe directory path %q must fail: %v", path, err)
		}
	}
	for _, path := range []string{"", "missing", filepath.Join("docs", "SKILL.md")} {
		if _, err := LoadDirectory(base, path); err == nil {
			t.Fatalf("non-directory path %q accepted", path)
		}
	}
	writeLocalSkill(t, base, "other")
	if _, err := LoadDirectory(base, "other"); !errs.IsKind(err, "config") {
		t.Fatalf("directory and frontmatter names must match: %v", err)
	}
}

func TestLoadDirectoryRejectsSupportingContent(t *testing.T) {
	for _, extra := range []string{"reference.txt", "run.py", "scripts"} {
		t.Run(extra, func(t *testing.T) {
			base := t.TempDir()
			directory := writeLocalSkill(t, base, "docs")
			path := filepath.Join(directory, extra)
			if extra == "scripts" {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("example"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadDirectory(base, "docs"); !errs.IsKind(err, "config") {
				t.Fatalf("supporting content must fail: %v", err)
			}
		})
	}
}

func TestLoadDirectoryRejectsMissingOrNonregularSkill(t *testing.T) {
	base := t.TempDir()
	directory := filepath.Join(base, "docs")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDirectory(base, "docs"); !errs.IsKind(err, "config") {
		t.Fatalf("missing SKILL.md must fail: %v", err)
	}
	if err := os.Mkdir(filepath.Join(directory, "SKILL.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDirectory(base, "docs"); !errs.IsKind(err, "security") {
		t.Fatalf("nonregular SKILL.md must fail: %v", err)
	}
}

func TestLoadDirectoryRejectsLinks(t *testing.T) {
	for _, targetOutside := range []bool{false, true} {
		t.Run(map[bool]string{false: "inside", true: "outside"}[targetOutside], func(t *testing.T) {
			base := t.TempDir()
			targetBase := base
			if targetOutside {
				targetBase = t.TempDir()
			}
			target := writeLocalSkill(t, targetBase, "original")
			if err := os.Symlink(target, filepath.Join(base, "docs")); err != nil {
				t.Skipf("symbolic links unavailable: %v", err)
			}
			if _, err := LoadDirectory(base, "docs"); !errs.IsKind(err, "security") {
				t.Fatalf("directory link must fail: %v", err)
			}
		})
	}
	t.Run("instruction link", func(t *testing.T) {
		base := t.TempDir()
		target := writeLocalSkill(t, base, "original")
		directory := filepath.Join(base, "docs")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(target, "SKILL.md"), filepath.Join(directory, "SKILL.md")); err != nil {
			t.Skipf("symbolic links unavailable: %v", err)
		}
		if _, err := LoadDirectory(base, "docs"); !errs.IsKind(err, "security") {
			t.Fatalf("instruction link must fail: %v", err)
		}
	})
}
