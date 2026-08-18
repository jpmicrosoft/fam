package custommetadata

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseAssignmentsSupportsOverridesAndEqualsInValues(t *testing.T) {
	metadata, err := ParseAssignments([]string{
		"owner=platform",
		"query=a=b",
		"owner=operations",
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata["owner"] != "operations" || metadata["query"] != "a=b" {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
}

func TestValidateEnforcesFoundryLimits(t *testing.T) {
	tooMany := make(map[string]string, MaxEntries+1)
	for i := 0; i < MaxEntries+1; i++ {
		tooMany[fmt.Sprintf("key-%d", i)] = "value"
	}
	for name, value := range map[string]map[string]string{
		"too many":      tooMany,
		"empty key":     {"": "value"},
		"long key":      {strings.Repeat("k", MaxKeyLength+1): "value"},
		"long value":    {"key": strings.Repeat("v", MaxValueLength+1)},
		"invalid utf-8": {"key": string([]byte{0xff})},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Validate(value); err == nil {
				t.Fatalf("expected invalid metadata to fail: %#v", value)
			}
		})
	}
}

func TestParseAssignmentsRejectsMissingEquals(t *testing.T) {
	if _, err := ParseAssignments([]string{"owner"}); err == nil {
		t.Fatal("expected missing key=value separator to fail")
	}
}

func TestHostedMapPreservesAuthorsList(t *testing.T) {
	metadata, err := HostedMap(map[string]interface{}{
		"owner":   "platform",
		"authors": []interface{}{"Ada", "Grace"},
	})
	if err != nil {
		t.Fatal(err)
	}
	authors, ok := metadata["authors"].([]string)
	if !ok || len(authors) != 2 || authors[1] != "Grace" {
		t.Fatalf("unexpected Hosted metadata: %#v", metadata)
	}
}
