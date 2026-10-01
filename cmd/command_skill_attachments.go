package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/netcheck"
	"foundry-agent-manager/internal/skills"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type skillAttachmentResult struct {
	File    string             `json:"file" yaml:"file"`
	Managed bool               `json:"managed" yaml:"managed"`
	Changed bool               `json:"changed" yaml:"changed"`
	Skills  []skills.Reference `json:"skills" yaml:"skills"`
}

func cmdSkillValidate(cmd *cobra.Command, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return errs.Config("failed to resolve the working directory: %v", err)
	}
	pkg, err := skills.LoadDirectory(cwd, getFlag(cmd, "path"))
	if err != nil {
		return err
	}
	return printResult(cmd, map[string]interface{}{
		"name": pkg.Name, "description": pkg.Description, "sha256": pkg.SHA256,
		"policy": "instructions-only", "valid": true,
	}, fmt.Sprintf("validated instructions-only Skill: name=%s sha256=%s", pkg.Name, pkg.SHA256))
}

func cmdPromptSkillList(cmd *cobra.Command, _ []string) error {
	if err := rejectSkillManifestOverrides(cmd); err != nil {
		return err
	}
	path, _, doc, err := readSkillConfiguration(getFlag(cmd, "manifest"), false)
	if err != nil {
		return err
	}
	if err := validatePromptSkillDocument(doc); err != nil {
		return err
	}
	agent, err := yamlMapping(doc.Content[0], "agent", false)
	if err != nil {
		return err
	}
	result := skillAttachmentResult{File: path, Skills: []skills.Reference{}}
	if sequence := yamlValue(agent, "skills"); sequence != nil {
		result.Managed = true
		if err := sequence.Decode(&result.Skills); err != nil {
			return errs.Manifest("agent.skills: %v", err)
		}
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Prompt Skill declarations: managed=%t count=%d (local configuration only)",
		result.Managed, len(result.Skills),
	))
}

func cmdPromptSkillAttach(cmd *cobra.Command, _ []string) error {
	reference := skills.Reference{Name: getFlag(cmd, "skill"), Version: getFlag(cmd, "version")}
	if err := skills.ValidateReference(reference); err != nil {
		return err
	}
	return editPromptSkill(cmd, reference, false)
}

func cmdPromptSkillRemove(cmd *cobra.Command, _ []string) error {
	return editPromptSkill(cmd, skills.Reference{Name: getFlag(cmd, "skill")}, true)
}

func editPromptSkill(cmd *cobra.Command, reference skills.Reference, remove bool) error {
	if err := rejectSkillManifestOverrides(cmd); err != nil {
		return err
	}
	path, original, doc, err := readSkillConfiguration(getFlag(cmd, "manifest"), false)
	if err != nil {
		return err
	}
	if err := validatePromptSkillDocument(doc); err != nil {
		return err
	}
	agent, err := yamlMapping(doc.Content[0], "agent", false)
	if err != nil {
		return err
	}
	sequence := yamlValue(agent, "skills")
	if sequence == nil {
		if remove {
			return errs.Config("agent.skills is unmanaged; declare the complete desired list before removing a Skill")
		}
		sequence = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		yamlSet(agent, "skills", sequence)
	}
	if sequence.Kind != yaml.SequenceNode {
		return errs.Manifest("agent.skills must be a sequence, not an alias or another YAML type")
	}
	changed := false
	found := false
	for i, entry := range sequence.Content {
		name := yamlValue(entry, "name")
		if name == nil || name.Value != reference.Name {
			continue
		}
		found = true
		if remove {
			sequence.Content = append(sequence.Content[:i], sequence.Content[i+1:]...)
			changed = true
		} else {
			version := yamlValue(entry, "version")
			if version == nil || version.Value != reference.Version {
				yamlSet(entry, "version", yamlString(reference.Version))
				changed = true
			}
		}
		break
	}
	if !found && !remove {
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		yamlSet(entry, "name", yamlString(reference.Name))
		yamlSet(entry, "version", yamlString(reference.Version))
		sequence.Content = append(sequence.Content, entry)
		changed = true
	}
	if err := validatePromptSkillDocument(doc); err != nil {
		return err
	}
	result := skillAttachmentResult{File: path, Managed: true, Changed: changed, Skills: []skills.Reference{}}
	if err := sequence.Decode(&result.Skills); err != nil {
		return errs.Manifest("agent.skills: %v", err)
	}
	if changed {
		if err := saveSkillConfiguration(path, original, doc); err != nil {
			return err
		}
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Prompt Skill declarations updated: changed=%t count=%d (local configuration only); run fam prompt preflight, then fam prompt deploy, both with -f <manifest> --accept-preview --experimental-native-skills",
		changed, len(result.Skills),
	))
}

func rejectSkillManifestOverrides(cmd *cobra.Command) error {
	for _, name := range []string{
		"location", "name", "model", "description", "instructions-file", "project-resource-id", "metadata",
	} {
		if flag := cmd.Flag(name); flag != nil && flag.Changed {
			return errs.Config("--%s does not apply to local Skill declarations; edit the manifest separately", name)
		}
	}
	return nil
}

func validatePromptSkillDocument(doc *yaml.Node) error {
	if err := validatePromptSkillYAML(doc); err != nil {
		return err
	}
	var document map[string]interface{}
	if err := doc.Decode(&document); err != nil {
		return errs.Manifest("invalid manifest: %v", err)
	}
	return config.ValidateManifest(document)
}

func validatePromptSkillYAML(doc *yaml.Node) error {
	root := doc.Content[0]
	agent := yamlValue(root, "agent")
	// Only these mappings and the Skills subtree are edited. Other fields
	// may safely retain their own aliases, anchors, and merge keys.
	for _, scope := range []struct {
		node        *yaml.Node
		path, field string
	}{
		{root, "manifest", "agent"},
		{agent, "agent", "skills"},
	} {
		if scope.node == nil {
			continue
		}
		if scope.node.Kind == yaml.AliasNode || scope.node.Anchor != "" {
			return unsupportedPromptSkillYAML(scope.path)
		}
		if scope.node.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(scope.node.Content); i += 2 {
			key := scope.node.Content[i]
			if key.Kind == yaml.AliasNode || key.Tag == "!!merge" ||
				(key.Value == scope.field && key.Anchor != "") {
				return unsupportedPromptSkillYAML(scope.path)
			}
		}
	}
	return validatePromptSkillYAMLTree(yamlValue(agent, "skills"))
}

func validatePromptSkillYAMLTree(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" || node.Tag == "!!merge" {
		return unsupportedPromptSkillYAML("agent.skills")
	}
	for _, child := range node.Content {
		if err := validatePromptSkillYAMLTree(child); err != nil {
			return err
		}
	}
	return nil
}

func unsupportedPromptSkillYAML(path string) error {
	return errs.Manifest(
		"%s uses YAML merges, aliases, or anchors that cannot be safely listed or edited as local Skill declarations; expand agent and agent.skills into explicit unanchored mappings/sequences and replace aliases referencing them before retrying",
		path,
	)
}

func readSkillConfiguration(name string, create bool) (string, []byte, *yaml.Node, error) {
	path, err := filepath.Abs(name)
	if err != nil {
		return "", nil, nil, errs.Config("failed to resolve configuration path: %v", err)
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && create {
		return path, nil, &yaml.Node{
			Kind:    yaml.DocumentNode,
			Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}},
		}, nil
	}
	if err != nil {
		return "", nil, nil, errs.Config("failed to inspect configuration %s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, nil, errs.Security("configuration must be a regular file, not a link: %s", path)
	}
	data, err := netcheck.ReadContainedFile(filepath.Dir(path), filepath.Base(path), "Skill configuration")
	if err != nil {
		return "", nil, nil, err
	}
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&doc); err != nil {
		return "", nil, nil, errs.Manifest("invalid Skill configuration: %v", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return "", nil, nil, errs.Manifest("Skill configuration must be one YAML or JSON mapping")
	}
	var value map[string]interface{}
	if err := doc.Decode(&value); err != nil {
		return "", nil, nil, errs.Manifest("invalid Skill configuration: %v", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err == nil {
		return "", nil, nil, errs.Manifest("Skill configuration must contain exactly one document")
	} else if err != io.EOF {
		return "", nil, nil, errs.Manifest("invalid trailing Skill configuration: %v", err)
	}
	return path, data, &doc, nil
}

func yamlValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func yamlString(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func yamlSet(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, yamlString(key), value)
}

func yamlMapping(parent *yaml.Node, key string, create bool) (*yaml.Node, error) {
	value := yamlValue(parent, key)
	if value == nil && create {
		value = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		yamlSet(parent, key, value)
	}
	if value == nil || value.Kind != yaml.MappingNode {
		return nil, errs.Manifest("%s must be a mapping, not an alias or another YAML type", key)
	}
	return value, nil
}

func saveSkillConfiguration(path string, original []byte, doc *yaml.Node) error {
	var data []byte
	var err error
	if strings.EqualFold(filepath.Ext(path), ".json") {
		var value map[string]interface{}
		if err := doc.Decode(&value); err != nil {
			return errs.Manifest("failed to encode configuration: %v", err)
		}
		data, err = json.MarshalIndent(value, "", "  ")
		data = append(data, '\n')
	} else {
		var buffer bytes.Buffer
		encoder := yaml.NewEncoder(&buffer)
		encoder.SetIndent(2)
		err = encoder.Encode(doc)
		closeErr := encoder.Close()
		if err == nil {
			err = closeErr
		}
		data = buffer.Bytes()
	}
	if err != nil {
		return errs.Config("failed to encode Skill configuration: %v", err)
	}
	return replaceSkillConfiguration(path, original, data)
}
