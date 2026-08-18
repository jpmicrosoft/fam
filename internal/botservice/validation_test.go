package botservice

import (
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

func TestParseBotServiceARMID(t *testing.T) {
	id := "/subscriptions/" + testSubscription +
		"/resourceGroups/" + testResourceGroup +
		"/providers/Microsoft.BotService/botServices/" + testBotName
	parsed, err := ParseBotServiceARMID(id)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SubscriptionID != testSubscription ||
		parsed.ResourceGroup != testResourceGroup ||
		parsed.BotName != testBotName {
		t.Fatalf("unexpected parsed ID: %#v", parsed)
	}
}

func TestParseBotServiceARMIDRejectsMalformedOrWrongResources(t *testing.T) {
	validPrefix := "/subscriptions/" + testSubscription +
		"/resourceGroups/" + testResourceGroup + "/providers/"
	tests := []string{
		"",
		"subscriptions/" + testSubscription + "/resourceGroups/" + testResourceGroup +
			"/providers/Microsoft.BotService/botServices/" + testBotName,
		validPrefix + "Microsoft.Web/botServices/" + testBotName,
		validPrefix + "Microsoft.BotService/bots/" + testBotName,
		validPrefix + "Microsoft.BotService/botServices/bad/name",
		validPrefix + "Microsoft.BotService/botServices/" + testBotName + "?api-version=x",
		"/subscriptions/not-a-uuid/resourceGroups/" + testResourceGroup +
			"/providers/Microsoft.BotService/botServices/" + testBotName,
	}
	for _, value := range tests {
		t.Run(strings.ReplaceAll(value, "/", "_"), func(t *testing.T) {
			if _, err := ParseBotServiceARMID(value); err == nil || !errs.IsKind(err, "config") {
				t.Fatalf("expected config error for %q, got %v", value, err)
			}
		})
	}
}

func TestStrictSegmentValidation(t *testing.T) {
	for _, value := range []string{
		"",
		"a",
		"_leading-underscore",
		".leading-dot",
		"-leading-hyphen",
		"bad/name",
		"bad%2Fname",
		"bad name",
		strings.Repeat("a", 65),
	} {
		if err := ValidateBotName(value); err == nil {
			t.Errorf("expected bot name rejection for %q", value)
		}
	}
	for _, value := range []string{"ab", "bot_name", "bot.name", "bot-name", "9bot.name"} {
		if err := ValidateBotName(value); err != nil {
			t.Errorf("expected valid bot name %q, got %v", value, err)
		}
	}
	for _, value := range []string{"", "bad/group", "bad\\group", "ends.", strings.Repeat("a", 91)} {
		if err := ValidateResourceGroup(value); err == nil {
			t.Errorf("expected resource group rejection for %q", value)
		}
	}
	for _, value := range []string{"", "subscription", testSubscription + "/extra"} {
		if err := ValidateSubscriptionID(value); err == nil {
			t.Errorf("expected subscription rejection for %q", value)
		}
	}
}

func TestBotSpecRejectsNonPublicOrMalformedEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"http://bot.example.com/api/messages",
		"https://localhost/api/messages",
		"https://127.0.0.1/api/messages",
		"https://bot.example.com",
		"https://user:pass@bot.example.com/api/messages",
		"https://bot.example.com:8443/api/messages",
		"https://bot.example.com/api/messages?token=secret",
	} {
		spec := testSpec()
		spec.Endpoint = endpoint
		if _, err := validateBotSpec(spec); err == nil || !errs.IsKind(err, "config") {
			t.Errorf("expected endpoint rejection for %q, got %v", endpoint, err)
		}
	}
}

func TestBotSpecAllowsFoundryActivityEndpointAPIVersion(t *testing.T) {
	spec := testSpec()
	spec.Endpoint = "https://account.services.ai.azure.com/api/projects/project/agents/agent/endpoint/protocols/activityProtocol?api-version=2025-05-15-preview"
	validated, err := validateBotSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Endpoint != spec.Endpoint {
		t.Fatalf("endpoint changed: got %q want %q", validated.Endpoint, spec.Endpoint)
	}
}
