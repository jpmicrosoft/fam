package publication

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "publish.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validConfig() string {
	return `apiVersion: foundry-agent-manager/publication/v1
microsoft365:
  bot_service:
    name: support-bot
    tenant_id: 99999999-8888-7777-6666-555555555555
  publish_scope: Shared
  app_version: 1.0.0
  agent_display_name: Support Agent
  short_description: Support agent
  full_description: Handles support requests.
  developer_name: Contoso
  developer_website_url: https://developer.example.com
  privacy_url: https://privacy.example.com
  terms_of_use_url: https://terms.example.com
`
}

func TestLoadPublicationConfig(t *testing.T) {
	config, err := Load(writeConfig(t, validConfig()))
	if err != nil {
		t.Fatal(err)
	}
	if config.Microsoft365.BotService.SKU != "F0" ||
		config.Microsoft365.BotService.DisplayName != "Support Agent" ||
		config.Microsoft365.PublishScope != "Shared" {
		t.Fatalf("unexpected publication config: %#v", config)
	}
	if config.Microsoft365.BotService.TenantID != "99999999-8888-7777-6666-555555555555" {
		t.Fatalf("tenant ID was not loaded: %#v", config.Microsoft365.BotService)
	}
	wantHosts := []string{"developer.example.com", "privacy.example.com", "terms.example.com"}
	if hosts := config.MetadataHosts(); !reflect.DeepEqual(hosts, wantHosts) {
		t.Fatalf("unexpected metadata hosts: %#v", hosts)
	}
}

func TestLoadShippedPublicationExample(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "publication.example.yaml")
	config, err := Load(path)
	if err != nil {
		t.Fatalf("load shipped publication example: %v", err)
	}
	if config.Microsoft365.BotService.Name != "sample-agent-bot" ||
		config.Microsoft365.PublishScope != "Tenant" ||
		config.Microsoft365.AppVersion != "1.0.0" {
		t.Fatalf("unexpected shipped publication example: %#v", config.Microsoft365)
	}
}

func TestLoadRejectsUnknownAndMalformedFields(t *testing.T) {
	tests := []string{
		strings.Replace(validConfig(), "  publish_scope: Shared\n", "  publish_scope: Everyone\n", 1),
		strings.Replace(validConfig(), "  app_version: 1.0.0\n", "  app_version: 0.1.0\n", 1),
		strings.Replace(validConfig(), "  agent_display_name: Support Agent\n", "", 1),
		strings.Replace(validConfig(), "  developer_name: Contoso\n", "  developer_name: Contoso\n  unknown: true\n", 1),
	}
	for _, contents := range tests {
		if _, err := Load(writeConfig(t, contents)); err == nil {
			t.Fatalf("expected invalid publication configuration to fail:\n%s", contents)
		}
	}
}

func TestLoadRejectsCredentialURL(t *testing.T) {
	contents := strings.Replace(
		validConfig(),
		"https://developer.example.com",
		"https://user:password@developer.example.com",
		1,
	)
	if _, err := Load(writeConfig(t, contents)); err == nil {
		t.Fatal("expected metadata URL credentials to be rejected")
	}
}

func TestLoadIconsValidatesContainmentFormatAndDimensions(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "publish.yaml")
	contents := validConfig() + "  color_icon_file: color.png\n  outline_icon_file: outline.png\n"
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(root, "color.png"), 192, 192)
	writePNG(t, filepath.Join(root, "outline.png"), 32, 32)
	config, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	icons, err := config.LoadIcons()
	if err != nil {
		t.Fatal(err)
	}
	if icons.ColorBase64 == "" || icons.OutlineBase64 == "" {
		t.Fatal("expected both icons to be encoded")
	}

	writePNG(t, filepath.Join(root, "outline.png"), 33, 32)
	if _, err := config.LoadIcons(); err == nil {
		t.Fatal("expected wrong icon dimensions to fail")
	}
}

func writePNG(t *testing.T, path string, width, height int) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	image := image.NewRGBA(image.Rect(0, 0, width, height))
	image.Set(0, 0, color.Black)
	if err := png.Encode(file, image); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
