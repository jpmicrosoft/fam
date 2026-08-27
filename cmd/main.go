// fam: deploy and manage Microsoft Foundry prompt agents from standalone manifests.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	errs "foundry-agent-manager/internal/errors"

	"github.com/spf13/pflag"
)

func main() {
	os.Exit(execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func execute(args []string, in io.Reader, out, errOut io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	args = normalizeRootArgs(args)
	root := rootCmd()
	primeOutputFlag(root, args)
	root.SetArgs(args)
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetContext(ctx)
	if err := root.Execute(); err != nil {
		if code, reported := errs.ReportedExitCode(err); reported {
			return code
		}
		if errs.KindOf(err) == "internal" && strings.HasPrefix(err.Error(), "unknown command ") {
			err = errs.Config("%v", err)
		}
		code := errs.ExitCode(err)
		debugf(root, "failure kind=%s exit=%d", errs.KindOf(err), code)
		printer, printerErr := printerFor(root, true)
		if printerErr == nil {
			_ = printer.PrintError(errs.KindOf(err), err.Error(), code, errs.Remediation(err)...)
		} else {
			_, _ = io.WriteString(errOut, "error: "+err.Error()+"\n")
		}
		return code
	}
	return 0
}

func normalizeRootArgs(args []string) []string {
	if len(args) == 0 || args[0] != "-version" {
		return args
	}
	normalized := append([]string(nil), args...)
	normalized[0] = "--version"
	return normalized
}

func primeOutputFlag(root interface{ PersistentFlags() *pflag.FlagSet }, args []string) {
	for index := 0; index < len(args); index++ {
		if args[index] == "--" {
			break
		}
		var value string
		switch {
		case args[index] == "--output" || args[index] == "-o":
			if index+1 >= len(args) {
				continue
			}
			index++
			value = args[index]
		case strings.HasPrefix(args[index], "--output="):
			value = strings.TrimPrefix(args[index], "--output=")
		case strings.HasPrefix(args[index], "-o="):
			value = strings.TrimPrefix(args[index], "-o=")
		case strings.HasPrefix(args[index], "-o") && len(args[index]) > 2:
			value = strings.TrimPrefix(args[index], "-o")
		}
		if value != "" {
			_ = root.PersistentFlags().Set("output", value)
		}
	}
}
