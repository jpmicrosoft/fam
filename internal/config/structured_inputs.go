package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	errs "foundry-agent-manager/internal/errors"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ValidateStructuredInputDefinitions validates the Foundry wire contract for
// prompt-agent structured inputs.
func ValidateStructuredInputDefinitions(definitions map[string]interface{}) error {
	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		rawDefinition := definitions[name]
		definition, ok := rawDefinition.(map[string]interface{})
		if !ok {
			return errs.Config("structured input definition %q must be an object", name)
		}
		required, _ := definition["required"].(bool)
		defaultValue, hasDefault := definition["default_value"]
		if !required && !hasDefault {
			return errs.Config(
				"optional structured input %q must define default_value; set required: true or provide a default",
				name,
			)
		}
		rawSchema, hasSchema := definition["schema"]
		if !hasDefault || !hasSchema {
			continue
		}
		schemaDocument, ok := rawSchema.(map[string]interface{})
		if !ok {
			return errs.Config("structured input %q schema must be an object", name)
		}
		if err := validateStructuredInputSchema(name, schemaDocument, defaultValue); err != nil {
			return errs.Config("structured input %q default_value failed schema validation: %v", name, err)
		}
	}
	return nil
}

// ValidateStructuredInputValues validates runtime values against the
// definitions stored on the prompt agent.
func ValidateStructuredInputValues(
	definitions map[string]interface{},
	values map[string]interface{},
) error {
	if err := ValidateStructuredInputDefinitions(definitions); err != nil {
		return err
	}
	for name := range values {
		if _, exists := definitions[name]; !exists {
			return errs.Config("structured input %q is not declared by agent.structured_inputs", name)
		}
	}
	for name, rawDefinition := range definitions {
		definition, ok := rawDefinition.(map[string]interface{})
		if !ok {
			return errs.Config("structured input definition %q must be an object", name)
		}
		value, provided := values[name]
		required, _ := definition["required"].(bool)
		_, hasDefault := definition["default_value"]
		if !provided {
			if required && !hasDefault {
				return errs.Config("required structured input %q was not provided", name)
			}
			continue
		}
		rawSchema, hasSchema := definition["schema"]
		if !hasSchema {
			continue
		}
		schemaDocument, ok := rawSchema.(map[string]interface{})
		if !ok {
			return errs.Config("structured input %q schema must be an object", name)
		}
		if err := validateStructuredInputSchema(name, schemaDocument, value); err != nil {
			return err
		}
	}
	return nil
}

func validateStructuredInputSchema(
	name string,
	schemaDocument map[string]interface{},
	value interface{},
) error {
	data, err := json.Marshal(schemaDocument)
	if err != nil {
		return fmt.Errorf("failed to marshal structured input %q schema: %w", name, err)
	}
	decoded, err := jsonschema.UnmarshalJSON(strings.NewReader(string(data)))
	if err != nil {
		return errs.Config("structured input %q has an invalid JSON schema: %v", name, err)
	}
	compiler := jsonschema.NewCompiler()
	resource := "structured-input-" + name + ".schema.json"
	if err := compiler.AddResource(resource, decoded); err != nil {
		return fmt.Errorf("failed to add structured input %q schema: %w", name, err)
	}
	compiled, err := compiler.Compile(resource)
	if err != nil {
		return errs.Config("structured input %q has an invalid JSON schema: %v", name, err)
	}
	if err := compiled.Validate(value); err != nil {
		return errs.Config("structured input %q failed schema validation: %v", name, err)
	}
	return nil
}
