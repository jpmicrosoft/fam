package main

import (
	"strings"
	"testing"
)

func TestEveryApplicationCommandHasParsableExamples(t *testing.T) {
	for _, command := range qualifiedCommands(rootCmd()) {
		if !command.Runnable() {
			continue
		}
		command := command
		t.Run(command.CommandPath(), func(t *testing.T) {
			if strings.TrimSpace(command.Example) == "" {
				t.Fatal("command help has no examples")
			}

			commandLines := 0
			for _, rawLine := range strings.Split(command.Example, "\n") {
				line := strings.TrimSpace(rawLine)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				commandLines++

				fields := strings.Fields(line)
				path := strings.Fields(command.CommandPath())
				if len(fields) < len(path) ||
					!equalStringSlices(fields[:len(path)], path) {
					t.Fatalf("example is not a direct %s invocation: %q", command.CommandPath(), line)
				}

				fresh, remaining, err := rootCmd().Find(path[1:])
				if err != nil {
					t.Fatal(err)
				}
				if len(remaining) != 0 {
					t.Fatalf("example command path was not resolved: %q", line)
				}
				if err := fresh.ParseFlags(fields[len(path):]); err != nil {
					t.Fatalf("example contains invalid flags: %q: %v", line, err)
				}
				args := fresh.Flags().Args()
				if len(args) != 0 {
					t.Fatalf("example contains unexpected positional arguments: %q: %#v", line, args)
				}
				if fresh.PreRunE != nil {
					if err := fresh.PreRunE(fresh, args); err != nil {
						t.Fatalf("example omits required flags: %q: %v", line, err)
					}
				}
			}
			if commandLines == 0 {
				t.Fatal("command help has comments but no runnable example")
			}
		})
	}
}

func TestDestructiveExamplesShowPreviewAndConfirmation(t *testing.T) {
	for _, command := range qualifiedCommands(rootCmd()) {
		if command.Flags().Lookup("dry-run") == nil || command.Flags().Lookup("yes") == nil {
			continue
		}
		t.Run(command.Name(), func(t *testing.T) {
			if !strings.Contains(command.Example, "--dry-run") {
				t.Errorf("destructive example has no preview:\n%s", command.Example)
			}
			if !strings.Contains(command.Example, "--yes") {
				t.Errorf("destructive example has no confirmed execution:\n%s", command.Example)
			}
		})
	}
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
