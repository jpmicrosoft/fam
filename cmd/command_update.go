package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/update"

	"github.com/spf13/cobra"
)

type selfUpdater interface {
	Check(context.Context) (*update.Plan, error)
	Apply(context.Context, *update.Plan) (update.Result, error)
}

var newSelfUpdaterFn = func(options update.Options) (selfUpdater, error) {
	return update.New(options)
}

func cmdUpdate(cmd *cobra.Command, _ []string) error {
	version := getFlag(cmd, "version")
	if cmd.Flags().Changed("version") && strings.TrimSpace(version) == "" {
		return errs.Config("--version must specify a stable release version")
	}
	updater, err := newSelfUpdaterFn(update.Options{
		CurrentVersion: config.Version,
		Version:        version,
		Token:          selfUpdateToken(),
		Timeout:        getDurationFlag(cmd, "request-timeout"),
		Retries:        getIntFlag(cmd, "retry-count"),
		RetryDelay:     getDurationFlag(cmd, "retry-delay"),
	})
	if err != nil {
		return err
	}
	plan, err := updater.Check(commandContext(cmd))
	if err != nil {
		return err
	}
	result := plan.Result
	if result.UpdateAvailable && !getBoolFlag(cmd, "check") {
		if err := confirmDestructive(cmd, fmt.Sprintf(
			"Update FAM from %s to %s?\nExecutable: %s",
			result.CurrentVersion, result.TargetVersion, result.Executable,
		)); err != nil {
			return err
		}
		result, err = updater.Apply(commandContext(cmd), plan)
		if err != nil {
			if result.Changed || result.BackupPath != "" {
				steps := errs.Remediation(err)
				if result.Changed {
					steps = append(steps, fmt.Sprintf(
						"FAM %s was installed at %s, but finalization failed; inspect the installed executable before retrying.",
						result.TargetVersion, result.Executable,
					))
				}
				if result.BackupPath != "" {
					steps = append(steps, "Preserve the previous executable at "+result.BackupPath+" until recovery is complete.")
				}
				err = errs.WithNextSteps(err, steps...)
			}
			return err
		}
	}
	return printUpdateResult(cmd, result)
}

func selfUpdateToken() string {
	for _, name := range []string{"FAM_INSTALL_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token
		}
	}
	return ""
}

func printUpdateResult(cmd *cobra.Command, result update.Result) error {
	var text string
	switch result.Status {
	case "available":
		text = fmt.Sprintf("FAM update available: %s -> %s", result.CurrentVersion, result.TargetVersion)
	case "up-to-date":
		text = fmt.Sprintf("FAM is already up to date (%s).", result.CurrentVersion)
	case "newer-installed":
		text = fmt.Sprintf("Installed FAM %s is newer than release %s; leaving it unchanged.", result.CurrentVersion, result.TargetVersion)
	case "updated":
		text = fmt.Sprintf("Updated FAM from %s to %s.", result.CurrentVersion, result.TargetVersion)
	default:
		return fmt.Errorf("updater returned an unknown status %q", result.Status)
	}
	text += fmt.Sprintf("\nExecutable: %s", result.Executable)
	if result.BackupPath != "" {
		text += fmt.Sprintf("\nPrevious executable retained at: %s\nRemove that backup only after this process exits and the new FAM runs successfully.", result.BackupPath)
	}
	return printResult(cmd, result, text)
}
