package hostedskills

import (
	"context"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/netcheck"
	"foundry-agent-manager/internal/skills"
)

// Client is deliberately read-only. The caller must construct it for the
// validated ProjectEndpoint using the existing Foundry authentication/preview
// policy. Nil is supported for local-only bundle synchronization and detachment.
type Client interface {
	GetSkillVersionContext(context.Context, string, string) (*foundry.SkillVersion, error)
	DownloadSkillContext(context.Context, string, string) ([]byte, error)
	GetToolboxVersionContext(context.Context, string, string) (*foundry.ToolboxVersion, error)
}

type SyncOptions struct {
	Root            string
	SourceDirectory string
	Service         string
	Config          *Config
	ProjectEndpoint string
	Image           string
	Client          Client
	// RuntimeFiles contains trusted adapter templates keyed by portable,
	// source-root-relative paths beneath fam_skills/. The prefix is stripped
	// for staging beside manifest.json. Nil preserves existing owned helpers;
	// a non-nil map is the complete desired helper set. Pass current adapters
	// even when detaching so obsolete owned helpers can be pruned.
	RuntimeFiles map[string]string
}

type ValidateOptions struct {
	Root            string
	SourceDirectory string
	Service         string
	Config          *Config
	ProjectEndpoint string
	Image           string
}

type Manifest struct {
	FormatVersion       int     `json:"formatVersion"`
	Mode                string  `json:"mode"`
	ProjectEndpoint     string  `json:"projectEndpoint"`
	ToolboxName         string  `json:"toolboxName,omitempty"`
	ToolboxVersion      string  `json:"toolboxVersion,omitempty"`
	Skills              []Entry `json:"skills"`
	Service             string  `json:"service"`
	Language            string  `json:"language"`
	DeclarationHash     string  `json:"declarationHash"`
	ImageReference      string  `json:"imageReference,omitempty"`
	ImageEvidenceSHA256 string  `json:"imageEvidenceSHA256,omitempty"`
	// RuntimeFiles hashes exact adapter bytes using paths relative to fam_skills.
	RuntimeFiles map[string]string `json:"runtimeFiles,omitempty"`
}

type Entry struct {
	Name          string `json:"name"`
	Version       string `json:"version,omitempty"`
	SHA256        string `json:"sha256"`
	Path          string `json:"path,omitempty"`
	SourcePath    string `json:"sourcePath,omitempty"`
	ArchiveSHA256 string `json:"archiveSHA256,omitempty"`
}

// Artifact reports only locally verified artifacts. RuntimeVerified is always
// false: runtime discovery/loading must be established by the application.
type Artifact struct {
	Directory       string            `json:"directory"`
	ManifestPath    string            `json:"manifestPath"`
	SHA256          string            `json:"sha256"`
	Manifest        Manifest          `json:"manifest"`
	RequiredFiles   []string          `json:"requiredFiles"`
	FileSHA256      map[string]string `json:"fileSha256"`
	State           string            `json:"state"`
	RuntimeVerified bool              `json:"runtimeVerified"`
}

// Sync validates every selected immutable input before replacing any output.
// Supplied adapters are managed inside fam_skills with the manifest and Skills;
// application code outside that directory is never modified.
func Sync(ctx context.Context, options SyncOptions) (*Artifact, error) {
	if options.Config == nil {
		return nil, errs.Config("Hosted Skill sync requires a declaration; use skills: [] to detach explicitly")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	validation := ValidateOptions{
		Root: options.Root, SourceDirectory: options.SourceDirectory,
		Service: options.Service, Config: options.Config,
		ProjectEndpoint: options.ProjectEndpoint, Image: options.Image,
	}
	manifest, err := expectedManifest(validation)
	if err != nil {
		return nil, err
	}
	directories, err := openDirectories(validation)
	if err != nil {
		return nil, err
	}
	defer directories.Close()
	if err := rejectManagedInputs(directories.sourceRelative, *options.Config); err != nil {
		return nil, err
	}
	if err := checkTransactions(directories.source); err != nil {
		return nil, err
	}
	previous, err := readManaged(directories.source, DirectoryName, options.Service)
	if err != nil {
		return nil, err
	}
	if options.Config.Mode == ModeMCP && len(options.Config.Skills) != 0 {
		if err := validateToolbox(ctx, options); err != nil {
			return nil, err
		}
	}
	files := make(map[string][]byte)
	names := make(map[string]bool)
	for _, source := range options.Config.Skills {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pkg, archiveHash, err := loadSource(ctx, directories, options, source)
		if err != nil {
			return nil, err
		}
		if names[strings.ToLower(pkg.Name)] {
			return nil, errs.Config("duplicate Hosted Skill identity %q", pkg.Name)
		}
		names[strings.ToLower(pkg.Name)] = true
		entry := Entry{
			Name: pkg.Name, Version: source.Version, SHA256: digest(pkg.Content),
			SourcePath: source.Path, ArchiveSHA256: archiveHash,
		}
		if options.Config.Mode == ModeBundle {
			entry.Path = pkg.Name + "/SKILL.md"
			files[entry.Path] = pkg.Content
		}
		manifest.Skills = append(manifest.Skills, entry)
	}
	if err := checkLocalInputs(directories, *options.Config, manifest); err != nil {
		return nil, err
	}
	if err := addRuntimeFiles(files, options.RuntimeFiles, previous, &manifest); err != nil {
		return nil, err
	}
	if err := validateImageRefresh(previous, manifest); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, errs.Config("cannot encode Hosted Skill manifest: %v", err)
	}
	files[ManifestFileName] = append(encoded, '\n')
	if err := replaceManaged(ctx, directories.source, options.Service, previous, files); err != nil {
		return nil, err
	}
	return ValidateArtifact(validation)
}

func validateImageRefresh(previous *managedFiles, manifest Manifest) error {
	if previous == nil || manifest.ImageReference == "" {
		return nil
	}
	var old Manifest
	if err := decodeJSON(previous.data[ManifestFileName], &old); err != nil {
		return errs.Config("existing Hosted Skill image manifest is invalid: %v", err)
	}
	if old.ImageReference == manifest.ImageReference && !maps.Equal(old.RuntimeFiles, manifest.RuntimeFiles) {
		return errs.Config("runtime helpers changed for the same immutable image; rebuild the image with the new helpers and declare its new digest before syncing")
	}
	if old.ImageReference == manifest.ImageReference &&
		(old.Mode != manifest.Mode || old.Language != manifest.Language ||
			old.ProjectEndpoint != manifest.ProjectEndpoint ||
			old.ToolboxName != manifest.ToolboxName || old.ToolboxVersion != manifest.ToolboxVersion ||
			!slices.Equal(old.Skills, manifest.Skills)) {
		return errs.Config("Skill runtime configuration changed for the same immutable image; rebuild the image with the new configuration and declare its new digest before syncing")
	}
	return nil
}

func addRuntimeFiles(files map[string][]byte, supplied map[string]string, previous *managedFiles, manifest *Manifest) error {
	helpers := make(map[string][]byte, len(supplied))
	if supplied == nil && previous != nil {
		var old Manifest
		if err := decodeJSON(previous.data[ManifestFileName], &old); err != nil {
			return errs.Config("cannot preserve existing runtime helpers from an invalid manifest: %v", err)
		}
		if err := validateRuntimeFiles(old, previous); err != nil {
			return err
		}
		for name := range old.RuntimeFiles {
			helpers[name] = previous.data[name]
		}
	} else {
		for sourcePath, content := range supplied {
			name, contained := strings.CutPrefix(sourcePath, DirectoryName+"/")
			if !contained {
				return errs.Security("runtime helper %q must be source-root-relative beneath %s/", sourcePath, DirectoryName)
			}
			helpers[name] = []byte(content)
		}
	}
	if len(helpers) != 0 {
		manifest.RuntimeFiles = make(map[string]string, len(helpers))
	}
	for name, data := range helpers {
		if err := validateRuntimePath(name, manifest.Skills); err != nil {
			return err
		}
		if len(data) > maxArtifactFileBytes {
			return errs.Config("runtime helper %q exceeds the existing %d byte FAM filesystem/download safety bound (not an Azure Skill quota)", name, maxArtifactFileBytes)
		}
		if _, exists := files[name]; exists {
			return errs.Security("runtime helper %q collides with a generated Skill artifact", name)
		}
		files[name] = data
		manifest.RuntimeFiles[name] = digest(data)
	}
	return nil
}

func loadSource(ctx context.Context, directories *directories, options SyncOptions, source Source) (skills.Package, string, error) {
	if source.Path != "" {
		pkg, err := skills.LoadDirectory(directories.workspace.Name(), source.Path)
		return pkg, "", err
	}
	if options.Client == nil {
		return skills.Package{}, "", errs.Config("Skill %q version %s requires an authenticated Foundry read client", source.Name, source.Version)
	}
	version, err := options.Client.GetSkillVersionContext(ctx, source.Name, source.Version)
	if err != nil {
		return skills.Package{}, "", err
	}
	if version == nil {
		return skills.Package{}, "", errs.Config("Skill %q version %s does not exist", source.Name, source.Version)
	}
	if version.Version != source.Version || (version.Name != "" && version.Name != source.Name) {
		return skills.Package{}, "", errs.Config("Foundry returned a different immutable Skill identity for %q version %s", source.Name, source.Version)
	}
	data, err := options.Client.DownloadSkillContext(ctx, source.Name, source.Version)
	if err != nil {
		return skills.Package{}, "", err
	}
	pkg, err := skills.ReadArchive(data)
	if err != nil {
		return skills.Package{}, "", errs.Config("Skill %q version %s is not instructions-only: %v", source.Name, source.Version, err)
	}
	if pkg.Name != source.Name {
		return skills.Package{}, "", errs.Config("Skill %q version %s has a different package name %q", source.Name, source.Version, pkg.Name)
	}
	return pkg, digest(data), nil
}

func validateToolbox(ctx context.Context, options SyncOptions) error {
	if options.Client == nil {
		return errs.Config("mcp Skill synchronization requires an authenticated Foundry read client")
	}
	pin := options.Config.Toolbox
	toolbox, err := options.Client.GetToolboxVersionContext(ctx, pin.Name, pin.Version)
	if err != nil {
		return err
	}
	if toolbox == nil {
		return errs.Config("Toolbox %q version %s does not exist", pin.Name, pin.Version)
	}
	if toolbox.Version != pin.Version || (toolbox.Name != "" && toolbox.Name != pin.Name) {
		return errs.Config("Foundry returned a different immutable Toolbox identity for %q version %s", pin.Name, pin.Version)
	}
	references := make(map[string]string)
	for _, raw := range toolbox.Skills {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return errs.Config("Toolbox %q contains an invalid Skill reference: %v", pin.Name, err)
		}
		var reference struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if err := decodeJSON(encoded, &reference); err != nil {
			return errs.Config("Toolbox %q contains an unsupported Skill reference: %v", pin.Name, err)
		}
		if reference.Type != "skill_reference" {
			return errs.Config("Toolbox %q contains an unsupported Skill reference type", pin.Name)
		}
		if err := skills.ValidateReference(skills.Reference{Name: reference.Name, Version: reference.Version}); err != nil {
			return errs.Config("Toolbox %q has an unpinned or invalid Skill reference: %v", pin.Name, err)
		}
		if !pinnedVersion(reference.Version) || references[reference.Name] != "" {
			return errs.Config("Toolbox %q has duplicate or unpinned Skill %q", pin.Name, reference.Name)
		}
		references[reference.Name] = reference.Version
	}
	for _, source := range options.Config.Skills {
		if references[source.Name] != source.Version {
			return errs.Config("Toolbox %q version %s does not contain selected Skill %q version %s", pin.Name, pin.Version, source.Name, source.Version)
		}
	}
	return nil
}

func expectedManifest(options ValidateOptions) (Manifest, error) {
	if options.Config == nil {
		return Manifest{}, errs.Config("Hosted Skill declaration is required")
	}
	hash, err := DeclarationHash(options.Service, *options.Config)
	if err != nil {
		return Manifest{}, err
	}
	endpoint, err := validateEndpoint(options.ProjectEndpoint)
	if err != nil {
		return Manifest{}, err
	}
	config := options.Config
	if config.RequiresRemote() && endpoint == "" {
		return Manifest{}, errs.Config("remote Hosted Skills require an explicit Foundry project endpoint")
	}
	manifest := Manifest{
		FormatVersion: 1, Mode: config.Mode, ProjectEndpoint: endpoint,
		Service: options.Service, Language: config.Language, DeclarationHash: hash,
		Skills: make([]Entry, 0, len(config.Skills)),
	}
	if len(config.Skills) != 0 && config.Toolbox != nil {
		manifest.ToolboxName = config.Toolbox.Name
		manifest.ToolboxVersion = config.Toolbox.Version
	}
	if options.Image != "" || config.Image != nil {
		if config.Image == nil || options.Image != config.Image.Reference {
			return Manifest{}, errs.Config("prebuilt-image Skills require matching immutable image.reference and operator image.integrationEvidence; local files are not injected into images")
		}
		data, err := netcheck.ReadContainedFile(options.Root, config.Image.IntegrationEvidence, "Hosted Skill image integration evidence")
		if err != nil {
			return Manifest{}, err
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			return Manifest{}, errs.Config("Hosted Skill image integration evidence must not be empty")
		}
		manifest.ImageReference = config.Image.Reference
		manifest.ImageEvidenceSHA256 = digest(data)
	}
	return manifest, nil
}

func validateEndpoint(endpoint string) (string, error) {
	if endpoint == "" {
		return "", nil
	}
	if _, err := netcheck.ValidateFoundryEndpoint(endpoint, "Hosted Skill project endpoint"); err != nil {
		return "", err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", errs.Config("invalid Hosted Skill project endpoint: %v", err)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" ||
		parsed.Port() != "" || len(parts) != 3 || parts[0] != "api" || parts[1] != "projects" ||
		parts[2] == "" || parts[2] == "." || parts[2] == ".." {
		return "", errs.Security("Hosted Skill project endpoint must identify exactly one Foundry /api/projects/{project}, without query, fragment, or port")
	}
	return strings.TrimRight(endpoint, "/"), nil
}
