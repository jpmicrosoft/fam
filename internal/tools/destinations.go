package tools

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	errs "foundry-agent-manager/internal/errors"
)

// Destination is one outbound network destination a built tool will use at runtime.
//
// Every manifest-controlled external destination must be approved by the
// operator before deployment.
type Destination struct {
	// Type is the manifest tool type.
	Type string
	// Field locates the destination inside the built payload, for diagnostics.
	Field string
	// URL is the destination as written in the built payload.
	URL string
	// AuthType is the tool's wire auth type ("anonymous", "managed_identity", ...).
	AuthType string
	// Audience is the managed-identity token audience, when one applies.
	Audience string
}

// openAPIOperations are the OpenAPI path-item fields that may carry their own servers list.
var openAPIOperations = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// Destinations extracts every effective outbound destination from built wire tools.
//
// It fails closed: templated server URLs, server variables, and specs without a
// usable servers entry are rejected instead of being deployed with an unknown
// effective destination.
func Destinations(built []interface{}) ([]Destination, error) {
	var destinations []Destination
	for index, entry := range built {
		tool, ok := entry.(map[string]interface{})
		if !ok {
			return nil, errs.Security("tool[%d]: built tool payload is not an object", index)
		}
		switch getStr(tool, "type") {
		case "openapi":
			found, err := openAPIDestinations(index, tool)
			if err != nil {
				return nil, err
			}
			destinations = append(destinations, found...)
		case "mcp":
			label := getStr(tool, "server_label")
			destinations = append(destinations, Destination{
				Type:     "mcp",
				Field:    fmt.Sprintf("tools[%d] mcp %q server_url", index, label),
				URL:      getStr(tool, "server_url"),
				AuthType: "mcp",
			})
		case "a2a_preview":
			destinations = appendOptionalDestination(
				destinations,
				"a2a_preview",
				fmt.Sprintf("tools[%d] a2a_preview base_url", index),
				getStr(tool, "base_url"),
			)
			withAgentCard, err := appendA2AAgentCardDestination(
				destinations,
				tool,
				fmt.Sprintf("tools[%d] a2a_preview agent_card_path", index),
			)
			if err != nil {
				return nil, err
			}
			destinations = withAgentCard
		case "work_iq_preview":
			destinations = appendOptionalDestination(
				destinations,
				"work_iq_preview",
				fmt.Sprintf("tools[%d] work_iq_preview base_url", index),
				getStr(tool, "base_url"),
			)
		case "fabric_iq_preview":
			for _, field := range []string{"base_url", "server_url"} {
				destinations = appendOptionalDestination(
					destinations,
					"fabric_iq_preview",
					fmt.Sprintf("tools[%d] fabric_iq_preview %s", index, field),
					getStr(tool, field),
				)
			}
		}
	}
	return destinations, nil
}

// ToolboxDestinations extracts external destinations from managed Toolbox payloads.
func ToolboxDestinations(definitions []ToolboxDefinition) ([]Destination, error) {
	var destinations []Destination
	for toolboxIndex, toolbox := range definitions {
		for toolIndex, entry := range toolbox.Tools {
			tool, ok := entry.(map[string]interface{})
			if !ok {
				return nil, errs.Security(
					"toolboxes[%d].tools[%d]: built tool payload is not an object",
					toolboxIndex,
					toolIndex,
				)
			}
			prefix := fmt.Sprintf(
				"toolboxes[%d] %q tools[%d]",
				toolboxIndex,
				toolbox.Name,
				toolIndex,
			)
			switch getStr(tool, "type") {
			case "mcp":
				destinations = append(destinations, Destination{
					Type:     "mcp",
					Field:    prefix + " mcp server_url",
					URL:      getStr(tool, "server_url"),
					AuthType: getStrDefault(getMap(tool, "authentication"), "type", "anonymous"),
				})
			case "a2a_preview":
				destinations = appendOptionalDestination(
					destinations,
					"a2a_preview",
					prefix+" a2a_preview base_url",
					getStr(tool, "base_url"),
				)
				withAgentCard, err := appendA2AAgentCardDestination(
					destinations,
					tool,
					prefix+" a2a_preview agent_card_path",
				)
				if err != nil {
					return nil, err
				}
				destinations = withAgentCard
			case "work_iq_preview":
				destinations = appendOptionalDestination(
					destinations,
					"work_iq_preview",
					prefix+" work_iq_preview base_url",
					getStr(tool, "base_url"),
				)
			case "fabric_iq_preview":
				for _, field := range []string{"base_url", "server_url"} {
					destinations = appendOptionalDestination(
						destinations,
						"fabric_iq_preview",
						prefix+" fabric_iq_preview "+field,
						getStr(tool, field),
					)
				}
			case "openapi":
				found, err := toolboxOpenAPIDestinations(prefix, tool)
				if err != nil {
					return nil, err
				}
				destinations = append(destinations, found...)
			}
		}
	}
	return destinations, nil
}

func appendOptionalDestination(
	destinations []Destination,
	toolType string,
	field string,
	rawURL string,
) []Destination {
	if strings.TrimSpace(rawURL) == "" {
		return destinations
	}
	return append(destinations, Destination{
		Type:     toolType,
		Field:    field,
		URL:      rawURL,
		AuthType: "project_connection",
	})
}

func appendA2AAgentCardDestination(
	destinations []Destination,
	tool map[string]interface{},
	field string,
) ([]Destination, error) {
	rawURL := getStr(tool, "agent_card_path")
	if strings.TrimSpace(rawURL) == "" {
		return destinations, nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, errs.Security("%s %q is not a valid URL: %v", field, rawURL, err)
	}
	if !parsed.IsAbs() {
		return destinations, nil
	}

	authType := "anonymous"
	sendCredentials, _ := tool["send_credentials_for_agent_card"].(bool)
	if sendCredentials {
		authType = "project_connection_same_host_only"
		if baseURL, parseErr := url.Parse(getStr(tool, "base_url")); parseErr == nil &&
			baseURL.Hostname() != "" &&
			!strings.EqualFold(baseURL.Hostname(), parsed.Hostname()) {
			authType = "anonymous"
		}
	}
	return append(destinations, Destination{
		Type:     "a2a_preview",
		Field:    field,
		URL:      rawURL,
		AuthType: authType,
	}), nil
}

func toolboxOpenAPIDestinations(prefix string, tool map[string]interface{}) ([]Destination, error) {
	openAPI := getMap(tool, "openapi")
	if openAPI == nil {
		return nil, errs.Security("%s openapi: built payload has no openapi object", prefix)
	}
	auth := getMap(openAPI, "auth")
	authType := getStrDefault(auth, "type", "anonymous")
	audience := getStr(getMap(auth, "security_scheme"), "audience")
	spec, ok := openAPI["spec"].(map[string]interface{})
	if !ok {
		return nil, errs.Security("%s openapi: spec must be an object", prefix)
	}
	if err := rejectRemoteReferences(spec, prefix+" openapi spec"); err != nil {
		return nil, err
	}
	servers, err := specServerURLs(spec, prefix+" openapi")
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, errs.Security(
			"%s openapi: spec declares no servers entry, so its effective destination is unresolved",
			prefix,
		)
	}
	destinations := make([]Destination, 0, len(servers))
	for _, server := range servers {
		destinations = append(destinations, Destination{
			Type:     "openapi",
			Field:    server.field,
			URL:      server.url,
			AuthType: authType,
			Audience: audience,
		})
	}
	return destinations, nil
}

func openAPIDestinations(index int, tool map[string]interface{}) ([]Destination, error) {
	openAPI := getMap(tool, "openapi")
	if openAPI == nil {
		return nil, errs.Security("tools[%d] (openapi): built payload has no openapi object", index)
	}
	name := getStr(openAPI, "name")
	prefix := fmt.Sprintf("tools[%d] openapi %q", index, name)

	auth := getMap(openAPI, "auth")
	authType := getStrDefault(auth, "type", "anonymous")
	audience := getStr(getMap(auth, "security_scheme"), "audience")

	spec, ok := openAPI["spec"].(map[string]interface{})
	if !ok {
		return nil, errs.Security("%s: spec must be an object with an explicit servers list", prefix)
	}
	if err := rejectRemoteReferences(spec, prefix+" spec"); err != nil {
		return nil, err
	}
	servers, err := specServerURLs(spec, prefix)
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, errs.Security(
			"%s: spec declares no servers entry, so its effective destination is unresolved; "+
				"add an absolute https servers[].url that names the approved host",
			prefix,
		)
	}
	destinations := make([]Destination, 0, len(servers))
	for _, server := range servers {
		destinations = append(destinations, Destination{
			Type:     "openapi",
			Field:    server.field,
			URL:      server.url,
			AuthType: authType,
			Audience: audience,
		})
	}
	return destinations, nil
}

type serverURL struct {
	field string
	url   string
}

// specServerURLs collects the servers entries OpenAPI treats as effective:
// the document root, path items, and individual operations.
func specServerURLs(spec map[string]interface{}, prefix string) ([]serverURL, error) {
	var collected []serverURL
	rootServers, err := serversFrom(spec, prefix+" spec")
	if err != nil {
		return nil, err
	}
	collected = append(collected, rootServers...)

	for _, container := range []string{"paths", "webhooks"} {
		items := getMap(spec, container)
		found, err := pathItemServers(items, fmt.Sprintf("%s spec.%s", prefix, container))
		if err != nil {
			return nil, err
		}
		collected = append(collected, found...)
	}
	components := getMap(spec, "components")
	found, err := pathItemServers(getMap(components, "pathItems"), prefix+" spec.components.pathItems")
	if err != nil {
		return nil, err
	}
	return append(collected, found...), nil
}

func pathItemServers(items map[string]interface{}, prefix string) ([]serverURL, error) {
	if items == nil {
		return nil, nil
	}
	var collected []serverURL
	for _, key := range sortedKeys(items) {
		item, ok := items[key].(map[string]interface{})
		if !ok {
			continue
		}
		itemPrefix := fmt.Sprintf("%s[%q]", prefix, key)
		itemServers, err := serversFrom(item, itemPrefix)
		if err != nil {
			return nil, err
		}
		collected = append(collected, itemServers...)
		for _, operation := range openAPIOperations {
			operationItem := getMap(item, operation)
			if operationItem == nil {
				continue
			}
			operationServers, err := serversFrom(operationItem, itemPrefix+"."+operation)
			if err != nil {
				return nil, err
			}
			collected = append(collected, operationServers...)
		}
	}
	return collected, nil
}

// serversFrom reads one servers array and fails closed on anything ambiguous.
func serversFrom(object map[string]interface{}, prefix string) ([]serverURL, error) {
	raw, present := object["servers"]
	if !present || raw == nil {
		return nil, nil
	}
	entries, ok := raw.([]interface{})
	if !ok {
		return nil, errs.Security("%s.servers must be an array of server objects", prefix)
	}
	var collected []serverURL
	for index, entry := range entries {
		field := fmt.Sprintf("%s.servers[%d].url", prefix, index)
		server, ok := entry.(map[string]interface{})
		if !ok {
			return nil, errs.Security("%s: server entry must be an object", field)
		}
		if variables, present := server["variables"]; present && variables != nil {
			return nil, errs.Security(
				"%s: server variables make the effective destination ambiguous; "+
					"pin the server to an absolute https URL",
				field,
			)
		}
		rawURL, ok := server["url"].(string)
		if !ok || strings.TrimSpace(rawURL) == "" {
			return nil, errs.Security("%s: server entry has no url", field)
		}
		if strings.ContainsAny(rawURL, "{}") {
			return nil, errs.Security(
				"%s: templated server URL %q has no single effective destination; "+
					"pin the server to an absolute https URL",
				field, rawURL,
			)
		}
		collected = append(collected, serverURL{field: field, url: strings.TrimSpace(rawURL)})
	}
	return collected, nil
}

func sortedKeys(values map[string]interface{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// rejectRemoteReferences fails closed on $ref values that point outside the
// document. A remote reference would pull in schemas, path items, and servers
// from a destination that was never inspected or approved.
func rejectRemoteReferences(value interface{}, prefix string) error {
	switch typed := value.(type) {
	case map[string]interface{}:
		for _, key := range sortedKeys(typed) {
			if key == "$ref" {
				reference, ok := typed[key].(string)
				if !ok || !strings.HasPrefix(strings.TrimSpace(reference), "#") {
					return errs.Security(
						"%s.$ref %v is not a local reference; external OpenAPI references resolve to an "+
							"uninspected destination and are not allowed",
						prefix, typed[key],
					)
				}
				continue
			}
			if err := rejectRemoteReferences(typed[key], prefix+"."+key); err != nil {
				return err
			}
		}
	case []interface{}:
		for index, item := range typed {
			if err := rejectRemoteReferences(item, fmt.Sprintf("%s[%d]", prefix, index)); err != nil {
				return err
			}
		}
	}
	return nil
}
