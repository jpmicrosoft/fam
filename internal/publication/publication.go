// Package publication loads and validates environment-specific Microsoft 365
// publication metadata.
package publication

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/netcheck"
	manifestschema "foundry-agent-manager/schema"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const APIVersion = "foundry-agent-manager/publication/v1"

type BotService struct {
	Name           string
	SubscriptionID string
	ResourceGroup  string
	TenantID       string
	DisplayName    string
	SKU            string
	AllowUpdate    bool
}

type Microsoft365 struct {
	BotService               BotService
	PublishScope             string
	AppVersion               string
	AgentDisplayName         string
	ShortDescription         string
	FullDescription          string
	DeveloperName            string
	DeveloperWebsiteURL      string
	PrivacyURL               string
	TermsOfUseURL            string
	ColorIconFile            string
	OutlineIconFile          string
	CanRespondWithoutMention bool
}

type Config struct {
	Path         string
	Microsoft365 Microsoft365
}

type Icons struct {
	ColorBase64   string
	OutlineBase64 string
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, errs.Manifest("failed to read publication configuration %s: %v", path, err)
	}
	var document map[string]interface{}
	if err := json.Unmarshal(data, &document); err != nil {
		if yamlErr := yaml.Unmarshal(data, &document); yamlErr != nil {
			return Config{}, errs.Manifest(
				"publication configuration %s is not valid JSON or YAML: %v",
				path,
				yamlErr,
			)
		}
	}
	if document == nil {
		return Config{}, errs.Manifest("publication configuration %s must be a mapping at the top level", path)
	}
	if err := validate(document); err != nil {
		return Config{}, err
	}
	m365 := object(document, "microsoft365")
	bot := object(m365, "bot_service")
	result := Config{
		Path: path,
		Microsoft365: Microsoft365{
			BotService: BotService{
				Name:           stringValue(bot, "name"),
				SubscriptionID: stringValue(bot, "subscription_id"),
				ResourceGroup:  stringValue(bot, "resource_group"),
				TenantID:       stringValue(bot, "tenant_id"),
				DisplayName:    stringValue(bot, "display_name"),
				SKU:            stringValue(bot, "sku"),
				AllowUpdate:    boolValue(bot, "allow_update"),
			},
			PublishScope:             stringValue(m365, "publish_scope"),
			AppVersion:               stringValue(m365, "app_version"),
			AgentDisplayName:         stringValue(m365, "agent_display_name"),
			ShortDescription:         stringValue(m365, "short_description"),
			FullDescription:          stringValue(m365, "full_description"),
			DeveloperName:            stringValue(m365, "developer_name"),
			DeveloperWebsiteURL:      stringValue(m365, "developer_website_url"),
			PrivacyURL:               stringValue(m365, "privacy_url"),
			TermsOfUseURL:            stringValue(m365, "terms_of_use_url"),
			ColorIconFile:            stringValue(m365, "color_icon_file"),
			OutlineIconFile:          stringValue(m365, "outline_icon_file"),
			CanRespondWithoutMention: boolValue(m365, "can_respond_without_mention"),
		},
	}
	if result.Microsoft365.BotService.SKU == "" {
		result.Microsoft365.BotService.SKU = "F0"
	}
	if result.Microsoft365.BotService.DisplayName == "" {
		result.Microsoft365.BotService.DisplayName = result.Microsoft365.AgentDisplayName
	}
	if err := validateURLs(result.Microsoft365); err != nil {
		return Config{}, err
	}
	return result, nil
}

func (c Config) MetadataHosts() []string {
	var result []string
	seen := map[string]struct{}{}
	for _, raw := range []string{
		c.Microsoft365.DeveloperWebsiteURL,
		c.Microsoft365.PrivacyURL,
		c.Microsoft365.TermsOfUseURL,
	} {
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" {
			continue
		}
		host := strings.ToLower(strings.TrimRight(parsed.Hostname(), "."))
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		result = append(result, host)
	}
	return result
}

func (c Config) LoadIcons() (Icons, error) {
	base := filepath.Dir(c.Path)
	color, err := loadIcon(base, c.Microsoft365.ColorIconFile, "microsoft365.color_icon_file", 192, 192)
	if err != nil {
		return Icons{}, err
	}
	outline, err := loadIcon(base, c.Microsoft365.OutlineIconFile, "microsoft365.outline_icon_file", 32, 32)
	if err != nil {
		return Icons{}, err
	}
	return Icons{ColorBase64: color, OutlineBase64: outline}, nil
}

func loadIcon(base, relative, field string, width, height int) (string, error) {
	if relative == "" {
		return "", nil
	}
	data, err := netcheck.ReadContainedFile(base, relative, field)
	if err != nil {
		return "", err
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", errs.Manifest("%s must reference a valid PNG image: %v", field, err)
	}
	if config.Width != width || config.Height != height {
		return "", errs.Manifest(
			"%s must be a %dx%d PNG image, got %dx%d",
			field,
			width,
			height,
			config.Width,
			config.Height,
		)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func validate(document map[string]interface{}) error {
	compiler := jsonschema.NewCompiler()
	decoded, err := jsonschema.UnmarshalJSON(strings.NewReader(string(manifestschema.PublicationBytes())))
	if err != nil {
		return fmt.Errorf("failed to unmarshal publication schema: %w", err)
	}
	if err := compiler.AddResource("publication.schema.json", decoded); err != nil {
		return fmt.Errorf("failed to add publication schema resource: %w", err)
	}
	compiled, err := compiler.Compile("publication.schema.json")
	if err != nil {
		return fmt.Errorf("failed to compile publication schema: %w", err)
	}
	data, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("failed to marshal publication configuration: %w", err)
	}
	var instance interface{}
	if err := json.Unmarshal(data, &instance); err != nil {
		return fmt.Errorf("failed to normalize publication configuration: %w", err)
	}
	if err := compiled.Validate(instance); err != nil {
		return errs.Manifest("publication configuration failed schema validation: %v", err)
	}
	return nil
}

func validateURLs(config Microsoft365) error {
	for field, raw := range map[string]string{
		"microsoft365.developer_website_url": config.DeveloperWebsiteURL,
		"microsoft365.privacy_url":           config.PrivacyURL,
		"microsoft365.terms_of_use_url":      config.TermsOfUseURL,
	} {
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return errs.Manifest("%s must be an absolute HTTPS URL", field)
		}
		if parsed.User != nil {
			return errs.Security("%s must not embed credentials", field)
		}
	}
	return nil
}

func object(document map[string]interface{}, key string) map[string]interface{} {
	value, _ := document[key].(map[string]interface{})
	return value
}

func stringValue(document map[string]interface{}, key string) string {
	value, _ := document[key].(string)
	return value
}

func boolValue(document map[string]interface{}, key string) bool {
	value, _ := document[key].(bool)
	return value
}
