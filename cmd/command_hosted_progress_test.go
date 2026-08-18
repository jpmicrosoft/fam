package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"foundry-agent-manager/internal/hosted"
)

type timedHostedRunner struct {
	delay time.Duration
	err   error
}

func (r timedHostedRunner) Run(ctx context.Context, _ hosted.Command) (hosted.Execution, error) {
	timer := time.NewTimer(r.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return hosted.Execution{}, ctx.Err()
	case <-timer.C:
		return hosted.Execution{Duration: r.delay}, r.err
	}
}

func TestHostedProgressPlainReportsLongPhaseAndHeartbeat(t *testing.T) {
	var output bytes.Buffer
	runner := hostedProgressRunner{
		delegate:       timedHostedRunner{delay: 35 * time.Millisecond},
		writer:         &output,
		display:        hostedProgressPlain,
		startDelay:     time.Millisecond,
		updateInterval: 5 * time.Millisecond,
	}
	_, err := runner.Run(context.Background(), hosted.Command{Phase: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, expected := range []string{
		"Deploying Hosted Agent...",
		"Still running: Deploying Hosted Agent",
		"Finished: Hosted Agent deployment",
		"elapsed",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("plain progress omitted %q:\n%s", expected, text)
		}
	}
}

func TestHostedProgressReportsFailure(t *testing.T) {
	var output bytes.Buffer
	failure := errors.New("deployment failed")
	runner := hostedProgressRunner{
		delegate:       timedHostedRunner{delay: 15 * time.Millisecond, err: failure},
		writer:         &output,
		display:        hostedProgressPlain,
		startDelay:     time.Millisecond,
		updateInterval: time.Hour,
	}
	_, err := runner.Run(context.Background(), hosted.Command{Phase: "deploy"})
	if !errors.Is(err, failure) {
		t.Fatalf("runner error = %v, want %v", err, failure)
	}
	if !strings.Contains(output.String(), "Failed: Hosted Agent deployment") {
		t.Fatalf("failed progress was not reported:\n%s", output.String())
	}
}

func TestHostedProgressKeepsFastPhasesSilent(t *testing.T) {
	var output bytes.Buffer
	runner := hostedProgressRunner{
		delegate:   timedHostedRunner{},
		writer:     &output,
		display:    hostedProgressPlain,
		startDelay: 50 * time.Millisecond,
	}
	if _, err := runner.Run(context.Background(), hosted.Command{Phase: "azd-version"}); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("fast phase emitted progress: %q", output.String())
	}
}

func TestHostedProgressDisplayPolicy(t *testing.T) {
	var output bytes.Buffer
	tests := []struct {
		name     string
		format   string
		progress string
		quiet    bool
		verbose  bool
		want     hostedProgressDisplay
	}{
		{name: "redirected text", format: "text", progress: "auto", want: hostedProgressPlain},
		{name: "structured", format: "json", progress: "auto", want: hostedProgressOff},
		{name: "structured verbose", format: "yaml", progress: "auto", verbose: true, want: hostedProgressPlain},
		{name: "explicit plain", format: "json", progress: "plain", want: hostedProgressPlain},
		{name: "explicit off", format: "text", progress: "off", want: hostedProgressOff},
		{name: "quiet", format: "text", progress: "plain", quiet: true, want: hostedProgressOff},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resolveHostedProgressDisplay(
				&output,
				test.format,
				test.progress,
				test.quiet,
				test.verbose,
			)
			if got != test.want {
				t.Fatalf("display = %v, want %v", got, test.want)
			}
		})
	}
}

func TestProgressFlagRejectsUnknownMode(t *testing.T) {
	run := runCLI(t, "", "version", "--progress", "spinner")
	if run.code != 3 || !strings.Contains(run.stderr, "--progress must be auto, plain, or off") {
		t.Fatalf("invalid progress mode was not rejected: %#v", run)
	}
}

func TestFormatProgressElapsed(t *testing.T) {
	if got := formatProgressElapsed(83 * time.Second); got != "01:23" {
		t.Fatalf("short elapsed = %q", got)
	}
	if got := formatProgressElapsed(3661 * time.Second); got != "01:01:01" {
		t.Fatalf("long elapsed = %q", got)
	}
}
