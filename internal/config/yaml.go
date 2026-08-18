package config

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// parseYAML parses YAML data into a map, normalizing types to be JSON-compatible.
func parseYAML(data []byte) (map[string]interface{}, error) {
	var raw interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	normalized := normalize(raw)
	m, ok := normalized.(map[string]interface{})
	if !ok {
		return nil, nil
	}
	return m, nil
}

// normalize converts YAML-decoded types to JSON-compatible types.
// yaml.v3 decodes maps as map[string]interface{} already, but we need to
// handle nested structures and ensure arrays are []interface{}.
func normalize(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(val))
		for k, v2 := range val {
			result[k] = normalize(v2)
		}
		return result
	case map[interface{}]interface{}:
		result := make(map[string]interface{}, len(val))
		for key, value := range val {
			result[fmt.Sprint(key)] = normalize(value)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, v2 := range val {
			result[i] = normalize(v2)
		}
		return result
	case int:
		return float64(val)
	case int64:
		return float64(val)
	default:
		return val
	}
}

// ManifestToJSON converts a manifest map to JSON bytes for schema validation.
func ManifestToJSON(doc map[string]interface{}) ([]byte, error) {
	return json.Marshal(doc)
}
