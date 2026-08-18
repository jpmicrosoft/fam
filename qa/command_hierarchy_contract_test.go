package qa

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestUserDocumentationUsesCanonicalCommandPaths(t *testing.T) {
	source := repositoryFile(t, "cmd", "command_hierarchy.go")
	routePattern := regexp.MustCompile(`\{Legacy: "([^"]+)", Path: \[\]string\{`)
	matches := routePattern.FindAllStringSubmatch(source, -1)
	if len(matches) == 0 {
		t.Fatal("no command hierarchy routes were discovered")
	}

	var files []string
	files = append(files, filepath.Join("..", "README.md"))
	for _, directory := range []string{"docs", "examples"} {
		root := filepath.Join("..", directory)
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".md", ".yaml", ".yml":
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(data)
		for _, match := range matches {
			legacy := match[1]
			needle := "foundry-agent-manager " + legacy
			if !strings.Contains(content, needle) {
				continue
			}
			if legacy == "hosted-deploy" &&
				(path == filepath.Join("..", "README.md") ||
					path == filepath.Join("..", "docs", "command-reference.md")) {
				continue
			}
			t.Errorf("%s contains legacy invocation %q", path, needle)
		}
	}
}
