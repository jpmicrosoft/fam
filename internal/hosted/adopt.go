package hosted

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"foundry-agent-manager/internal/custommetadata"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundryid"
	"foundry-agent-manager/internal/netcheck"
)

const maxAdoptionFileBytes = int64(32 << 20)

var adoptionIgnoreDefaults = []string{
	".git/",
	".azure/",
	".foundry-agent-manager/",
	".foundry/",
	".agent_configs/",
	".venv/",
	"venv/",
	"__pycache__/",
	".pytest_cache/",
	".mypy_cache/",
	".ruff_cache/",
	"*.pyc",
	".env",
	"eval.yaml",
}

type AdoptOptions struct {
	Source               string
	Destination          string
	InPlace              bool
	AgentName            string
	Protocol             string
	Runtime              string
	EntryPoint           string
	DependencyResolution string
	GuardrailPolicyID    string
	NoGuardrail          bool
	Metadata             map[string]string
}

type AdoptResult struct {
	Root                 string            `json:"root" yaml:"root"`
	Source               string            `json:"source" yaml:"source"`
	AgentName            string            `json:"agentName" yaml:"agentName"`
	Protocol             string            `json:"protocol" yaml:"protocol"`
	Runtime              string            `json:"runtime" yaml:"runtime"`
	EntryPoint           string            `json:"entryPoint" yaml:"entryPoint"`
	DependencyResolution string            `json:"dependencyResolution" yaml:"dependencyResolution"`
	GuardrailPolicyID    string            `json:"guardrailPolicyId,omitempty" yaml:"guardrailPolicyId,omitempty"`
	NoGuardrail          bool              `json:"noGuardrail,omitempty" yaml:"noGuardrail,omitempty"`
	DependencyFiles      []string          `json:"dependencyFiles" yaml:"dependencyFiles"`
	InPlace              bool              `json:"inPlace" yaml:"inPlace"`
	CopiedFiles          int               `json:"copiedFiles" yaml:"copiedFiles"`
	HostingDetected      bool              `json:"hostingDetected" yaml:"hostingDetected"`
	Files                []string          `json:"files" yaml:"files"`
	Metadata             map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

type adoptionSource struct {
	root            string
	info            fs.FileInfo
	files           []adoptionFile
	entryPoint      string
	dependencyFiles []string
	hostingDetected bool
}

type adoptionFile struct {
	relative string
	size     int64
}

type adoptionBackup struct {
	path   string
	data   []byte
	mode   fs.FileMode
	exists bool
}

type adoptionDestination struct {
	absolute       string
	cwdReal        string
	parentReal     string
	parentRelative string
	base           string
}

func AdoptPythonSource(options AdoptOptions) (AdoptResult, error) {
	if !agentNamePattern.MatchString(options.AgentName) {
		return AdoptResult{}, errs.Config(
			"--name %q is invalid; use a 1-63 character Hosted Agent name containing letters, digits, or internal hyphens",
			options.AgentName,
		)
	}
	switch options.Protocol {
	case "responses", "invocations":
	default:
		return AdoptResult{}, errs.Config("--protocol must be responses or invocations")
	}
	if options.NoGuardrail && strings.TrimSpace(options.GuardrailPolicyID) != "" {
		return AdoptResult{}, errs.Config(
			"--guardrail-policy-id and --no-guardrail cannot be used together",
		)
	}
	if strings.TrimSpace(options.GuardrailPolicyID) != "" {
		policy, err := foundryid.ParseRAIPolicyID(options.GuardrailPolicyID)
		if err != nil {
			return AdoptResult{}, errs.Config("--guardrail-policy-id is invalid: %v", err)
		}
		options.GuardrailPolicyID = policy.String()
	}
	switch options.Runtime {
	case "python_3_13", "python_3_14":
	default:
		return AdoptResult{}, errs.Config("--runtime must be python_3_13 or python_3_14")
	}
	if _, ok := supportedDependency[options.DependencyResolution]; !ok {
		return AdoptResult{}, errs.Config(
			"--dependency-resolution must be remote_build or bundled",
		)
	}
	if err := custommetadata.Validate(options.Metadata); err != nil {
		return AdoptResult{}, err
	}
	if options.InPlace && strings.TrimSpace(options.Destination) != "" {
		return AdoptResult{}, errs.Config("--destination cannot be used with --in-place")
	}

	source, err := inspectAdoptionSource(
		options.Source,
		options.EntryPoint,
		options.Protocol,
	)
	if err != nil {
		return AdoptResult{}, err
	}

	sourcePath := filepath.ToSlash(filepath.Join("src", options.AgentName))
	if options.InPlace {
		sourcePath = "."
	}
	azureYAML, err := renderHostedAzureYAML(hostedAzureYAMLOptions{
		AgentName:            options.AgentName,
		Source:               sourcePath,
		Protocol:             options.Protocol,
		Runtime:              options.Runtime,
		EntryPoint:           filepath.ToSlash(source.entryPoint),
		DependencyResolution: options.DependencyResolution,
		GuardrailPolicyID:    options.GuardrailPolicyID,
		NoGuardrail:          options.NoGuardrail,
		Metadata:             options.Metadata,
	})
	if err != nil {
		return AdoptResult{}, err
	}

	var root string
	var files []string
	copiedFiles := 0
	if options.InPlace {
		root, files, err = adoptInPlace(source, options.AgentName, azureYAML)
	} else {
		root, files, err = adoptByCopy(
			source,
			options.Destination,
			options.AgentName,
			azureYAML,
		)
		copiedFiles = len(source.files)
	}
	if err != nil {
		return AdoptResult{}, err
	}

	return AdoptResult{
		Root:                 root,
		Source:               source.root,
		AgentName:            options.AgentName,
		Protocol:             options.Protocol,
		Runtime:              options.Runtime,
		EntryPoint:           filepath.ToSlash(source.entryPoint),
		DependencyResolution: options.DependencyResolution,
		GuardrailPolicyID:    options.GuardrailPolicyID,
		NoGuardrail:          options.NoGuardrail,
		DependencyFiles:      append([]string(nil), source.dependencyFiles...),
		InPlace:              options.InPlace,
		CopiedFiles:          copiedFiles,
		HostingDetected:      source.hostingDetected,
		Files:                files,
		Metadata:             custommetadata.Clone(options.Metadata),
	}, nil
}

func inspectAdoptionSource(
	sourcePath,
	entryPoint,
	protocol string,
) (adoptionSource, error) {
	if strings.TrimSpace(sourcePath) == "" {
		return adoptionSource{}, errs.Config("--source is required")
	}
	absolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return adoptionSource{}, errs.Config("failed to resolve --source %q: %v", sourcePath, err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return adoptionSource{}, errs.Config("failed to inspect --source %q: %v", sourcePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return adoptionSource{}, errs.Security("--source must be a real directory, not a file or symbolic link")
	}
	root, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return adoptionSource{}, errs.Security("failed to resolve --source links: %v", err)
	}

	files, err := adoptionSourceFiles(root)
	if err != nil {
		return adoptionSource{}, err
	}
	entryPoint, err = resolveAdoptionEntryPoint(root, entryPoint, files)
	if err != nil {
		return adoptionSource{}, err
	}
	dependencyFiles, err := adoptionDependencyFiles(root)
	if err != nil {
		return adoptionSource{}, err
	}
	hostingDetected, err := adoptionHostingDetected(root, entryPoint, protocol)
	if err != nil {
		return adoptionSource{}, err
	}
	return adoptionSource{
		root:            filepath.Clean(root),
		info:            info,
		files:           files,
		entryPoint:      filepath.Clean(entryPoint),
		dependencyFiles: dependencyFiles,
		hostingDetected: hostingDetected,
	}, nil
}

func adoptionSourceFiles(root string) ([]adoptionFile, error) {
	files := make([]adoptionFile, 0)
	var totalBytes int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() {
			switch filepath.Base(relative) {
			case ".agentignore", ".env.example":
				return errs.Config("--source %s must be a regular file", filepath.Base(relative))
			}
		}
		normalized := filepath.ToSlash(relative)
		if adoptionPathExcluded(normalized, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errs.Security("Hosted adoption source contains unsupported symbolic link %q", relative)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return errs.Security("failed to resolve Hosted adoption source path %q: %v", relative, err)
		}
		if !pathWithin(root, resolved) {
			return errs.Security("Hosted adoption source path %q escapes --source", relative)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errs.Security("Hosted adoption source contains unsupported non-regular file %q", relative)
		}
		if info.Size() > maxAdoptionFileBytes {
			return errs.Config(
				"Hosted adoption source file %q exceeds the %d MiB per-file limit",
				relative,
				maxAdoptionFileBytes>>20,
			)
		}
		totalBytes += info.Size()
		if totalBytes > MaxCodeArchiveBytes {
			return errs.Config(
				"Hosted adoption source exceeds the %d MiB aggregate limit",
				MaxCodeArchiveBytes>>20,
			)
		}
		files = append(files, adoptionFile{
			relative: filepath.Clean(relative),
			size:     info.Size(),
		})
		if len(files) > maxSnapshotFiles {
			return errs.Config("Hosted adoption source exceeds the %d file safety limit", maxSnapshotFiles)
		}
		return nil
	})
	if err != nil {
		if errs.IsKind(err, "security") || errs.IsKind(err, "config") {
			return nil, err
		}
		return nil, errs.Config("failed to enumerate Hosted adoption source: %v", err)
	}
	sort.Slice(files, func(i, j int) bool {
		return filepath.ToSlash(files[i].relative) < filepath.ToSlash(files[j].relative)
	})
	return files, nil
}

func adoptionPathExcluded(relative string, directory bool) bool {
	segments := strings.Split(filepath.ToSlash(relative), "/")
	for _, segment := range segments {
		switch segment {
		case ".git", ".azure", ".foundry-agent-manager", ".foundry",
			".agent_configs", ".venv", "venv", "__pycache__",
			".pytest_cache", ".mypy_cache", ".ruff_cache":
			return true
		}
	}
	if directory {
		return false
	}
	base := filepath.Base(relative)
	if base == ".env" || strings.HasSuffix(strings.ToLower(base), ".pyc") {
		return true
	}
	if strings.HasPrefix(base, ".env.") &&
		base != ".env.example" &&
		base != ".env.sample" {
		return true
	}
	return false
}

func resolveAdoptionEntryPoint(
	root,
	configured string,
	files []adoptionFile,
) (string, error) {
	if strings.TrimSpace(configured) != "" {
		if err := validateAdoptionEntryPoint(configured); err != nil {
			return "", err
		}
		if !adoptionFileExists(files, configured) {
			return "", errs.Config("--entry-point %q was not found under --source", configured)
		}
		return filepath.Clean(filepath.FromSlash(configured)), nil
	}
	for _, candidate := range []string{"main.py", "app.py", "agent.py"} {
		if adoptionFileExists(files, candidate) {
			return candidate, nil
		}
	}
	var topLevel []string
	for _, file := range files {
		if filepath.Dir(file.relative) == "." &&
			strings.EqualFold(filepath.Ext(file.relative), ".py") {
			topLevel = append(topLevel, file.relative)
		}
	}
	if len(topLevel) == 1 {
		return topLevel[0], nil
	}
	if len(topLevel) == 0 {
		return "", errs.Config(
			"no Python entry point was detected under --source; add main.py or set --entry-point",
		)
	}
	return "", errs.Config(
		"multiple Python entry-point candidates were found (%s); set --entry-point",
		strings.Join(topLevel, ", "),
	)
}

func validateAdoptionEntryPoint(value string) error {
	if err := validateRelativePath(value, "--entry-point"); err != nil {
		return err
	}
	normalized := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if filepath.Ext(normalized) != ".py" {
		return errs.Config("--entry-point must reference a Python .py file")
	}
	if strings.ContainsAny(normalized, " \t\r\n:") {
		return errs.Config("--entry-point must not contain whitespace or ':'")
	}
	return nil
}

func adoptionFileExists(files []adoptionFile, expected string) bool {
	expected = filepath.ToSlash(filepath.Clean(filepath.FromSlash(expected)))
	for _, file := range files {
		if filepath.ToSlash(file.relative) == expected {
			return true
		}
	}
	return false
}

func adoptionDependencyFiles(root string) ([]string, error) {
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, errs.Security("failed to open --source safely: %v", err)
	}
	defer rootHandle.Close()
	var found []string
	for _, candidate := range []string{"requirements.txt", "pyproject.toml", "setup.py"} {
		info, statErr := rootHandle.Stat(candidate)
		switch {
		case statErr == nil && info.Mode().IsRegular():
			found = append(found, candidate)
		case statErr == nil:
			return nil, errs.Config("--source dependency path %q is not a regular file", candidate)
		case os.IsNotExist(statErr):
		default:
			return nil, errs.Config("failed to inspect --source dependency file %q: %v", candidate, statErr)
		}
	}
	if len(found) == 0 {
		return nil, errs.Config(
			"--source must contain requirements.txt, pyproject.toml, or setup.py for Python dependency installation",
		)
	}
	return found, nil
}

func adoptionHostingDetected(root, entryPoint, protocol string) (bool, error) {
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return false, errs.Security("failed to open --source safely: %v", err)
	}
	defer rootHandle.Close()
	file, err := rootHandle.Open(filepath.ToSlash(entryPoint))
	if err != nil {
		return false, errs.Config("failed to inspect --entry-point %q: %v", entryPoint, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return false, errs.Config("failed to read --entry-point %q: %v", entryPoint, err)
	}
	if len(data) > 1<<20 {
		return false, errs.Config("--entry-point %q exceeds the 1 MiB inspection limit", entryPoint)
	}
	marker := "ResponsesHostServer"
	if protocol == "invocations" {
		marker = "InvocationsHostServer"
	}
	return strings.Contains(string(data), marker), nil
}

func adoptByCopy(
	source adoptionSource,
	destination,
	agentName,
	azureYAML string,
) (string, []string, error) {
	resolved, err := resolveAdoptionDestination(destination)
	if err != nil {
		return "", nil, err
	}
	if pathWithin(source.root, resolved.parentReal) {
		return "", nil, errs.Security(
			"--destination must not be inside --source; run from the source parent, choose a sibling destination, or use --in-place",
		)
	}

	cwdRoot, err := os.OpenRoot(".")
	if err != nil {
		return "", nil, errs.Security("failed to open the current directory safely: %v", err)
	}
	defer cwdRoot.Close()
	parentRoot, err := cwdRoot.OpenRoot(filepath.ToSlash(resolved.parentRelative))
	if err != nil {
		return "", nil, errs.Security("failed to open the destination parent safely: %v", err)
	}
	defer parentRoot.Close()
	if err := ensureRootMatchesPath(cwdRoot, resolved.cwdReal, "current directory"); err != nil {
		return "", nil, err
	}
	if err := ensureRootMatchesPath(parentRoot, resolved.parentReal, "destination parent"); err != nil {
		return "", nil, err
	}
	if _, err := parentRoot.Lstat(resolved.base); err == nil {
		return "", nil, errs.Config("--destination %q already exists", destination)
	} else if !os.IsNotExist(err) {
		return "", nil, errs.Config("failed to inspect --destination %q: %v", destination, err)
	}

	tempName, tempRoot, err := createRootTempDirectory(parentRoot, ".foundry-agent-manager-adopt-")
	if err != nil {
		return "", nil, errs.Config("failed to create Hosted adoption staging directory: %v", err)
	}
	defer func() {
		_ = tempRoot.Close()
		_ = parentRoot.RemoveAll(tempName)
	}()
	tempPath := filepath.Join(resolved.parentReal, tempName)

	targetSource := filepath.ToSlash(filepath.Join("src", agentName))
	if err := tempRoot.MkdirAll(targetSource, 0o700); err != nil {
		return "", nil, errs.Config("failed to create adopted source directory: %v", err)
	}
	if err := copyAdoptionFiles(source, tempRoot, targetSource); err != nil {
		return "", nil, err
	}
	if err := prepareAdoptedSourceFiles(tempRoot, targetSource, false); err != nil {
		return "", nil, err
	}
	if err := writeRootFileExclusive(tempRoot, AzureYAMLFile, []byte(azureYAML), 0o600); err != nil {
		return "", nil, errs.Config("failed to write adopted azure.yaml: %v", err)
	}
	if err := ensureRootMatchesPath(parentRoot, resolved.parentReal, "destination parent"); err != nil {
		return "", nil, err
	}
	if err := ensureRootMatchesPath(tempRoot, tempPath, "adoption staging directory"); err != nil {
		return "", nil, err
	}
	if _, err := LoadWorkspace(tempPath, agentName); err != nil {
		return "", nil, errs.Config("adopted Hosted Agent workspace failed validation: %v", err)
	}
	if err := ensureRootMatchesPath(parentRoot, resolved.parentReal, "destination parent"); err != nil {
		return "", nil, err
	}
	if err := ensureRootMatchesPath(tempRoot, tempPath, "adoption staging directory"); err != nil {
		return "", nil, err
	}
	if err := parentRoot.Rename(tempName, resolved.base); err != nil {
		return "", nil, errs.Config("failed to finalize Hosted adoption workspace: %v", err)
	}
	if err := ensureRootEntryMatchesPath(parentRoot, resolved.base, resolved.absolute, "adoption destination"); err != nil {
		_ = parentRoot.RemoveAll(resolved.base)
		return "", nil, err
	}
	return resolved.absolute, []string{
		"azure.yaml",
		filepath.ToSlash(filepath.Join("src", agentName)),
	}, nil
}

func resolveAdoptionDestination(destination string) (adoptionDestination, error) {
	if strings.TrimSpace(destination) == "" {
		return adoptionDestination{}, errs.Config("--destination is required unless --in-place is used")
	}
	if err := netcheck.ValidateRelativeFileReference(destination, "--destination"); err != nil {
		return adoptionDestination{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return adoptionDestination{}, errs.Config("failed to resolve the current directory: %v", err)
	}
	cwdReal, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return adoptionDestination{}, errs.Security("failed to resolve the current directory safely: %v", err)
	}
	resolved, err := netcheck.RequireContainedFile(cwd, destination, "--destination")
	if err != nil {
		return adoptionDestination{}, err
	}
	if _, err := os.Lstat(resolved); err == nil {
		return adoptionDestination{}, errs.Config("--destination %q already exists", destination)
	} else if !os.IsNotExist(err) {
		return adoptionDestination{}, errs.Config("failed to inspect --destination %q: %v", destination, err)
	}
	parent := filepath.Dir(resolved)
	info, err := os.Lstat(parent)
	if err != nil {
		return adoptionDestination{}, errs.Config("the parent directory for --destination must already exist: %v", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return adoptionDestination{}, errs.Security("the parent directory for --destination must be a real directory")
	}
	parentReal, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return adoptionDestination{}, errs.Security("failed to resolve the destination parent safely: %v", err)
	}
	cwdReal = filepath.Clean(cwdReal)
	parentReal = filepath.Clean(parentReal)
	if !pathWithin(cwdReal, parentReal) {
		return adoptionDestination{}, errs.Security(
			"the real parent directory for --destination must remain inside the current directory",
		)
	}
	parentRelative, err := filepath.Rel(cwdReal, parentReal)
	if err != nil || filepath.IsAbs(parentRelative) {
		return adoptionDestination{}, errs.Security("failed to root the destination parent safely")
	}
	return adoptionDestination{
		absolute:       filepath.Join(parentReal, filepath.Base(resolved)),
		cwdReal:        cwdReal,
		parentReal:     parentReal,
		parentRelative: parentRelative,
		base:           filepath.Base(resolved),
	}, nil
}

func copyAdoptionFiles(source adoptionSource, destinationRoot *os.Root, destinationBase string) error {
	rootHandle, err := os.OpenRoot(source.root)
	if err != nil {
		return errs.Security("failed to open --source safely: %v", err)
	}
	defer rootHandle.Close()
	if err := ensureRootMatchesInfo(rootHandle, source.info, "--source"); err != nil {
		return err
	}
	for _, item := range source.files {
		relative := filepath.ToSlash(item.relative)
		from, err := rootHandle.Open(relative)
		if err != nil {
			return errs.Config("failed to open --source file %q: %v", relative, err)
		}
		target := filepath.ToSlash(filepath.Join(destinationBase, item.relative))
		if err := destinationRoot.MkdirAll(filepath.ToSlash(filepath.Dir(target)), 0o700); err != nil {
			from.Close()
			return errs.Config("failed to create adopted directory for %q: %v", relative, err)
		}
		to, err := destinationRoot.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			from.Close()
			return errs.Config("failed to create adopted file %q: %v", relative, err)
		}
		written, copyErr := io.CopyN(to, from, item.size)
		if copyErr == nil {
			var extra [1]byte
			count, readErr := from.Read(extra[:])
			if readErr != nil && readErr != io.EOF {
				copyErr = readErr
			} else if count != 0 {
				copyErr = fmt.Errorf("source file grew while it was copied")
			}
		}
		closeErr := to.Close()
		from.Close()
		if copyErr != nil || written != item.size {
			return errs.Security("--source file %q changed while it was copied; retry with stable source", relative)
		}
		if closeErr != nil {
			return errs.Config("failed to close adopted file %q: %v", relative, closeErr)
		}
	}
	if err := ensureRootMatchesPath(rootHandle, source.root, "--source"); err != nil {
		return err
	}
	return nil
}

func adoptInPlace(
	source adoptionSource,
	agentName,
	azureYAML string,
) (string, []string, error) {
	sourceRoot, err := os.OpenRoot(source.root)
	if err != nil {
		return "", nil, errs.Security("failed to open --source safely for in-place adoption: %v", err)
	}
	defer sourceRoot.Close()
	if err := ensureRootMatchesInfo(sourceRoot, source.info, "--source"); err != nil {
		return "", nil, err
	}
	if err := ensureRootMatchesPath(sourceRoot, source.root, "--source"); err != nil {
		return "", nil, err
	}
	if _, err := sourceRoot.Lstat(AzureYAMLFile); err == nil {
		return "", nil, errs.Config(
			"--source already contains azure.yaml; use the existing workspace or choose copy mode",
		)
	} else if !os.IsNotExist(err) {
		return "", nil, errs.Config("failed to inspect existing azure.yaml: %v", err)
	}

	backups, err := captureAdoptionBackups(sourceRoot, ".agentignore", ".env.example")
	if err != nil {
		return "", nil, err
	}
	rollback := func(operationErr error) error {
		if removeErr := sourceRoot.Remove(AzureYAMLFile); removeErr != nil && !os.IsNotExist(removeErr) {
			return errs.Security("%v; additionally failed to remove azure.yaml during rollback: %v", operationErr, removeErr)
		}
		if restoreErr := restoreAdoptionBackups(sourceRoot, backups); restoreErr != nil {
			return errs.Security("%v; additionally failed to restore source files during rollback: %v", operationErr, restoreErr)
		}
		return operationErr
	}

	if err := writeRootFileExclusive(sourceRoot, AzureYAMLFile, []byte(azureYAML), 0o600); err != nil {
		return "", nil, errs.Config("failed to write adopted azure.yaml: %v", err)
	}
	if err := prepareAdoptedSourceFiles(sourceRoot, ".", true); err != nil {
		return "", nil, rollback(err)
	}
	if err := ensureRootMatchesPath(sourceRoot, source.root, "--source"); err != nil {
		return "", nil, rollback(err)
	}
	if _, err := LoadWorkspace(source.root, agentName); err != nil {
		return "", nil, rollback(errs.Config("in-place Hosted adoption failed validation and was rolled back: %v", err))
	}
	if err := ensureRootMatchesPath(sourceRoot, source.root, "--source"); err != nil {
		return "", nil, rollback(err)
	}
	files := []string{"azure.yaml", ".agentignore"}
	if !backups[".env.example"].exists {
		files = append(files, ".env.example")
	}
	return source.root, files, nil
}

func captureAdoptionBackups(root *os.Root, paths ...string) (map[string]adoptionBackup, error) {
	result := make(map[string]adoptionBackup, len(paths))
	for _, path := range paths {
		info, err := root.Lstat(path)
		switch {
		case err == nil && !info.Mode().IsRegular():
			return nil, errs.Config("%s is not a regular file", path)
		case err == nil:
			data, readErr := root.ReadFile(path)
			if readErr != nil {
				return nil, errs.Config("failed to read %s before in-place adoption: %v", path, readErr)
			}
			result[path] = adoptionBackup{
				path:   path,
				data:   data,
				mode:   info.Mode().Perm(),
				exists: true,
			}
		case os.IsNotExist(err):
			result[path] = adoptionBackup{path: path}
		default:
			return nil, errs.Config("failed to inspect %s before in-place adoption: %v", path, err)
		}
	}
	return result, nil
}

func restoreAdoptionBackups(root *os.Root, backups map[string]adoptionBackup) error {
	for _, backup := range backups {
		if backup.exists {
			if err := replaceRootFile(root, backup.path, backup.data, backup.mode); err != nil {
				return err
			}
			continue
		}
		if err := root.Remove(backup.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func prepareAdoptedSourceFiles(root *os.Root, base string, inPlace bool) error {
	ignorePath := filepath.ToSlash(filepath.Join(base, ".agentignore"))
	existing, err := root.ReadFile(ignorePath)
	if err != nil && !os.IsNotExist(err) {
		return errs.Config("failed to read adopted .agentignore: %v", err)
	}
	if len(existing) > 1<<20 {
		return errs.Config("adopted .agentignore exceeds the 1 MiB safety limit")
	}
	lines := append([]string(nil), adoptionIgnoreDefaults...)
	if inPlace {
		lines = append(lines, "azure.yaml")
	}
	merged := mergeAgentIgnore(existing, lines)
	if err := replaceRootFile(root, ignorePath, merged, 0o600); err != nil {
		return errs.Config("failed to write adopted .agentignore: %v", err)
	}
	if _, err := parseAgentIgnore(merged); err != nil {
		return errs.Config("adopted .agentignore is incompatible with Hosted deployment: %v", err)
	}

	envExample := filepath.ToSlash(filepath.Join(base, ".env.example"))
	info, err := root.Lstat(envExample)
	if os.IsNotExist(err) {
		if err := writeRootFileExclusive(
			root,
			envExample,
			[]byte("AZURE_AI_MODEL_DEPLOYMENT_NAME=<model-deployment-name>\n"),
			0o600,
		); err != nil {
			return errs.Config("failed to write adopted .env.example: %v", err)
		}
	} else if err != nil {
		return errs.Config("failed to inspect adopted .env.example: %v", err)
	} else if !info.Mode().IsRegular() {
		return errs.Config("adopted .env.example must be a regular file")
	}
	return nil
}

func createRootTempDirectory(root *os.Root, prefix string) (string, *os.Root, error) {
	for range 100 {
		suffix := make([]byte, 12)
		if _, err := rand.Read(suffix); err != nil {
			return "", nil, err
		}
		name := prefix + hex.EncodeToString(suffix)
		if err := root.Mkdir(name, 0o700); err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", nil, err
		}
		tempRoot, err := root.OpenRoot(name)
		if err != nil {
			_ = root.Remove(name)
			return "", nil, err
		}
		return name, tempRoot, nil
	}
	return "", nil, fmt.Errorf("could not allocate a unique staging directory")
}

func writeRootFileExclusive(root *os.Root, name string, data []byte, mode fs.FileMode) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		_ = root.Remove(name)
		return err
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(name)
		return err
	}
	return nil
}

func replaceRootFile(root *os.Root, name string, data []byte, mode fs.FileMode) error {
	directory := filepath.ToSlash(filepath.Dir(name))
	if directory == "." {
		directory = ""
	}
	base := filepath.Base(name)
	tempPrefix := "." + base + ".fam-"
	var tempName string
	created := false
	for range 100 {
		suffix := make([]byte, 12)
		if _, err := rand.Read(suffix); err != nil {
			return err
		}
		tempName = tempPrefix + hex.EncodeToString(suffix) + ".tmp"
		if directory != "" {
			tempName = directory + "/" + tempName
		}
		if err := writeRootFileExclusive(root, tempName, data, mode); err != nil {
			if os.IsExist(err) {
				continue
			}
			return err
		}
		created = true
		break
	}
	if !created {
		return fmt.Errorf("could not allocate a unique temporary file for %s", name)
	}
	defer root.Remove(tempName)
	if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
		return err
	}
	return root.Rename(tempName, name)
}

func ensureRootMatchesPath(root *os.Root, path, label string) error {
	return ensureRootEntryMatchesPath(root, ".", path, label)
}

func ensureRootMatchesInfo(root *os.Root, expected fs.FileInfo, label string) error {
	actual, err := root.Stat(".")
	if err != nil {
		return errs.Security("failed to verify the rooted %s: %v", label, err)
	}
	if !os.SameFile(actual, expected) {
		return errs.Security("%s changed while adoption was running; no workspace was finalized", label)
	}
	return nil
}

func ensureRootEntryMatchesPath(root *os.Root, entry, path, label string) error {
	rootInfo, err := root.Stat(entry)
	if err != nil {
		return errs.Security("failed to verify the rooted %s: %v", label, err)
	}
	pathInfo, err := os.Stat(path)
	if err != nil {
		return errs.Security("failed to verify the path for %s: %v", label, err)
	}
	if !os.SameFile(rootInfo, pathInfo) {
		return errs.Security("%s changed while adoption was running; no workspace was finalized", label)
	}
	return nil
}

func mergeAgentIgnore(existing []byte, defaults []string) []byte {
	normalized := strings.ReplaceAll(string(existing), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	seen := make(map[string]bool)
	for _, line := range strings.Split(normalized, "\n") {
		seen[strings.TrimSpace(line)] = true
	}
	var builder strings.Builder
	if normalized != "" {
		builder.WriteString(normalized)
		builder.WriteString("\n")
	}
	for _, line := range defaults {
		if seen[line] {
			continue
		}
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	return []byte(builder.String())
}

func pathWithin(base, candidate string) bool {
	base = filepath.Clean(base)
	candidate = filepath.Clean(candidate)
	relative, err := filepath.Rel(base, candidate)
	if err != nil {
		return false
	}
	return relative == "." ||
		(relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
			!filepath.IsAbs(relative))
}
