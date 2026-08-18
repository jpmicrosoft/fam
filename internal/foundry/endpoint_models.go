package foundry

import (
	"encoding/json"
	"fmt"
)

const (
	LatestAgentVersion = "@latest"

	VersionSelectorFixedRatio = "FixedRatio"

	AuthorizationSchemeEntra            = "Entra"
	AuthorizationSchemeBotService       = "BotService"
	AuthorizationSchemeBotServiceRBAC   = "BotServiceRbac"
	AuthorizationSchemeBotServiceTenant = "BotServiceTenant"

	ProtocolResponses     = "responses"
	ProtocolActivity      = "activity"
	ProtocolInvocations   = "invocations"
	ProtocolInvocationsWS = "invocations_ws"
	ProtocolA2A           = "a2a"
	ProtocolMCP           = "mcp"
)

// AgentDetailsPatch is the top-level merge-patch shape for configurable agent
// details. AgentCard is a sibling of AgentEndpoint in the service contract.
type AgentDetailsPatch struct {
	AgentEndpoint *AgentEndpointConfig `json:"agent_endpoint,omitempty" yaml:"agentEndpoint,omitempty"`
	AgentCard     *AgentCard           `json:"agent_card,omitempty" yaml:"agentCard,omitempty"`
}

// AgentEndpointConfig controls version routing, protocols, and authorization.
// AdditionalFields retains service fields introduced after this client version.
type AgentEndpointConfig struct {
	VersionSelector       *VersionSelector           `json:"version_selector,omitempty" yaml:"versionSelector,omitempty"`
	ProtocolConfiguration ProtocolConfiguration      `json:"protocol_configuration,omitempty" yaml:"protocolConfiguration,omitempty"`
	AuthorizationSchemes  []AuthorizationScheme      `json:"authorization_schemes,omitempty" yaml:"authorizationSchemes,omitempty"`
	AdditionalFields      map[string]json.RawMessage `json:"-" yaml:"-"`
}

// UnmarshalJSON preserves unknown endpoint configuration fields.
func (c *AgentEndpointConfig) UnmarshalJSON(data []byte) error {
	type wire AgentEndpointConfig
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	additional, err := unknownFields(data,
		"version_selector",
		"protocol_configuration",
		"authorization_schemes",
	)
	if err != nil {
		return err
	}
	*c = AgentEndpointConfig(decoded)
	c.AdditionalFields = additional
	return nil
}

// MarshalJSON writes typed fields while retaining unknown service fields.
func (c AgentEndpointConfig) MarshalJSON() ([]byte, error) {
	type wire AgentEndpointConfig
	return marshalWithAdditional(wire(c), c.AdditionalFields)
}

// VersionSelector contains the traffic rules for an endpoint.
// Raw contains the selector exactly as received for audit receipts.
type VersionSelector struct {
	VersionSelectionRules []FixedRatioVersionSelectionRule `json:"version_selection_rules" yaml:"versionSelectionRules"`
	Raw                   json.RawMessage                  `json:"-" yaml:"-"`
	AdditionalFields      map[string]json.RawMessage       `json:"-" yaml:"-"`
}

// UnmarshalJSON retains both the original selector and unknown selector fields.
func (s *VersionSelector) UnmarshalJSON(data []byte) error {
	type wire VersionSelector
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	additional, err := unknownFields(data, "version_selection_rules")
	if err != nil {
		return err
	}
	*s = VersionSelector(decoded)
	s.Raw = cloneRawMessage(data)
	s.AdditionalFields = additional
	return nil
}

// MarshalJSON writes typed fields while retaining unknown service fields.
func (s VersionSelector) MarshalJSON() ([]byte, error) {
	type wire VersionSelector
	return marshalWithAdditional(wire(s), s.AdditionalFields)
}

// RawJSON returns a defensive copy of the selector received from Foundry.
func (s *VersionSelector) RawJSON() json.RawMessage {
	if s == nil {
		return nil
	}
	return cloneRawMessage(s.Raw)
}

// FixedRatioVersionSelectionRule routes a percentage of traffic to one version.
type FixedRatioVersionSelectionRule struct {
	Type              string                     `json:"type" yaml:"type"`
	AgentVersion      string                     `json:"agent_version" yaml:"agentVersion"`
	TrafficPercentage int                        `json:"traffic_percentage" yaml:"trafficPercentage"`
	AdditionalFields  map[string]json.RawMessage `json:"-" yaml:"-"`

	trafficPercentagePresent bool
	raw                      json.RawMessage
}

// NewFixedRatioVersionSelectionRule creates one validated-shape routing rule.
func NewFixedRatioVersionSelectionRule(version string, percentage int) FixedRatioVersionSelectionRule {
	return FixedRatioVersionSelectionRule{
		Type:                     VersionSelectorFixedRatio,
		AgentVersion:             version,
		TrafficPercentage:        percentage,
		trafficPercentagePresent: true,
	}
}

// UnmarshalJSON preserves unknown rule fields and tracks required-key presence.
func (r *FixedRatioVersionSelectionRule) UnmarshalJSON(data []byte) error {
	var decoded struct {
		Type              string `json:"type"`
		AgentVersion      string `json:"agent_version"`
		TrafficPercentage *int   `json:"traffic_percentage"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	additional, err := unknownFields(data, "type", "agent_version", "traffic_percentage")
	if err != nil {
		return err
	}
	*r = FixedRatioVersionSelectionRule{
		Type:                     decoded.Type,
		AgentVersion:             decoded.AgentVersion,
		AdditionalFields:         additional,
		trafficPercentagePresent: decoded.TrafficPercentage != nil,
		raw:                      cloneRawMessage(data),
	}
	if decoded.TrafficPercentage != nil {
		r.TrafficPercentage = *decoded.TrafficPercentage
	}
	return nil
}

// MarshalJSON writes typed fields while retaining unknown service fields.
func (r FixedRatioVersionSelectionRule) MarshalJSON() ([]byte, error) {
	known := struct {
		Type              string `json:"type"`
		AgentVersion      string `json:"agent_version"`
		TrafficPercentage int    `json:"traffic_percentage"`
	}{
		Type:              r.Type,
		AgentVersion:      r.AgentVersion,
		TrafficPercentage: r.TrafficPercentage,
	}
	return marshalWithAdditional(known, r.AdditionalFields)
}

// ProtocolConfiguration is a key-presence map. A protocol is enabled when its
// key exists, even when the configuration object is empty.
type ProtocolConfiguration map[string]json.RawMessage

// NewProtocolConfiguration enables protocols with empty configuration objects.
func NewProtocolConfiguration(protocols ...string) ProtocolConfiguration {
	configuration := make(ProtocolConfiguration, len(protocols))
	for _, protocol := range protocols {
		configuration[protocol] = json.RawMessage(`{}`)
	}
	return configuration
}

// Has reports whether a protocol key is present.
func (c ProtocolConfiguration) Has(protocol string) bool {
	_, ok := c[protocol]
	return ok
}

// AuthorizationScheme describes one endpoint authorization mechanism.
type AuthorizationScheme struct {
	Type               string                     `json:"type" yaml:"type"`
	IsolationKeySource *IsolationKeySource        `json:"isolation_key_source,omitempty" yaml:"isolationKeySource,omitempty"`
	AdditionalFields   map[string]json.RawMessage `json:"-" yaml:"-"`
}

// IsolationKeySource retains the identity source used by older endpoint shapes.
type IsolationKeySource struct {
	Kind             string                     `json:"kind" yaml:"kind"`
	AdditionalFields map[string]json.RawMessage `json:"-" yaml:"-"`
}

// UnmarshalJSON preserves authorization fields added by the service.
func (s *AuthorizationScheme) UnmarshalJSON(data []byte) error {
	var decoded struct {
		Type               string              `json:"type"`
		IsolationKeySource *IsolationKeySource `json:"isolation_key_source"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	additional, err := unknownFields(data, "type", "isolation_key_source")
	if err != nil {
		return err
	}
	*s = AuthorizationScheme{
		Type:               decoded.Type,
		IsolationKeySource: decoded.IsolationKeySource,
		AdditionalFields:   additional,
	}
	return nil
}

// MarshalJSON writes typed authorization fields and retained service fields.
func (s AuthorizationScheme) MarshalJSON() ([]byte, error) {
	known := struct {
		Type               string              `json:"type"`
		IsolationKeySource *IsolationKeySource `json:"isolation_key_source,omitempty"`
	}{
		Type:               s.Type,
		IsolationKeySource: s.IsolationKeySource,
	}
	return marshalWithAdditional(known, s.AdditionalFields)
}

// UnmarshalJSON preserves isolation-source fields added by the service.
func (s *IsolationKeySource) UnmarshalJSON(data []byte) error {
	var decoded struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	additional, err := unknownFields(data, "kind")
	if err != nil {
		return err
	}
	*s = IsolationKeySource{Kind: decoded.Kind, AdditionalFields: additional}
	return nil
}

// MarshalJSON writes the isolation source and retained service fields.
func (s IsolationKeySource) MarshalJSON() ([]byte, error) {
	known := struct {
		Kind string `json:"kind"`
	}{Kind: s.Kind}
	return marshalWithAdditional(known, s.AdditionalFields)
}

// AgentIdentity is the managed identity assigned to an agent or blueprint.
type AgentIdentity struct {
	PrincipalID string `json:"principal_id" yaml:"principalId"`
	ClientID    string `json:"client_id" yaml:"clientId"`
	Status      string `json:"status,omitempty" yaml:"status,omitempty"`
}

// AgentBlueprintReference identifies the managed blueprint backing an agent.
type AgentBlueprintReference struct {
	Type        string `json:"type" yaml:"type"`
	BlueprintID string `json:"blueprint_id,omitempty" yaml:"blueprintId,omitempty"`
}

// AgentCard describes an agent's discoverable capabilities.
type AgentCard struct {
	Version     string           `json:"version" yaml:"version"`
	Description string           `json:"description,omitempty" yaml:"description,omitempty"`
	Skills      []AgentCardSkill `json:"skills" yaml:"skills"`
}

// AgentCardSkill describes one capability advertised by an agent.
type AgentCardSkill struct {
	ID          string   `json:"id" yaml:"id"`
	Name        string   `json:"name" yaml:"name"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Tags        []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	Examples    []string `json:"examples,omitempty" yaml:"examples,omitempty"`
}

// UnmarshalJSON preserves fields added to Agent by future service versions.
func (a *Agent) UnmarshalJSON(data []byte) error {
	type wire Agent
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	additional, err := unknownFields(data,
		"object",
		"id",
		"name",
		"state",
		"agent_endpoint",
		"instance_identity",
		"blueprint",
		"blueprint_reference",
		"agent_card",
		"versions",
	)
	if err != nil {
		return err
	}
	*a = Agent(decoded)
	a.AdditionalFields = additional
	return nil
}

// MarshalJSON writes typed Agent fields while retaining unknown service fields.
func (a Agent) MarshalJSON() ([]byte, error) {
	type wire Agent
	return marshalWithAdditional(wire(a), a.AdditionalFields)
}

// IsModernAgent reports whether the response contains endpoint configuration.
func (a *Agent) IsModernAgent() bool {
	return a != nil && a.AgentEndpoint != nil
}

// IsNewAgent is an alias for IsModernAgent.
func (a *Agent) IsNewAgent() bool {
	return a.IsModernAgent()
}

// IsLegacyAgent reports whether the response uses the pre-endpoint agent shape.
func (a *Agent) IsLegacyAgent() bool {
	return a != nil && a.AgentEndpoint == nil
}

// RawVersionSelector returns the exact selector JSON received from Foundry.
func (a *Agent) RawVersionSelector() json.RawMessage {
	if a == nil || a.AgentEndpoint == nil {
		return nil
	}
	return a.AgentEndpoint.VersionSelector.RawJSON()
}

// VersionSelectorResolution safely interprets the agent's endpoint routing.
func (a *Agent) VersionSelectorResolution() SelectorResolution {
	if a == nil {
		return malformedResolution(nil, "agent is nil")
	}
	var selector *VersionSelector
	if a.AgentEndpoint != nil {
		selector = a.AgentEndpoint.VersionSelector
	}
	return ResolveVersionSelector(selector, a.Versions.Latest.Version)
}

// EffectiveActiveVersions returns every immutable version receiving traffic.
func (a *Agent) EffectiveActiveVersions() ([]string, error) {
	resolution := a.VersionSelectorResolution()
	if resolution.IsMalformed() {
		return nil, fmt.Errorf("malformed agent version selector: %s", resolution.Problem)
	}
	return append([]string(nil), resolution.ActiveVersions...), nil
}

// EffectiveActiveVersion returns the sole active version. Traffic splits are
// valid but do not have one effective version.
func (a *Agent) EffectiveActiveVersion() (string, error) {
	versions, err := a.EffectiveActiveVersions()
	if err != nil {
		return "", err
	}
	if len(versions) != 1 {
		return "", fmt.Errorf("agent endpoint routes traffic to %d versions", len(versions))
	}
	return versions[0], nil
}

// SelectorMode describes how an endpoint selects active versions.
type SelectorMode string

const (
	SelectorDefaultLatest SelectorMode = "default_latest"
	SelectorLatest        SelectorMode = "latest"
	SelectorPinned        SelectorMode = "pinned"
	SelectorSplit         SelectorMode = "split"
	SelectorMalformed     SelectorMode = "malformed"
)

// SelectorResolution is a safe interpretation of endpoint routing.
type SelectorResolution struct {
	Mode           SelectorMode
	ActiveVersions []string
	RawSelector    json.RawMessage
	Problem        string
}

// IsLatest reports explicit or default routing to the latest version.
func (r SelectorResolution) IsLatest() bool {
	return r.Mode == SelectorDefaultLatest || r.Mode == SelectorLatest
}

// IsPinned reports routing of 100 percent of traffic to one concrete version.
func (r SelectorResolution) IsPinned() bool {
	return r.Mode == SelectorPinned
}

// IsMalformed reports a selector that cannot be interpreted safely.
func (r SelectorResolution) IsMalformed() bool {
	return r.Mode == SelectorMalformed
}

// IsPinned reports whether this is a valid 100-percent concrete-version selector.
func (s *VersionSelector) IsPinned() bool {
	return ResolveVersionSelector(s, "__latest__").IsPinned()
}

// IsLatest reports whether this explicitly selects @latest.
func (s *VersionSelector) IsLatest() bool {
	return ResolveVersionSelector(s, "__latest__").Mode == SelectorLatest
}

// IsMalformed reports whether the selector cannot be interpreted safely.
func (s *VersionSelector) IsMalformed() bool {
	return ResolveVersionSelector(s, "__latest__").IsMalformed()
}

// ResolveVersionSelector interprets a selector. A nil selector defaults to the
// latest immutable version, matching the service behavior.
func ResolveVersionSelector(selector *VersionSelector, latest string) SelectorResolution {
	if selector == nil {
		if latest == "" {
			return malformedResolution(nil, "latest version is missing")
		}
		return SelectorResolution{
			Mode:           SelectorDefaultLatest,
			ActiveVersions: []string{latest},
		}
	}

	resolution := SelectorResolution{RawSelector: selector.RawJSON()}
	rules := selector.VersionSelectionRules
	if len(rules) == 0 {
		return malformedResolution(resolution.RawSelector, "version_selection_rules is empty")
	}

	total := 0
	active := make([]string, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for i, rule := range rules {
		if rule.Type != VersionSelectorFixedRatio {
			return malformedResolution(resolution.RawSelector,
				fmt.Sprintf("rule %d has unsupported type %q", i, rule.Type))
		}
		if rule.AgentVersion == "" {
			return malformedResolution(resolution.RawSelector,
				fmt.Sprintf("rule %d has no agent_version", i))
		}
		if rule.raw != nil && !rule.trafficPercentagePresent {
			return malformedResolution(resolution.RawSelector,
				fmt.Sprintf("rule %d has no traffic_percentage", i))
		}
		if rule.TrafficPercentage < 0 || rule.TrafficPercentage > 100 {
			return malformedResolution(resolution.RawSelector,
				fmt.Sprintf("rule %d traffic_percentage is outside 0..100", i))
		}
		total += rule.TrafficPercentage
		if rule.TrafficPercentage == 0 {
			continue
		}
		version := rule.AgentVersion
		if version == LatestAgentVersion {
			if latest == "" {
				return malformedResolution(resolution.RawSelector, "latest version is missing")
			}
			version = latest
		}
		if _, duplicate := seen[version]; duplicate {
			return malformedResolution(resolution.RawSelector,
				fmt.Sprintf("version %q has more than one active rule", version))
		}
		seen[version] = struct{}{}
		active = append(active, version)
	}
	if total != 100 {
		return malformedResolution(resolution.RawSelector,
			fmt.Sprintf("traffic percentages total %d instead of 100", total))
	}
	if len(active) == 0 {
		return malformedResolution(resolution.RawSelector, "selector has no active version")
	}

	resolution.ActiveVersions = active
	if len(rules) == 1 && rules[0].TrafficPercentage == 100 {
		if rules[0].AgentVersion == LatestAgentVersion {
			resolution.Mode = SelectorLatest
		} else {
			resolution.Mode = SelectorPinned
		}
		return resolution
	}
	resolution.Mode = SelectorSplit
	return resolution
}

func malformedResolution(raw json.RawMessage, problem string) SelectorResolution {
	return SelectorResolution{
		Mode:        SelectorMalformed,
		RawSelector: cloneRawMessage(raw),
		Problem:     problem,
	}
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func unknownFields(data []byte, known ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for _, key := range known {
		delete(fields, key)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return fields, nil
}

func marshalWithAdditional(known any, additional map[string]json.RawMessage) ([]byte, error) {
	data, err := json.Marshal(known)
	if err != nil {
		return nil, err
	}
	if len(additional) == 0 {
		return data, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key, value := range additional {
		if _, exists := fields[key]; !exists {
			fields[key] = cloneRawMessage(value)
		}
	}
	return json.Marshal(fields)
}
