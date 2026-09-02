package config

import "testing"

func TestValidateStructuredInputDefinitions(t *testing.T) {
	tests := []struct {
		name        string
		definitions map[string]interface{}
		wantError   bool
	}{
		{
			name: "required input",
			definitions: map[string]interface{}{
				"storeIds": map[string]interface{}{"required": true},
			},
		},
		{
			name: "optional input with valid default",
			definitions: map[string]interface{}{
				"mode": map[string]interface{}{
					"default_value": "safe",
					"schema": map[string]interface{}{
						"type": "string",
						"enum": []interface{}{"safe", "fast"},
					},
				},
			},
		},
		{
			name: "explicit optional input without default",
			definitions: map[string]interface{}{
				"mode": map[string]interface{}{"required": false},
			},
			wantError: true,
		},
		{
			name: "implicit optional input without default",
			definitions: map[string]interface{}{
				"mode": map[string]interface{}{},
			},
			wantError: true,
		},
		{
			name: "default does not match schema",
			definitions: map[string]interface{}{
				"mode": map[string]interface{}{
					"default_value": 42,
					"schema":        map[string]interface{}{"type": "string"},
				},
			},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateStructuredInputDefinitions(test.definitions)
			if test.wantError && err == nil {
				t.Fatal("expected validation error")
			}
			if !test.wantError && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

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
