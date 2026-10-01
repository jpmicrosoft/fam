package skills

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/netcheck"
)

func LoadDirectory(baseDir, relativePath string) (Package, error) {
	if relativePath == "" {
		return Package{}, errs.Config("skill directory path is required")
	}
	basePath, err := filepath.Abs(baseDir)
	if err != nil {
		return Package{}, errs.Security("cannot resolve skill base directory: %v", err)
	}
	relativePath = filepath.FromSlash(strings.ReplaceAll(relativePath, `\`, "/"))
	if strings.ContainsRune(relativePath, ':') {
		return Package{}, errs.Security("skill directory must not contain drive or alternate data stream syntax")
	}
	if _, err := netcheck.RequireContainedFile(basePath, relativePath, "skill directory"); err != nil {
		return Package{}, err
	}
	base, err := os.OpenRoot(basePath)
	if err != nil {
		return Package{}, errs.Security("cannot open skill base directory safely: %v", err)
	}
	defer base.Close()
	relativePath = filepath.Clean(relativePath)
	current := "."
	for _, component := range strings.Split(relativePath, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := base.Lstat(current)
		if err != nil {
			return Package{}, errs.Config("cannot inspect skill directory: %v", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return Package{}, errs.Security("skill directory must not contain symbolic links")
		}
	}
	root, err := base.OpenRoot(relativePath)
	if err != nil {
		return Package{}, errs.Security("cannot open contained skill directory safely: %v", err)
	}
	defer root.Close()
	content, err := readDirectory(root)
	if err != nil {
		return Package{}, err
	}
	pkg, err := Parse(content)
	if err != nil {
		return Package{}, err
	}
	directoryName := filepath.Base(filepath.Join(basePath, relativePath))
	if directoryName != pkg.Name {
		return Package{}, errs.Config("skill directory name must match the SKILL.md name %q", pkg.Name)
	}
	return pkg, nil
}

func readDirectory(root *os.Root) ([]byte, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, errs.Security("cannot inspect contained skill directory safely: %v", err)
	}
	// Reading at most two entries is enough to reject all supporting content.
	entries, readErr := directory.ReadDir(2)
	closeErr := directory.Close()
	if readErr != nil && readErr != io.EOF {
		return nil, errs.Config("cannot list skill directory: %v", readErr)
	}
	if closeErr != nil {
		return nil, errs.Config("cannot close skill directory: %v", closeErr)
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, errs.Security("skill packages must not contain symbolic links")
		}
	}
	if len(entries) != 1 || entries[0].Name() != "SKILL.md" {
		return nil, errs.Config("instructions-only skill directories must contain only a root SKILL.md; supporting files and directories are not supported")
	}
	info, err := root.Lstat("SKILL.md")
	if err != nil {
		return nil, errs.Config("cannot inspect SKILL.md: %v", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errs.Security("SKILL.md must be a regular file, not a link or directory")
	}
	file, err := root.Open("SKILL.md")
	if err != nil {
		return nil, errs.Security("cannot open contained SKILL.md safely: %v", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, errs.Config("cannot inspect opened SKILL.md: %v", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, errs.Security("SKILL.md changed while it was being opened; retry with a stable package")
	}
	if openedInfo.Size() > maxSkillFileBytes {
		return nil, errs.Config("SKILL.md exceeds the %d byte FAM file safety guard (not an Azure quota)", maxSkillFileBytes)
	}
	content, err := readContent(file, maxSkillFileBytes)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) != openedInfo.Size() {
		return nil, errs.Security("SKILL.md changed while it was being read; retry with a stable package")
	}
	return content, nil
}

func ReadArchive(content []byte) (Package, error) {
	return readArchive(content, maxSkillArchiveBytes, maxSkillFileBytes)
}

func readArchive(content []byte, archiveLimit, fileLimit int64) (Package, error) {
	if int64(len(content)) > archiveLimit {
		return Package{}, errs.Config("skill ZIP exceeds the %d byte FAM archive safety guard (not an Azure quota)", archiveLimit)
	}
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err == zip.ErrInsecurePath {
		return Package{}, errs.Security("skill ZIP contains unsafe entry paths")
	}
	if err != nil {
		return Package{}, errs.Config("cannot read skill ZIP: %v", err)
	}
	seen := make(map[string]bool)
	for _, file := range archive.File {
		if err := validateArchivePath(file.Name); err != nil {
			return Package{}, err
		}
		key := strings.ToLower(file.Name)
		if seen[key] {
			return Package{}, errs.Security("skill ZIP contains duplicate or case-colliding entries")
		}
		seen[key] = true
		if !file.Mode().IsRegular() {
			return Package{}, errs.Security("instructions-only skill ZIP entries must be regular files, not links or directories")
		}
	}
	if len(archive.File) != 1 || archive.File[0].Name != "SKILL.md" {
		return Package{}, errs.Config("instructions-only skill ZIPs must contain only a root SKILL.md; wrapped directories and supporting files are not supported")
	}
	file := archive.File[0]
	if file.UncompressedSize64 > uint64(fileLimit) {
		return Package{}, errs.Config("SKILL.md exceeds the %d byte FAM file safety guard (not an Azure quota)", fileLimit)
	}
	reader, err := file.Open()
	if err != nil {
		return Package{}, errs.Config("cannot open SKILL.md in skill ZIP: %v", err)
	}
	data, readErr := readContent(reader, fileLimit)
	closeErr := reader.Close()
	if readErr != nil {
		return Package{}, readErr
	}
	if closeErr != nil {
		return Package{}, errs.Config("cannot close SKILL.md in skill ZIP: %v", closeErr)
	}
	return Parse(data)
}

func validateArchivePath(name string) error {
	if name == "" || !utf8.ValidString(name) || strings.HasPrefix(name, "/") ||
		strings.ContainsAny(name, `\:`) || strings.ContainsFunc(name, unicode.IsControl) {
		return errs.Security("skill ZIP contains an unsafe entry path")
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." ||
			strings.TrimRight(component, ". ") != component {
			return errs.Security("skill ZIP contains a noncanonical or traversing entry path")
		}
	}
	return nil
}

func readContent(reader io.Reader, limit int64) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, errs.Config("cannot read complete SKILL.md content: %v", err)
	}
	if int64(len(content)) > limit {
		return nil, errs.Config("SKILL.md exceeds the %d byte FAM file safety guard (not an Azure quota)", limit)
	}
	return content, nil
}
