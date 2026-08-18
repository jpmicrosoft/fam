package config

import (
	"encoding/json"
	"fmt"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/netcheck"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ValidateManifest validates a manifest document against the embedded JSON schema.
func ValidateManifest(doc map[string]interface{}) error {
	schemaData, err := LoadSchemaBytes()
	if err != nil {
		return fmt.Errorf("failed to load schema: %w", err)
	}

	c := jsonschema.NewCompiler()
	decoded, unmarshalErr := jsonschema.UnmarshalJSON(strings.NewReader(string(schemaData)))
	if unmarshalErr != nil {
		return fmt.Errorf("failed to unmarshal schema: %w", unmarshalErr)
	}
	if err := c.AddResource("manifest.schema.json", decoded); err != nil {
		return fmt.Errorf("failed to add schema resource: %w", err)
	}
	sch, err := c.Compile("manifest.schema.json")
	if err != nil {
		return fmt.Errorf("failed to compile schema: %w", err)
	}

	// Convert doc to JSON and back to get consistent types for validation
	jsonBytes, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}
	var instance interface{}
	if err := json.Unmarshal(jsonBytes, &instance); err != nil {
		return fmt.Errorf("failed to unmarshal manifest: %w", err)
	}

	validationErr := sch.Validate(instance)
	if validationErr != nil {
		return errs.Manifest("manifest failed schema validation: %s", formatValidationError(validationErr))
	}
	if err := validateSpecFileReferences(doc); err != nil {
		return err
	}
	return nil
}

func validateSpecFileReferences(doc map[string]interface{}) error {
	if err := validateToolSpecFileReferences(doc["tools"], "tools"); err != nil {
		return err
	}
	rawToolboxes, _ := doc["toolboxes"].([]interface{})
	for toolboxIndex, rawToolbox := range rawToolboxes {
		toolbox, ok := rawToolbox.(map[string]interface{})
		if !ok {
			continue
		}
		if err := validateToolSpecFileReferences(
			toolbox["tools"],
			fmt.Sprintf("toolboxes/%d/tools", toolboxIndex),
		); err != nil {
			return err
		}
	}
	if grounding, ok := doc["grounding"].(map[string]interface{}); ok {
		rawStores, _ := grounding["vector_stores"].([]interface{})
		for storeIndex, rawStore := range rawStores {
			store, _ := rawStore.(map[string]interface{})
			rawFiles, _ := store["files"].([]interface{})
			for fileIndex, rawFile := range rawFiles {
				file, _ := rawFile.(map[string]interface{})
				path, _ := file["path"].(string)
				field := fmt.Sprintf(
					"grounding/vector_stores/%d/files/%d/path",
					storeIndex,
					fileIndex,
				)
				if err := netcheck.ValidateRelativeFileReference(path, field); err != nil {
					return errs.Manifest("manifest failed schema validation: %s", err)
				}
			}
		}
	}
	return nil
}

func validateToolSpecFileReferences(raw interface{}, prefix string) error {
	rawTools, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	for i, rawTool := range rawTools {
		tool, ok := rawTool.(map[string]interface{})
		if !ok || tool["type"] != "openapi" {
			continue
		}
		specFile, ok := tool["spec_file"].(string)
		if !ok {
			continue
		}
		field := fmt.Sprintf("%s/%d/spec_file", prefix, i)
		if err := netcheck.ValidateRelativeFileReference(specFile, field); err != nil {
			return errs.Manifest("manifest failed schema validation: %s", err)
		}
	}
	return nil
}

func formatValidationError(err error) string {
	if ve, ok := err.(*jsonschema.ValidationError); ok {
		var msgs []string
		collectErrors(ve, &msgs)
		if len(msgs) > 0 {
			return strings.Join(msgs, "; ")
		}
		return ve.Error()
	}
	return err.Error()
}

func collectErrors(ve *jsonschema.ValidationError, msgs *[]string) {
	if len(ve.Causes) == 0 {
		path := strings.Join(ve.InstanceLocation, "/")
		if path == "" {
			path = "<root>"
		}
		*msgs = append(*msgs, fmt.Sprintf("%s: %s", path, ve.Error()))
		return
	}
	for _, cause := range ve.Causes {
		collectErrors(cause, msgs)
	}
}
