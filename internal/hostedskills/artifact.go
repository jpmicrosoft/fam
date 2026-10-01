package hostedskills

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/netcheck"
	"foundry-agent-manager/internal/skills"
)

const (
	ownershipFile = ".ownership.json"
	ownerName     = "foundry-agent-manager/hostedskills"
	// This is the existing Foundry download safety bound, not an Azure Skill quota.
	maxArtifactFileBytes = 256 << 20
)

type ownership struct {
	FormatVersion int               `json:"formatVersion"`
	Owner         string            `json:"owner"`
	Service       string            `json:"service"`
	Files         map[string]string `json:"files"`
}

type managedFiles struct {
	ownership ownership
	data      map[string][]byte
	identity  string
}

type directories struct {
	workspace      *os.Root
	source         *os.Root
	sourceRelative string
}

func (d *directories) Close() {
	d.source.Close()
	d.workspace.Close()
}

func openDirectories(options ValidateOptions) (*directories, error) {
	root, err := filepath.Abs(options.Root)
	if err != nil || options.Root == "" || options.SourceDirectory == "" {
		return nil, errs.Config("Hosted Skill workspace and source directories are required")
	}
	source, err := filepath.Abs(options.SourceDirectory)
	if err != nil {
		return nil, errs.Config("cannot resolve Hosted Skill source directory: %v", err)
	}
	relative, err := filepath.Rel(root, source)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, errs.Security("Hosted Skill artifacts must remain inside the workspace source directory")
	}
	relative = filepath.ToSlash(relative)
	for _, part := range strings.Split(relative, "/") {
		switch strings.ToLower(part) {
		case ".git", ".foundry-agent-manager", ".azure", ".venv", "__pycache__":
			return nil, errs.Security("Hosted Skill artifacts cannot be placed in an excluded source directory")
		}
	}
	workspace, err := os.OpenRoot(root)
	if err != nil {
		return nil, errs.Security("cannot safely open Hosted Skill workspace: %v", err)
	}
	sourceRoot, err := openDirectory(workspace, relative)
	if err != nil {
		workspace.Close()
		return nil, err
	}
	return &directories{workspace: workspace, source: sourceRoot, sourceRelative: relative}, nil
}

func openDirectory(root *os.Root, relative string) (*os.Root, error) {
	if relative != "." {
		if err := safeRelative(relative); err != nil {
			return nil, err
		}
		current := ""
		for _, part := range strings.Split(relative, "/") {
			current = filepath.Join(current, part)
			info, err := root.Lstat(current)
			if err != nil {
				return nil, errs.Config("cannot inspect Hosted Skill directory %q: %v", relative, err)
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, errs.Security("Hosted Skill directory %q must not contain links or non-directories", relative)
			}
		}
	}
	opened, err := root.OpenRoot(filepath.FromSlash(relative))
	if err != nil {
		return nil, errs.Security("cannot safely open Hosted Skill directory %q: %v", relative, err)
	}
	return opened, nil
}

func readRegular(root *os.Root, relative string, limit int64) ([]byte, error) {
	before, err := root.Lstat(filepath.FromSlash(relative))
	if err != nil {
		return nil, errs.Config("cannot inspect Hosted Skill file %q: %v", relative, err)
	}
	if !before.Mode().IsRegular() {
		return nil, errs.Security("Hosted Skill file %q must be regular, not a link", relative)
	}
	file, err := root.Open(filepath.FromSlash(relative))
	if err != nil {
		return nil, errs.Security("cannot safely open Hosted Skill file %q: %v", relative, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !os.SameFile(before, info) || !info.Mode().IsRegular() {
		return nil, errs.Security("Hosted Skill file %q changed while opening", relative)
	}
	if info.Size() > limit {
		return nil, errs.Config("Hosted Skill file %q exceeds the existing %d byte FAM filesystem/download safety bound (not an Azure Skill quota)", relative, limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, errs.Config("cannot read Hosted Skill file %q: %v", relative, err)
	}
	if int64(len(data)) > limit || int64(len(data)) != info.Size() {
		return nil, errs.Config("Hosted Skill file %q exceeded its safety bound or changed while reading", relative)
	}
	return data, nil
}

func rejectManagedInputs(sourceRelative string, config Config) error {
	target := strings.ToLower(filepath.ToSlash(filepath.Join(sourceRelative, DirectoryName)))
	for _, source := range config.Skills {
		if source.Path == "" {
			continue
		}
		local := strings.ToLower(filepath.ToSlash(filepath.Clean(source.Path)))
		if local == target || strings.HasPrefix(local, target+"/") || strings.HasPrefix(target, local+"/") {
			return errs.Security("local Skill source %q overlaps managed fam_skills output; keep original sources separate", source.Path)
		}
	}
	if config.Image != nil {
		evidence := strings.ToLower(config.Image.IntegrationEvidence)
		if evidence == target || strings.HasPrefix(evidence, target+"/") {
			return errs.Security("image integration evidence must not be stored inside managed fam_skills output")
		}
	}
	return nil
}

func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errs.Config("expected exactly one JSON value")
	}
	return nil
}

func readManaged(source *os.Root, directoryName, service string) (*managedFiles, error) {
	info, err := source.Lstat(directoryName)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errs.Config("cannot inspect managed Hosted Skill output: %v", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errs.Security("managed Hosted Skill output must be a directory, not a link")
	}
	directory, err := openDirectory(source, directoryName)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	encoded, err := readRegular(directory, ownershipFile, netcheck.MaxContainedFileBytes)
	if err != nil {
		return nil, errs.Config("fam_skills is unowned or damaged; resolve it manually without deleting original sources: %v", err)
	}
	var inventory ownership
	if err := decodeJSON(encoded, &inventory); err != nil {
		return nil, errs.Config("invalid Hosted Skill ownership inventory: %v", err)
	}
	if inventory.FormatVersion != 1 || inventory.Owner != ownerName || inventory.Service != service ||
		inventory.Files[ManifestFileName] == "" {
		return nil, errs.Config("fam_skills is not owned by this FAM service; refusing to replace it")
	}
	canonical, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil || !bytes.Equal(encoded, append(canonical, '\n')) {
		return nil, errs.Config("Hosted Skill ownership inventory was modified; restore it or move the output aside before explicit sync")
	}
	if err := validateManagedPaths(inventory.Files); err != nil {
		return nil, err
	}
	expected := map[string]bool{ownershipFile: true}
	dirs := map[string]bool{".": true}
	for name, hash := range inventory.Files {
		if !validManagedPath(name) || !validDigest(hash) {
			return nil, errs.Security("Hosted Skill ownership inventory contains an unsafe path or digest")
		}
		expected[name] = true
		addParentDirectories(dirs, name)
	}
	result := &managedFiles{ownership: inventory, data: map[string][]byte{ownershipFile: encoded}, identity: digest(encoded)}
	err = fs.WalkDir(directory.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errs.Security("managed Hosted Skill output contains a link %q", name)
		}
		if entry.IsDir() {
			if !dirs[name] {
				return errs.Config("fam_skills contains unowned directory %q; resolve it before synchronization", name)
			}
			return nil
		}
		if !expected[name] {
			return errs.Config("fam_skills contains unowned file %q; resolve it before synchronization", name)
		}
		if name == ownershipFile {
			return nil
		}
		data, err := readRegular(directory, name, maxArtifactFileBytes)
		if err != nil {
			return err
		}
		if digest(data) != inventory.Files[name] {
			return errs.Config("managed Hosted Skill file %q was modified; restore it or move the output aside before explicit sync", name)
		}
		result.data[name] = data
		return nil
	})
	if err != nil {
		return nil, errs.Config("cannot verify managed Hosted Skill ownership: %v", err)
	}
	if len(result.data) != len(expected) {
		return nil, errs.Config("managed Hosted Skill files are missing; restore them or move the output aside before explicit sync")
	}
	return result, nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func validManagedPath(name string) bool {
	if err := safeRelative(name); err != nil {
		return false
	}
	top := strings.SplitN(name, "/", 2)[0]
	if strings.EqualFold(top, ownershipFile) {
		return false
	}
	return !strings.EqualFold(top, ManifestFileName) || name == ManifestFileName
}

func validateManagedPaths(files map[string]string) error {
	type node struct {
		name      string
		directory bool
	}
	nodes := make(map[string]node)
	for name := range files {
		if !validManagedPath(name) {
			return errs.Security("Hosted Skill ownership inventory contains unsafe path %q", name)
		}
		for current := name; current != "."; current = path.Dir(current) {
			key := strings.ToLower(current)
			directory := current != name
			if previous, found := nodes[key]; found && (previous.name != current || previous.directory != directory) {
				return errs.Security("Hosted Skill artifact paths contain a case or file/directory collision at %q", current)
			}
			nodes[key] = node{name: current, directory: directory}
		}
	}
	return nil
}

func addParentDirectories(directories map[string]bool, name string) {
	for directory := path.Dir(name); directory != "."; directory = path.Dir(directory) {
		directories[directory] = true
	}
}

func validateRuntimePath(name string, entries []Entry) error {
	if !validManagedPath(name) || name == ManifestFileName || strings.EqualFold(path.Base(name), "SKILL.md") {
		return errs.Security("runtime helper %q must be a contained path distinct from manifest, ownership, and Skill files", name)
	}
	top := strings.SplitN(name, "/", 2)[0]
	for _, entry := range entries {
		if strings.EqualFold(top, entry.Name) {
			return errs.Security("runtime helper %q must not add supporting files to Skill %q", name, entry.Name)
		}
	}
	return nil
}

func validateRuntimeFiles(manifest Manifest, managed *managedFiles) error {
	for name, hash := range manifest.RuntimeFiles {
		if err := validateRuntimePath(name, manifest.Skills); err != nil {
			return err
		}
		data, found := managed.data[name]
		if !found || !validDigest(hash) || managed.ownership.Files[name] != hash || digest(data) != hash {
			return errs.Config("Hosted Skill runtime helper %q is missing or modified; resolve managed output before explicit sync", name)
		}
	}
	return nil
}

// ValidateArtifact is offline. ProjectEndpoint optionally enforces an exact
// project binding. It detects stale declarations, local input changes, missing
// output, edits to managed Skills or runtime helpers, and changed image evidence.
func ValidateArtifact(options ValidateOptions) (*Artifact, error) {
	if options.Config == nil {
		return nil, nil
	}
	if err := Validate(*options.Config); err != nil {
		return nil, err
	}
	directories, err := openDirectories(options)
	if err != nil {
		return nil, err
	}
	defer directories.Close()
	if err := checkTransactions(directories.source); err != nil {
		return nil, err
	}
	if err := rejectManagedInputs(directories.sourceRelative, *options.Config); err != nil {
		return nil, err
	}
	managed, err := readManaged(directories.source, DirectoryName, options.Service)
	if err != nil {
		return nil, err
	}
	if managed == nil {
		return nil, errs.Config("Hosted Skill artifacts are missing; run explicit Hosted Skill sync")
	}
	var manifest Manifest
	if err := decodeJSON(managed.data[ManifestFileName], &manifest); err != nil {
		return nil, errs.Config("Hosted Skill manifest is invalid: %v", err)
	}
	effective := options
	if effective.ProjectEndpoint == "" {
		effective.ProjectEndpoint = manifest.ProjectEndpoint
	}
	expected, err := expectedManifest(effective)
	if err != nil {
		return nil, err
	}
	if manifest.FormatVersion != expected.FormatVersion || manifest.Mode != expected.Mode ||
		manifest.Service != expected.Service || manifest.Language != expected.Language ||
		manifest.DeclarationHash != expected.DeclarationHash || manifest.ProjectEndpoint != expected.ProjectEndpoint ||
		manifest.ToolboxName != expected.ToolboxName || manifest.ToolboxVersion != expected.ToolboxVersion ||
		manifest.ImageReference != expected.ImageReference || manifest.ImageEvidenceSHA256 != expected.ImageEvidenceSHA256 {
		return nil, errs.Config("Hosted Skill artifacts are stale or bound to another project/image; run explicit sync and rebuild images when needed")
	}
	if err := validateEntries(*options.Config, manifest, managed); err != nil {
		return nil, err
	}
	if err := checkLocalInputs(directories, *options.Config, manifest); err != nil {
		return nil, err
	}
	required := make([]string, 0, len(managed.data))
	hashes := make(map[string]string, len(managed.data))
	for name, data := range managed.data {
		relative := filepath.ToSlash(filepath.Join(DirectoryName, name))
		required = append(required, relative)
		hashes[relative] = digest(data)
	}
	sort.Strings(required)
	state := "artifacts-verified"
	if manifest.ImageReference != "" {
		state = "operator-integration-evidence"
	} else if len(manifest.Skills) == 0 {
		state = "detached"
	}
	return &Artifact{
		Directory:    filepath.Join(options.SourceDirectory, DirectoryName),
		ManifestPath: filepath.Join(options.SourceDirectory, DirectoryName, ManifestFileName),
		SHA256:       managed.identity, Manifest: manifest, RequiredFiles: required, FileSHA256: hashes,
		State: state, RuntimeVerified: false,
	}, nil
}

func validateEntries(config Config, manifest Manifest, managed *managedFiles) error {
	if manifest.Skills == nil || len(manifest.Skills) != len(config.Skills) {
		return errs.Config("Hosted Skill inventory is stale; run explicit sync")
	}
	if err := validateRuntimeFiles(manifest, managed); err != nil {
		return err
	}
	expectedFiles := 1 + len(manifest.RuntimeFiles)
	seen := make(map[string]bool)
	for index, source := range config.Skills {
		entry := manifest.Skills[index]
		if skills.ValidateReference(skills.Reference{Name: entry.Name, Version: "1"}) != nil ||
			seen[entry.Name] || !validDigest(entry.SHA256) || entry.Version != source.Version ||
			entry.SourcePath != source.Path || (source.Name != "" && entry.Name != source.Name) {
			return errs.Config("Hosted Skill inventory identity does not match its declaration; run explicit sync")
		}
		seen[entry.Name] = true
		if source.Path == "" && !validDigest(entry.ArchiveSHA256) || source.Path != "" && entry.ArchiveSHA256 != "" {
			return errs.Config("Hosted Skill %q source provenance is invalid", entry.Name)
		}
		if config.Mode == ModeBundle {
			if entry.Path != entry.Name+"/SKILL.md" || digest(managed.data[entry.Path]) != entry.SHA256 {
				return errs.Config("Hosted Skill %q bundled instructions are missing or modified", entry.Name)
			}
			pkg, err := skills.Parse(managed.data[entry.Path])
			if err != nil || pkg.Name != entry.Name {
				return errs.Config("Hosted Skill %q bundled instructions are invalid", entry.Name)
			}
			expectedFiles++
		} else if entry.Path != "" {
			return errs.Config("MCP Skill inventory must not declare bundled paths")
		}
	}
	if len(managed.ownership.Files) != expectedFiles {
		return errs.Config("Hosted Skill output contains files not declared in its runtime inventory")
	}
	return nil
}

func checkLocalInputs(directories *directories, config Config, manifest Manifest) error {
	for index, source := range config.Skills {
		if source.Path == "" {
			continue
		}
		pkg, err := skills.LoadDirectory(directories.workspace.Name(), source.Path)
		if err != nil {
			return err
		}
		entry := manifest.Skills[index]
		if pkg.Name != entry.Name || digest(pkg.Content) != entry.SHA256 {
			return errs.Config("local Skill %q changed since synchronization; run explicit sync", source.Path)
		}
	}
	return nil
}
