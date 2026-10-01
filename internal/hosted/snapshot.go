package hosted

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/hostedskills"
	"foundry-agent-manager/internal/netcheck"
)

const (
	MaxCodeArchiveBytes = int64(250 << 20)
	maxSnapshotFiles    = 100000
)

type DeploymentSnapshot struct {
	Hash      string `json:"hash" yaml:"hash"`
	FileCount int    `json:"fileCount" yaml:"fileCount"`
	Bytes     int64  `json:"bytes" yaml:"bytes"`
}

type CodeArchive struct {
	Path      string `json:"path" yaml:"path"`
	SHA256    string `json:"sha256" yaml:"sha256"`
	Size      int64  `json:"size" yaml:"size"`
	FileCount int    `json:"fileCount" yaml:"fileCount"`
}

func (a CodeArchive) Remove() {
	if a.Path != "" {
		_ = os.Remove(a.Path)
	}
}

func ComputeDeploymentSnapshot(workspace Workspace, environment string) (DeploymentSnapshot, error) {
	digest := sha256.New()
	hashString(digest, "foundry-agent-manager/hosted-deployment/v1")
	hashString(digest, workspace.Hash)
	hashString(digest, workspace.Name)
	hashString(digest, workspace.Selected.ServiceName)
	hashString(digest, workspace.Selected.AgentName)
	hashString(digest, string(workspace.Selected.Mode))
	hashString(digest, environment)
	if code := workspace.Selected.Code; workspace.Selected.Mode == DeploymentModeCode && code != nil {
		hashString(digest, "code-command/v1")
		hashString(digest, code.Runtime)
		hashString(digest, code.DependencyResolution)
		for _, arg := range code.EntryPoint {
			hashString(digest, arg)
		}
	}

	artifact, err := validateSkillArtifacts(workspace)
	if err != nil {
		return DeploymentSnapshot{}, err
	}
	if artifact != nil {
		hashString(digest, artifact.Manifest.DeclarationHash)
		hashString(digest, artifact.SHA256)
	}
	if workspace.Selected.Mode == DeploymentModeImage {
		hashString(digest, workspace.Selected.Image)
		return DeploymentSnapshot{Hash: hex.EncodeToString(digest.Sum(nil))}, nil
	}
	files, err := hostedSourceFiles(workspace.Selected.SourceDirectory, true)
	if err != nil {
		return DeploymentSnapshot{}, err
	}
	if err := requireSkillFiles(files, artifact); err != nil {
		return DeploymentSnapshot{}, err
	}
	if err := requireCodeFiles(workspace.Selected, files); err != nil {
		return DeploymentSnapshot{}, err
	}
	root, err := os.OpenRoot(workspace.Selected.SourceDirectory)
	if err != nil {
		return DeploymentSnapshot{}, errs.Security("failed to open Hosted Agent source safely: %v", err)
	}
	defer root.Close()

	result := DeploymentSnapshot{FileCount: len(files)}
	for _, file := range files {
		opened, err := root.Open(filepath.ToSlash(file.relative))
		if err != nil {
			return DeploymentSnapshot{}, errs.Security("failed to open Hosted Agent source file %q safely: %v", file.relative, err)
		}
		info, err := opened.Stat()
		if err != nil {
			opened.Close()
			return DeploymentSnapshot{}, errs.Config("failed to inspect Hosted Agent source file %q: %v", file.relative, err)
		}
		if !info.Mode().IsRegular() {
			opened.Close()
			return DeploymentSnapshot{}, errs.Security("Hosted Agent source file %q is no longer a regular file", file.relative)
		}
		hashString(digest, filepath.ToSlash(file.relative))
		hashString(digest, fmt.Sprintf("%o", info.Mode().Perm()&0o111))
		fileDigest := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(digest, fileDigest), opened)
		closeErr := opened.Close()
		if copyErr != nil {
			return DeploymentSnapshot{}, errs.Config("failed to hash Hosted Agent source file %q: %v", file.relative, copyErr)
		}
		if closeErr != nil {
			return DeploymentSnapshot{}, errs.Config("failed to close Hosted Agent source file %q: %v", file.relative, closeErr)
		}
		if err := verifySkillFile(artifact, file.relative, fileDigest); err != nil {
			return DeploymentSnapshot{}, err
		}
		result.Bytes += written
	}
	result.Hash = hex.EncodeToString(digest.Sum(nil))
	return result, nil
}

func BuildCodeArchive(workspace Workspace) (CodeArchive, error) {
	if workspace.Selected.Mode != DeploymentModeCode {
		return CodeArchive{}, errs.Config("Hosted code archives require a codeConfiguration deployment")
	}
	artifact, err := validateSkillArtifacts(workspace)
	if err != nil {
		return CodeArchive{}, err
	}
	files, err := hostedSourceFiles(workspace.Selected.SourceDirectory, true)
	if err != nil {
		return CodeArchive{}, err
	}
	if err := requireSkillFiles(files, artifact); err != nil {
		return CodeArchive{}, err
	}
	if err := requireCodeFiles(workspace.Selected, files); err != nil {
		return CodeArchive{}, err
	}
	archiveFile, err := os.CreateTemp("", "foundry-agent-manager-hosted-*.zip")
	if err != nil {
		return CodeArchive{}, errs.Config("failed to create Hosted Agent code archive: %v", err)
	}
	archivePath := archiveFile.Name()
	cleanup := func() {
		_ = archiveFile.Close()
		_ = os.Remove(archivePath)
	}
	if err := archiveFile.Chmod(0o600); err != nil {
		cleanup()
		return CodeArchive{}, errs.Config("failed to secure Hosted Agent code archive: %v", err)
	}
	root, err := os.OpenRoot(workspace.Selected.SourceDirectory)
	if err != nil {
		cleanup()
		return CodeArchive{}, errs.Security("failed to open Hosted Agent source safely: %v", err)
	}
	defer root.Close()

	zipWriter := zip.NewWriter(archiveFile)
	for _, file := range files {
		opened, err := root.Open(filepath.ToSlash(file.relative))
		if err != nil {
			cleanup()
			return CodeArchive{}, errs.Security("failed to open Hosted Agent source file %q safely: %v", file.relative, err)
		}
		info, err := opened.Stat()
		if err != nil {
			opened.Close()
			cleanup()
			return CodeArchive{}, errs.Config("failed to inspect Hosted Agent source file %q: %v", file.relative, err)
		}
		if !info.Mode().IsRegular() {
			opened.Close()
			cleanup()
			return CodeArchive{}, errs.Security("Hosted Agent source file %q is no longer a regular file", file.relative)
		}
		header := &zip.FileHeader{
			Name:   filepath.ToSlash(file.relative),
			Method: zip.Deflate,
		}
		header.SetModTime(time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC))
		if info.Mode().Perm()&0o111 != 0 {
			header.SetMode(0o755)
		} else {
			header.SetMode(0o644)
		}
		part, err := zipWriter.CreateHeader(header)
		if err != nil {
			opened.Close()
			cleanup()
			return CodeArchive{}, errs.Config("failed to add %q to Hosted Agent code archive: %v", file.relative, err)
		}
		fileDigest := sha256.New()
		if _, err := io.Copy(io.MultiWriter(part, fileDigest), opened); err != nil {
			opened.Close()
			cleanup()
			return CodeArchive{}, errs.Config("failed to archive Hosted Agent source file %q: %v", file.relative, err)
		}
		if err := opened.Close(); err != nil {
			cleanup()
			return CodeArchive{}, errs.Config("failed to close Hosted Agent source file %q: %v", file.relative, err)
		}
		if err := verifySkillFile(artifact, file.relative, fileDigest); err != nil {
			cleanup()
			return CodeArchive{}, err
		}
	}
	if err := zipWriter.Close(); err != nil {
		cleanup()
		return CodeArchive{}, errs.Config("failed to finalize Hosted Agent code archive: %v", err)
	}
	if err := archiveFile.Sync(); err != nil {
		cleanup()
		return CodeArchive{}, errs.Config("failed to flush Hosted Agent code archive: %v", err)
	}
	if err := archiveFile.Close(); err != nil {
		_ = os.Remove(archivePath)
		return CodeArchive{}, errs.Config("failed to close Hosted Agent code archive: %v", err)
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		_ = os.Remove(archivePath)
		return CodeArchive{}, errs.Config("failed to inspect Hosted Agent code archive: %v", err)
	}
	if info.Size() > MaxCodeArchiveBytes {
		_ = os.Remove(archivePath)
		return CodeArchive{}, errs.Config(
			"Hosted Agent code archive is %d bytes and exceeds the %d byte service limit",
			info.Size(),
			MaxCodeArchiveBytes,
		)
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		_ = os.Remove(archivePath)
		return CodeArchive{}, errs.Config("failed to reopen Hosted Agent code archive: %v", err)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, archive); err != nil {
		archive.Close()
		_ = os.Remove(archivePath)
		return CodeArchive{}, errs.Config("failed to hash Hosted Agent code archive: %v", err)
	}
	if err := archive.Close(); err != nil {
		_ = os.Remove(archivePath)
		return CodeArchive{}, errs.Config("failed to close Hosted Agent code archive after hashing: %v", err)
	}
	return CodeArchive{
		Path:      archivePath,
		SHA256:    hex.EncodeToString(digest.Sum(nil)),
		Size:      info.Size(),
		FileCount: len(files),
	}, nil
}

type hostedSourceFile struct {
	relative string
}

// ValidateSkillArtifacts checks synchronized content and required source/Docker
// packaging without contacting providers or asserting runtime readiness. For
// remote Skills, an empty Selected.ProjectEndpoint defers the project-binding
// check; online preparation must repeat this with the resolved endpoint.
func ValidateSkillArtifacts(workspace Workspace) (*hostedskills.Artifact, error) {
	artifact, err := validateSkillArtifacts(workspace)
	if err != nil || artifact == nil || workspace.Selected.Mode == DeploymentModeImage {
		return artifact, err
	}
	files, err := hostedSourceFiles(workspace.Selected.SourceDirectory, true)
	if err != nil {
		return nil, err
	}
	if err := requireSkillFiles(files, artifact); err != nil {
		return nil, err
	}
	return artifact, nil
}

func validateSkillArtifacts(workspace Workspace) (*hostedskills.Artifact, error) {
	projectEndpoint := ""
	if workspace.Selected.Skills.RequiresRemote() {
		projectEndpoint = workspace.Selected.ProjectEndpoint
	}
	artifact, err := hostedskills.ValidateArtifact(hostedskills.ValidateOptions{
		Root:            workspace.Root,
		SourceDirectory: workspace.Selected.SourceDirectory,
		Service:         workspace.Selected.ServiceName,
		Config:          workspace.Selected.Skills,
		ProjectEndpoint: projectEndpoint,
		Image:           workspace.Selected.Image,
	})
	if err != nil {
		return nil, err
	}
	if artifact != nil && workspace.Selected.Mode == DeploymentModeDocker {
		if err := validateContainerSkillContext(workspace, artifact); err != nil {
			return nil, err
		}
	}
	return artifact, nil
}

func validateContainerSkillContext(workspace Workspace, artifact *hostedskills.Artifact) error {
	services, ok := asMap(workspace.resolvedDocument["services"])
	if !ok {
		return errs.Config("Hosted Skill container packaging requires a freshly loaded workspace")
	}
	service, ok := asMap(services[workspace.Selected.ServiceName])
	if !ok {
		return errs.Config("cannot inspect the selected Hosted Skill Docker context")
	}
	dockerfile, contextDirectory, err := dockerPaths(workspace.Selected.ServiceName, service["docker"])
	if err != nil {
		return err
	}
	if filepath.Clean(contextDirectory) != "." {
		return errs.Config("Hosted Skill artifacts live directly under the service source; docker.context must include that root rather than a nested directory")
	}
	root, err := os.OpenRoot(workspace.Selected.SourceDirectory)
	if err != nil {
		return errs.Security("cannot inspect Hosted Skill Docker ignore rules: %v", err)
	}
	defer root.Close()
	ignoreFile := ".dockerignore"
	if _, err := root.Lstat(dockerfile + ".dockerignore"); err == nil {
		ignoreFile = dockerfile + ".dockerignore"
	} else if !os.IsNotExist(err) {
		return errs.Config("cannot inspect Dockerfile-specific ignore rules: %v", err)
	}
	if _, err := root.Lstat(ignoreFile); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return errs.Config("cannot inspect %s: %v", ignoreFile, err)
	}
	data, err := netcheck.ReadContainedFile(workspace.Selected.SourceDirectory, ignoreFile, "Hosted Skill Docker ignore rules")
	if err != nil {
		return err
	}
	// Only the common positive-glob subset is statically provable here. Do not
	// silently reinterpret Docker negation or platform-dependent escape rules.
	patterns, err := parseSourceIgnore(data, true)
	if err != nil {
		return errs.Config("cannot verify Hosted Skill packaging with %s: %s", ignoreFile, strings.ReplaceAll(err.Error(), ".agentignore", ignoreFile))
	}
	for _, required := range artifact.RequiredFiles {
		for candidate := required; candidate != "."; candidate = path.Dir(candidate) {
			if archivePathIgnored(candidate, candidate != required, patterns) {
				return errs.Config("required Hosted Skill artifact %q is excluded by %s", required, ignoreFile)
			}
		}
	}
	return nil
}

func requireSkillFiles(files []hostedSourceFile, artifact *hostedskills.Artifact) error {
	if artifact == nil {
		return nil
	}
	included := make(map[string]bool, len(files))
	for _, file := range files {
		relative := filepath.ToSlash(file.relative)
		if err := requireKnownSkillFile(artifact, relative); err != nil {
			return err
		}
		included[relative] = true
	}
	for _, required := range artifact.RequiredFiles {
		if !included[required] {
			return errs.Config("required Hosted Skill artifact %q is excluded from deployable source; remove the conflicting .agentignore rule", required)
		}
	}
	return nil
}

func requireKnownSkillFile(artifact *hostedskills.Artifact, relative string) error {
	if artifact == nil {
		return nil
	}
	relative = filepath.ToSlash(relative)
	top, _, _ := strings.Cut(relative, "/")
	if strings.EqualFold(top, hostedskills.DirectoryName) {
		if _, known := artifact.FileSHA256[relative]; !known {
			return errs.Config("Hosted Skill artifact %q is not in the validated inventory; retry with stable synchronized output", relative)
		}
	}
	return nil
}

func verifySkillFile(artifact *hostedskills.Artifact, relative string, digest hash.Hash) error {
	if err := requireKnownSkillFile(artifact, relative); err != nil {
		return err
	}
	if artifact != nil {
		if expected, managed := artifact.FileSHA256[filepath.ToSlash(relative)]; managed &&
			hex.EncodeToString(digest.Sum(nil)) != expected {
			return errs.Config("Hosted Skill artifact %q changed during packaging; retry with stable synchronized output", relative)
		}
	}
	return nil
}

type agentIgnorePattern struct {
	raw       string
	directory bool
	matcher   *regexp.Regexp
}

func hostedSourceFiles(root string, applyAgentIgnore bool) ([]hostedSourceFile, error) {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return nil, errs.Security("failed to resolve Hosted Agent source directory: %v", err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbsolute)
	if err != nil {
		return nil, errs.Security("failed to resolve Hosted Agent source directory links: %v", err)
	}
	var ignorePatterns []agentIgnorePattern
	if applyAgentIgnore {
		ignorePatterns, err = loadAgentIgnore(rootReal)
		if err != nil {
			return nil, err
		}
	}
	files := make([]hostedSourceFile, 0)
	err = filepath.WalkDir(rootReal, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(rootReal, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		normalized := filepath.ToSlash(relative)
		top := strings.SplitN(normalized, "/", 2)[0]
		if entry.IsDir() && (top == ".git" || top == ".foundry-agent-manager" || top == ".azure") {
			return filepath.SkipDir
		}
		resolvedPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return errs.Security("failed to resolve Hosted Agent source path %q: %v", relative, err)
		}
		resolvedRelative, err := filepath.Rel(rootReal, resolvedPath)
		if err != nil ||
			resolvedRelative == ".." ||
			strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) ||
			filepath.IsAbs(resolvedRelative) {
			return errs.Security("Hosted Agent source path %q escapes the source directory", relative)
		}
		if applyAgentIgnore && archivePathIgnored(normalized, entry.IsDir(), ignorePatterns) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errs.Security("Hosted Agent source contains unsupported symbolic link %q", relative)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errs.Security("Hosted Agent source contains unsupported non-regular file %q", relative)
		}
		files = append(files, hostedSourceFile{relative: filepath.Clean(relative)})
		if len(files) > maxSnapshotFiles {
			return errs.Config("Hosted Agent source exceeds the %d file safety limit", maxSnapshotFiles)
		}
		return nil
	})
	if err != nil {
		if errs.IsKind(err, "security") || errs.IsKind(err, "config") {
			return nil, err
		}
		return nil, errs.Config("failed to enumerate Hosted Agent source: %v", err)
	}
	sort.Slice(files, func(i, j int) bool {
		return filepath.ToSlash(files[i].relative) < filepath.ToSlash(files[j].relative)
	})
	return files, nil
}

func loadAgentIgnore(root string) ([]agentIgnorePattern, error) {
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, errs.Security("failed to open Hosted Agent source for .agentignore: %v", err)
	}
	defer rootHandle.Close()
	file, err := rootHandle.Open(".agentignore")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errs.Config("failed to read .agentignore: %v", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, errs.Config("failed to inspect .agentignore: %v", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errs.Security(".agentignore must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, errs.Config("failed to read .agentignore: %v", err)
	}
	if len(data) > 1<<20 {
		return nil, errs.Config(".agentignore exceeds the 1 MiB safety limit")
	}
	return parseAgentIgnore(data)
}

func parseAgentIgnore(data []byte) ([]agentIgnorePattern, error) {
	return parseSourceIgnore(data, false)
}

func parseSourceIgnore(data []byte, docker bool) ([]agentIgnorePattern, error) {
	content := string(data)
	if docker {
		content = strings.TrimPrefix(content, "\ufeff")
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	patterns := make([]agentIgnorePattern, 0, len(lines))
	for lineNumber, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "!") {
			return nil, errs.Config(
				".agentignore line %d uses unsupported negation; remove the ! rule",
				lineNumber+1,
			)
		}
		if strings.ContainsAny(line, "[]\\") {
			return nil, errs.Config(
				".agentignore line %d uses unsupported bracket or escape syntax",
				lineNumber+1,
			)
		}
		directory := strings.HasSuffix(line, "/")
		anchored := strings.HasPrefix(line, "/")
		line = strings.TrimSuffix(strings.TrimPrefix(line, "/"), "/")
		if docker {
			line = path.Clean(strings.Trim(line, "/"))
			if line == "." {
				continue
			}
			directory, anchored = false, true
		}
		if line == "" || path.Clean(line) == ".." || strings.HasPrefix(path.Clean(line), "../") {
			return nil, errs.Security(".agentignore line %d contains an unsafe path", lineNumber+1)
		}
		expression := agentIgnoreExpression(line, directory, anchored, docker)
		matcher, err := regexp.Compile(expression)
		if err != nil {
			return nil, errs.Config(".agentignore line %d is invalid: %v", lineNumber+1, err)
		}
		patterns = append(patterns, agentIgnorePattern{
			raw:       line,
			directory: directory,
			matcher:   matcher,
		})
	}
	return patterns, nil
}

func agentIgnoreExpression(pattern string, directory, anchored, zeroDepthGlobstar bool) string {
	var expression strings.Builder
	if anchored || strings.Contains(pattern, "/") {
		expression.WriteString("^")
	} else {
		expression.WriteString(`(^|.*/)`)
	}
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if zeroDepthGlobstar && i+2 < len(pattern) && pattern[i+2] == '/' {
					expression.WriteString(`(?:.*/)?`)
					i += 2
				} else {
					expression.WriteString(".*")
					i++
				}
			} else {
				expression.WriteString(`[^/]*`)
			}
		case '?':
			expression.WriteString(`[^/]`)
		default:
			expression.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	if directory {
		expression.WriteString(`(/.*)?$`)
	} else {
		expression.WriteString("$")
	}
	return expression.String()
}

func archivePathIgnored(
	relative string,
	isDirectory bool,
	patterns []agentIgnorePattern,
) bool {
	segments := strings.Split(relative, "/")
	for _, segment := range segments {
		if segment == ".git" ||
			segment == ".foundry-agent-manager" ||
			segment == ".azure" ||
			segment == ".venv" ||
			segment == "__pycache__" {
			return true
		}
	}
	base := path.Base(relative)
	if base == ".env" || base == ".agentignore" || strings.HasSuffix(base, ".pyc") {
		return true
	}
	for _, pattern := range patterns {
		if pattern.directory && !isDirectory && !strings.Contains(relative, pattern.raw+"/") {
			continue
		}
		if pattern.matcher.MatchString(relative) {
			return true
		}
	}
	return false
}

func hashString(digest hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = io.WriteString(digest, value)
}
