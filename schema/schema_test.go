package schema

import (
	"encoding/json"
	"os"
	"testing"
)

func TestBytesReturnsTheEmbeddedSchemaFile(t *testing.T) {
	onDisk, err := os.ReadFile("manifest.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	embedded := Bytes()
	if string(embedded) != string(onDisk) {
		t.Fatal("the embedded schema does not match manifest.schema.json")
	}
	var document map[string]interface{}
	if err := json.Unmarshal(embedded, &document); err != nil {
		t.Fatalf("the embedded schema is not valid JSON: %v", err)
	}
	if document["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("unexpected schema dialect: %v", document["$schema"])
	}
}

// TestBytesReturnsADefensiveCopy proves a caller cannot corrupt the schema that
// every later validation depends on.
func TestBytesReturnsADefensiveCopy(t *testing.T) {
	first := Bytes()
	if len(first) == 0 {
		t.Fatal("the embedded schema must not be empty")
	}
	original := first[0]
	first[0] = 'X'
	second := Bytes()
	if second[0] != original {
		t.Fatal("Bytes must return an independent copy of the embedded schema")
	}
}

func TestPublicationBytesReturnsTheEmbeddedSchemaFile(t *testing.T) {
	onDisk, err := os.ReadFile("publication.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	embedded := PublicationBytes()
	if string(embedded) != string(onDisk) {
		t.Fatal("the embedded schema does not match publication.schema.json")
	}
	var document map[string]interface{}
	if err := json.Unmarshal(embedded, &document); err != nil {
		t.Fatalf("the embedded publication schema is not valid JSON: %v", err)
	}
	if document["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("unexpected publication schema dialect: %v", document["$schema"])
	}

	original := embedded[0]
	embedded[0] = 'X'
	if PublicationBytes()[0] != original {
		t.Fatal("PublicationBytes must return an independent copy of the embedded schema")
	}
}
