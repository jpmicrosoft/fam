package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"foundry-agent-manager/internal/hosted"
)

type hostedProgressDisplay int

const (
	hostedProgressOff hostedProgressDisplay = iota
	hostedProgressPlain
	hostedProgressSpinner
)

type hostedPhaseText struct {
	running   string
	completed string
}

type hostedProgressRunner struct {
	delegate        hosted.Runner
	writer          io.Writer
	display         hostedProgressDisplay
	startDelay      time.Duration
	updateInterval  time.Duration
	spinnerInterval time.Duration
}

func validateProgressSetting(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto", "plain", "off":
		return nil
	default:
		return fmt.Errorf("--progress must be auto, plain, or off")
	}
}

func resolveHostedProgressDisplay(cmdWriter io.Writer, output, progress string, quiet, verbose bool) hostedProgressDisplay {
	if quiet {
		return hostedProgressOff
	}
	switch strings.ToLower(strings.TrimSpace(progress)) {
	case "off":
		return hostedProgressOff
	case "plain":
		return hostedProgressPlain
	}
	if !strings.EqualFold(strings.TrimSpace(output), "text") {
		if verbose {
			return hostedProgressPlain
		}
		return hostedProgressOff
	}
	if isTerminalWriter(cmdWriter) {
		return hostedProgressSpinner
	}
	return hostedProgressPlain
}

func isTerminalWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (r hostedProgressRunner) Run(ctx context.Context, command hosted.Command) (hosted.Execution, error) {
	if r.delegate == nil {
		return hosted.Execution{}, fmt.Errorf("Hosted command runner is required")
	}
	if r.writer == nil || r.display == hostedProgressOff {
		return r.delegate.Run(ctx, command)
	}
	started := time.Now()
	stop := make(chan struct{})
	rendered := make(chan bool, 1)
	go r.render(command.Phase, started, stop, rendered)

	execution, err := r.delegate.Run(ctx, command)
	close(stop)
	if <-rendered {
		r.finish(command.Phase, time.Since(started), err)
	}
	return execution, err
}

func (r hostedProgressRunner) render(phase string, started time.Time, stop <-chan struct{}, rendered chan<- bool) {
	delay := r.startDelay
	if delay <= 0 {
		delay = time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-stop:
		rendered <- false
		return
	case <-timer.C:
	}

	text := hostedProgressText(phase)
	switch r.display {
	case hostedProgressSpinner:
		r.renderSpinner(text.running, started, stop)
	case hostedProgressPlain:
		r.renderPlain(text.running, started, stop)
	}
	rendered <- true
}

func (r hostedProgressRunner) renderSpinner(label string, started time.Time, stop <-chan struct{}) {
	frames := []byte{'|', '/', '-', '\\'}
	interval := r.spinnerInterval
	if interval <= 0 {
		interval = 120 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	index := 0
	write := func() {
		_, _ = fmt.Fprintf(
			r.writer,
			"\r%c %s [%s elapsed]",
			frames[index%len(frames)],
			label,
			formatProgressElapsed(time.Since(started)),
		)
		index++
	}
	write()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			write()
		}
	}
}

func (r hostedProgressRunner) renderPlain(label string, started time.Time, stop <-chan struct{}) {
	_, _ = fmt.Fprintf(r.writer, "%s... [%s elapsed]\n", label, formatProgressElapsed(time.Since(started)))
	interval := r.updateInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			_, _ = fmt.Fprintf(
				r.writer,
				"Still running: %s [%s elapsed]\n",
				label,
				formatProgressElapsed(time.Since(started)),
			)
		}
	}
}

func (r hostedProgressRunner) finish(phase string, elapsed time.Duration, runErr error) {
	text := hostedProgressText(phase)
	status := "Finished"
	if runErr != nil {
		status = "Failed"
	}
	prefix := ""
	if r.display == hostedProgressSpinner {
		prefix = "\r"
	}
	_, _ = fmt.Fprintf(
		r.writer,
		"%s%s: %s [%s elapsed]\n",
		prefix,
		status,
		text.completed,
		formatProgressElapsed(elapsed),
	)
}

func hostedProgressText(phase string) hostedPhaseText {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "deploy":
		return hostedPhaseText{"Deploying Hosted Agent", "Hosted Agent deployment"}
	case "provision":
		return hostedPhaseText{"Provisioning Azure resources", "Azure resource provisioning"}
	case "provision-preview":
		return hostedPhaseText{"Previewing Azure resource provisioning", "Azure resource provisioning preview"}
	case "doctor":
		return hostedPhaseText{"Checking Hosted deployment prerequisites", "Hosted deployment prerequisite check"}
	case "status":
		return hostedPhaseText{"Checking Hosted Agent status", "Hosted Agent status check"}
	case "environment-create":
		return hostedPhaseText{"Creating the workspace azd environment", "workspace azd environment creation"}
	case "environment-configure":
		return hostedPhaseText{"Configuring the workspace azd environment", "workspace azd environment configuration"}
	default:
		name := strings.ReplaceAll(strings.TrimSpace(phase), "-", " ")
		if name == "" {
			name = "command"
		}
		return hostedPhaseText{
			running:   "Running Hosted phase " + name,
			completed: "Hosted phase " + name,
		}
	}
}

func formatProgressElapsed(duration time.Duration) string {
	seconds := int64(duration.Round(time.Second) / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	remaining := seconds % 60
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, remaining)
	}
	return fmt.Sprintf("%02d:%02d", minutes, remaining)
}
