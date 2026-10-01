package hosted

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/netcheck"
)

func entryPointArgs(runtime string, value any) ([]string, error) {
	var args []string
	switch value := value.(type) {
	case string:
		args = []string{value}
	case []string:
		args = append([]string(nil), value...)
	case []any:
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, errs.Manifest("must contain only string arguments")
			}
			args = append(args, text)
		}
	default:
		return nil, errs.Manifest("must be a filename or a non-empty command array")
	}
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return nil, errs.Manifest("must be a filename or a non-empty command array")
	}
	for _, arg := range args {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return nil, errs.Manifest("arguments must not contain NUL bytes or line breaks")
		}
	}
	// Older FAM workspaces stored only the file, not the runtime executable.
	if len(args) == 1 {
		if strings.HasPrefix(runtime, "python_") && path.Ext(args[0]) == ".py" {
			return []string{"python", args[0]}, nil
		}
		if runtime == "dotnet_10" && path.Ext(args[0]) == ".dll" {
			return []string{"dotnet", args[0]}, nil
		}
	}
	return args, nil
}

func azdEntryPointFile(code CodeConfiguration) (string, error) {
	file, err := codeEntryPointFile(code)
	if err != nil {
		return "", err
	}
	executable := "dotnet"
	if strings.HasPrefix(code.Runtime, "python_") {
		executable = "python"
	}
	if len(code.EntryPoint) != 2 || code.EntryPoint[0] != executable {
		return "", errs.Config("azure.ai.agents %s accepts only a code entry-point filename and supplies the runtime executable; use a contained launcher file or a container to preserve explicit command arguments", RequiredExtensionVer)
	}
	return file, nil
}

func codeEntryPointFile(code CodeConfiguration) (string, error) {
	args := code.EntryPoint
	if len(args) < 2 {
		return "", errs.Manifest("codeConfiguration.entryPoint requires a runtime executable and a contained script or assembly")
	}
	index, extension := 1, ".dll"
	if strings.HasPrefix(code.Runtime, "python_") {
		versionedPython := "python" + strings.ReplaceAll(strings.TrimPrefix(code.Runtime, "python_"), "_", ".")
		if args[0] != "python" && args[0] != "python3" && args[0] != versionedPython {
			return "", errs.Manifest("codeConfiguration.entryPoint must launch python, python3, or %s with a contained .py file", versionedPython)
		}
		var err error
		index, err = pythonScriptIndex(args)
		if err != nil {
			return "", err
		}
		extension = ".py"
	} else {
		if args[0] != "dotnet" {
			return "", errs.Manifest("codeConfiguration.entryPoint must launch dotnet with a contained .dll assembly")
		}
		if args[index] == "exec" {
			index++
		}
	}
	if index >= len(args) || path.Ext(args[index]) != extension {
		return "", errs.Manifest("codeConfiguration.entryPoint must identify a %s file before application arguments", extension)
	}
	file := args[index]
	if strings.ContainsAny(file, "\\:\x00\r\n") {
		return "", errs.Security("codeConfiguration.entryPoint file must use a portable source-relative path")
	}
	if err := netcheck.ValidateRelativeFileReference(file, "codeConfiguration.entryPoint"); err != nil {
		return "", err
	}
	return path.Clean(file), nil
}

func pythonScriptIndex(args []string) (int, error) {
	index := 1
	for index < len(args) && strings.HasPrefix(args[index], "-") {
		arg := args[index]
		index++
		switch arg {
		case "--":
			return index, nil
		case "-u", "-B", "-E", "-I", "-O", "-OO", "-s", "-S", "-v", "-b", "-bb", "-q", "-d", "-P", "-x":
		case "-W", "-X":
			if index >= len(args) || args[index] == "" {
				return 0, errs.Manifest("codeConfiguration.entryPoint Python option %s requires a value", arg)
			}
			index++
		default:
			if !strings.HasPrefix(arg, "-W") && !strings.HasPrefix(arg, "-X") {
				return 0, errs.Manifest("codeConfiguration.entryPoint Python option %q is unsupported; use a contained .py launcher rather than inline code, modules, or shell commands", arg)
			}
		}
	}
	return index, nil
}

func codeSourceFiles(root *os.Root, code *CodeConfiguration) ([]string, error) {
	if code == nil {
		return nil, nil
	}
	entryPoint, err := codeEntryPointFile(*code)
	if err != nil {
		return nil, err
	}
	if code.Runtime != "dotnet_10" || code.DependencyResolution != "remote_build" {
		if err := requireCodeFile(root, entryPoint); err != nil {
			return nil, err
		}
		return []string{entryPoint}, nil
	}
	// Remote .NET builds produce the DLL server-side; validate source projects,
	// not the existence of an assembly that should not yet be in the upload.
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, errs.Security("cannot inspect the contained .NET remote-build source: %v", err)
	}
	var projects []string
	for _, entry := range entries {
		if path.Ext(entry.Name()) == ".csproj" {
			if err := requireCodeFile(root, entry.Name()); err != nil {
				return nil, err
			}
			projects = append(projects, entry.Name())
		}
	}
	if len(projects) == 0 {
		return nil, errs.Manifest("dotnet_10 remote_build requires a contained .csproj at the service source root; entryPoint names its server-published .dll")
	}
	return projects, nil
}

func requireCodeFile(root *os.Root, relative string) error {
	current := ""
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		current = path.Join(current, part)
		info, err := root.Lstat(filepath.FromSlash(current))
		if err != nil {
			return errs.Manifest("codeConfiguration requires contained source file %q: %v", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 ||
			(index < len(parts)-1 && !info.IsDir()) ||
			(index == len(parts)-1 && !info.Mode().IsRegular()) {
			return errs.Security("codeConfiguration source %q must be a regular file without symbolic links", relative)
		}
	}
	return nil
}

func requireCodeFiles(service Service, files []hostedSourceFile) error {
	if service.Mode != DeploymentModeCode || service.Code == nil {
		return nil
	}
	root, err := os.OpenRoot(service.SourceDirectory)
	if err != nil {
		return errs.Security("cannot open the contained codeConfiguration source: %v", err)
	}
	defer root.Close()
	required, err := codeSourceFiles(root, service.Code)
	if err != nil {
		return err
	}
	included := make(map[string]bool, len(files))
	for _, file := range files {
		included[filepath.ToSlash(file.relative)] = true
	}
	for _, file := range required {
		if !included[file] {
			return errs.Config("required codeConfiguration source %q is excluded from deployment by .agentignore or a default exclusion", file)
		}
	}
	return nil
}
