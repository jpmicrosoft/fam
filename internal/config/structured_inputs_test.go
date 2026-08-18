package config

import "testing"

func TestValidateStructuredInputValues(t *testing.T) {
	definitions := map[string]interface{}{
		"vectorStoreIds": map[string]interface{}{
			"required": true,
			"schema": map[string]interface{}{
				"type":  "array",
				"items": map[string]interface{}{"type": "string"},
			},
		},
		"mode": map[string]interface{}{
			"default_value": "safe",
			"schema": map[string]interface{}{
				"type": "string",
				"enum": []interface{}{"safe", "fast"},
			},
		},
	}
	if err := ValidateStructuredInputValues(definitions, map[string]interface{}{
		"vectorStoreIds": []interface{}{"vs-1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStructuredInputValues(definitions, nil); err == nil {
		t.Fatal("expected missing required value to fail")
	}
	if err := ValidateStructuredInputValues(definitions, map[string]interface{}{
		"vectorStoreIds": []interface{}{1},
	}); err == nil {
		t.Fatal("expected schema mismatch to fail")
	}
	if err := ValidateStructuredInputValues(definitions, map[string]interface{}{
		"vectorStoreIds": []interface{}{"vs-1"},
		"unknown":        true,
	}); err == nil {
		t.Fatal("expected undeclared value to fail")
	}
}
