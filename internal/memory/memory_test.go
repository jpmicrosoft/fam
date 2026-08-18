package memory

import "testing"

func TestBuildAppliesDocumentedDefaults(t *testing.T) {
	definitions, err := Build([]map[string]interface{}{{
		"name":            "assistant-memory",
		"chat_model":      "gpt-5.2",
		"embedding_model": "text-embedding-3-small",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 ||
		!definitions[0].Options.UserProfileEnabled ||
		!definitions[0].Options.ChatSummaryEnabled ||
		definitions[0].DesiredHash == "" {
		t.Fatalf("unexpected definition: %#v", definitions)
	}
	store := definitions[0].Store()
	if store.Definition.Kind != "default" ||
		store.Definition.ChatModel != "gpt-5.2" ||
		store.Definition.EmbeddingModel != "text-embedding-3-small" {
		t.Fatalf("unexpected wire store: %#v", store)
	}
}

func TestBuildRejectsDuplicateNames(t *testing.T) {
	_, err := Build([]map[string]interface{}{
		{"name": "memory", "chat_model": "chat", "embedding_model": "embed"},
		{"name": "MEMORY", "chat_model": "chat", "embedding_model": "embed"},
	})
	if err == nil {
		t.Fatal("expected duplicate memory store names to fail")
	}
}
