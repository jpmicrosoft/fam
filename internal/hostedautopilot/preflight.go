package hostedautopilot

import (
	"errors"
	"fmt"
	"os/exec"
)

const (
	OfficialRepositoryURL = "https://github.com/microsoft-foundry/foundry-samples"
	SamplePath            = "samples/csharp/foundry-autopilot-agent"
	ReviewedSampleCommit  = "a2de504ff6b69149bd40d89edd1c86dc11c6af57"

	AzureCloud Cloud = "AzureCloud"
)

var (
	ErrPreviewNotAccepted  = errors.New("hosted autopilot preview was not explicitly accepted")
	ErrCommitNotApproved   = errors.New("sample commit was not explicitly approved")
	ErrUnsupportedCloud    = errors.New("unsupported Azure cloud")
	ErrRegionNotAllowed    = errors.New("region is not in the caller-supplied allowed list")
	ErrAllowedRegionsEmpty = errors.New("caller-supplied allowed region list is empty")
	ErrMissingExecutable   = errors.New("required executable was not found")
	ErrLookPathUnavailable = errors.New("LookPath function is required")
)

var requiredExecutables = [...]string{"git", "az", "azd", "pwsh", "docker", "dotnet"}

type Cloud string

type LookPathFunc func(file string) (string, error)

type PreflightOptions struct {
	Cloud               Cloud
	AcceptPreview       bool
	ApproveSampleCommit string
	Region              string
	AllowedRegions      []string
	LookPath            LookPathFunc
}

type PreflightResult struct {
	Cloud               Cloud
	Region              string
	ApprovedCommit      string
	ResolvedExecutables map[string]string
}

func RequiredExecutables() []string {
	executables := make([]string, len(requiredExecutables))
	copy(executables, requiredExecutables[:])
	return executables
}

func Preflight(options PreflightOptions) (PreflightResult, error) {
	if options.Cloud != AzureCloud {
		return PreflightResult{}, fmt.Errorf("%w: %q", ErrUnsupportedCloud, options.Cloud)
	}
	if !options.AcceptPreview {
		return PreflightResult{}, ErrPreviewNotAccepted
	}
	if options.ApproveSampleCommit != ReviewedSampleCommit {
		return PreflightResult{}, ErrCommitNotApproved
	}
	if len(options.AllowedRegions) == 0 {
		return PreflightResult{}, ErrAllowedRegionsEmpty
	}

	regionAllowed := false
	for _, allowedRegion := range options.AllowedRegions {
		if options.Region == allowedRegion {
			regionAllowed = true
			break
		}
	}
	if !regionAllowed {
		return PreflightResult{}, fmt.Errorf("%w: %q", ErrRegionNotAllowed, options.Region)
	}

	lookPath := options.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if lookPath == nil {
		return PreflightResult{}, ErrLookPathUnavailable
	}

	resolved := make(map[string]string, len(requiredExecutables))
	for _, executable := range requiredExecutables {
		path, err := lookPath(executable)
		if err != nil || path == "" {
			return PreflightResult{}, fmt.Errorf("%w: %s", ErrMissingExecutable, executable)
		}
		resolved[executable] = path
	}

	return PreflightResult{
		Cloud:               options.Cloud,
		Region:              options.Region,
		ApprovedCommit:      options.ApproveSampleCommit,
		ResolvedExecutables: resolved,
	}, nil
}
