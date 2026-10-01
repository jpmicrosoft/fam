package config

import (
	"reflect"
	"testing"

	"foundry-agent-manager/internal/skills"
)

func TestPromptSkillsPresence(t *testing.T) {
	for _, test := range []struct {
		name       string
		configured bool
		value      interface{}
		want       []skills.Reference
	}{
		{name: "omitted"},
		{name: "clear", configured: true, value: []interface{}{}, want: []skills.Reference{}},
		{
			name: "pinned", configured: true,
			value: []interface{}{map[string]interface{}{"name": "greeting", "version": "1"}},
			want:  []skills.Reference{{Name: "greeting", Version: "1"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc := validDoc()
			if test.configured {
				doc["agent"].(map[string]interface{})["skills"] = test.value
			}
			if err := ValidateManifest(doc); err != nil {
				t.Fatal(err)
			}
			cfg, err := ResolveConfig(doc)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Agent.SkillsConfigured != test.configured || !reflect.DeepEqual(cfg.Agent.Skills, test.want) {
				t.Fatalf("skills = %#v, configured = %t", cfg.Agent.Skills, cfg.Agent.SkillsConfigured)
			}
		})
	}
}

func TestPromptSkillsRejectInvalidReferences(t *testing.T) {
	for _, test := range []struct {
		name  string
		value interface{}
	}{
		{"null", nil},
		{"object", map[string]interface{}{"name": "greeting", "version": "1"}},
		{"missing-version", []interface{}{map[string]interface{}{"name": "greeting"}}},
		{"empty-version", []interface{}{map[string]interface{}{"name": "greeting", "version": ""}}},
		{"latest", []interface{}{map[string]interface{}{"name": "greeting", "version": "latest"}}},
		{"numeric-version", []interface{}{map[string]interface{}{"name": "greeting", "version": 1}}},
		{"local-path", []interface{}{map[string]interface{}{"path": "greeting"}}},
		{"toolbox-discriminator", []interface{}{map[string]interface{}{"name": "greeting", "version": "1", "type": "skill_reference"}}},
		{"duplicate-name", []interface{}{
			map[string]interface{}{"name": "greeting", "version": "1"},
			map[string]interface{}{"name": "greeting", "version": "2"},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc := validDoc()
			doc["agent"].(map[string]interface{})["skills"] = test.value
			if err := ValidateManifest(doc); err == nil {
				t.Fatal("invalid native skill declaration passed schema/semantic validation")
			}
			if _, err := ResolveConfig(doc); err == nil {
				t.Fatal("invalid native skill declaration passed resolution")
			}
		})
	}
}

func TestAgentCardSkillsAreNotNativeSkills(t *testing.T) {
	doc := validDoc()
	doc["endpoint"] = map[string]interface{}{
		"agent_card": map[string]interface{}{
			"skills": []interface{}{map[string]interface{}{"id": "capability", "name": "Advertised capability"}},
		},
	}
	cfg, err := ResolveConfig(doc)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.SkillsConfigured || len(cfg.Agent.Skills) != 0 || len(cfg.Endpoint.AgentCard.Skills) != 1 {
		t.Fatalf("A2A and native skills must remain independent: %#v", cfg)
	}
}
