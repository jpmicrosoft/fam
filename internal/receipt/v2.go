package receipt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"foundry-agent-manager/internal/redact"
)

const SchemaVersionV2 = "foundry-agent-manager/receipt/v2"

// ManifestReference identifies the local desired-state input without copying
// its potentially sensitive content into the receipt.
type ManifestReference struct {
	Path        string `json:"path,omitempty"`
	Hash        string `json:"hash,omitempty"`
	DesiredHash string `json:"desiredHash,omitempty"`
}

// ResourceReference identifies an Azure or Foundry resource touched by an
// operation.
type ResourceReference struct {
	Name     string `json:"name,omitempty"`
	ID       string `json:"id,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
}

// SelectorState records endpoint routing before or after a release operation.
// Raw is limited to the version selector object and must never contain tokens
// or operator trust configuration.
type SelectorState struct {
	Mode          string          `json:"mode,omitempty"`
	ActiveVersion string          `json:"activeVersion,omitempty"`
	Raw           json.RawMessage `json:"raw,omitempty"`
}

// AgentReleaseChange records immutable-version creation and stable endpoint
// routing independently.
type AgentReleaseChange struct {
	Name                string        `json:"name"`
	ID                  string        `json:"id,omitempty"`
	LatestVersionBefore string        `json:"latestVersionBefore,omitempty"`
	LatestVersionAfter  string        `json:"latestVersionAfter,omitempty"`
	ActiveVersionBefore string        `json:"activeVersionBefore,omitempty"`
	ActiveVersionAfter  string        `json:"activeVersionAfter,omitempty"`
	CreatedVersion      string        `json:"createdVersion,omitempty"`
	SelectorBefore      SelectorState `json:"selectorBefore,omitempty"`
	SelectorAfter       SelectorState `json:"selectorAfter,omitempty"`
	Changed             bool          `json:"changed"`
	Compensated         bool          `json:"compensated"`
}

// ResourceChange records an auxiliary resource mutation, such as a Bot Service,
// Teams channel, APIM connection, or legacy Agent Application.
type ResourceChange struct {
	Kind           string `json:"kind"`
	Name           string `json:"name,omitempty"`
	ID             string `json:"id,omitempty"`
	Action         string `json:"action,omitempty"`
	Status         string `json:"status,omitempty"`
	CreatedByRun   bool   `json:"createdByRun"`
	Compensated    bool   `json:"compensated"`
	Reconciliation string `json:"reconciliation,omitempty"`
}

// ExternalAction records a mutation whose lifecycle is owned by another
// control plane, such as Microsoft 365 publication or tenant approval.
type ExternalAction struct {
	Kind           string     `json:"kind"`
	System         string     `json:"system"`
	Status         string     `json:"status"`
	IdempotencyKey string     `json:"idempotencyKey,omitempty"`
	RequestHash    string     `json:"requestHash,omitempty"`
	ResourceID     string     `json:"resourceId,omitempty"`
	Irreversible   bool       `json:"irreversible"`
	Compensation   string     `json:"compensation,omitempty"`
	Reconciliation string     `json:"reconciliation,omitempty"`
	StartedAt      time.Time  `json:"startedAt"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
}

// CommandRecord records an external executable invocation without environment
// values or command output. Args must contain no secrets.
type CommandRecord struct {
	Phase      string        `json:"phase"`
	Executable string        `json:"executable"`
	Args       []string      `json:"args"`
	Directory  string        `json:"directory"`
	ExitCode   int           `json:"exitCode"`
	Duration   time.Duration `json:"duration"`
}

// ReceiptV2 is the operation-oriented journal used by release, endpoint,
// publication, legacy compatibility, and experimental workflows.
type ReceiptV2 struct {
	SchemaVersion   string                 `json:"schemaVersion"`
	ID              string                 `json:"id"`
	Operation       string                 `json:"operation"`
	Status          string                 `json:"status"`
	StartedAt       time.Time              `json:"startedAt"`
	CompletedAt     *time.Time             `json:"completedAt,omitempty"`
	Cloud           string                 `json:"cloud"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
	Manifest        ManifestReference      `json:"manifest"`
	Project         ResourceReference      `json:"project"`
	Agent           AgentReleaseChange     `json:"agent"`
	Resources       []ResourceChange       `json:"resources,omitempty"`
	ExternalActions []ExternalAction       `json:"externalActions,omitempty"`
	Commands        []CommandRecord        `json:"commands,omitempty"`
	Steps           []Step                 `json:"steps"`
	Error           string                 `json:"error,omitempty"`
}

// OperationStore writes a v2 receipt atomically after every transition.
type OperationStore struct {
	Path        string
	Receipt     ReceiptV2
	secrets     []string
	publication *publicationTarget
}

// SetPublisher configures an optional external sink for the terminal redacted
// receipt. Intermediate state transitions remain local-only.
func (s *OperationStore) SetPublisher(ctx context.Context, publisher Publisher) {
	s.publication = newPublicationTarget(ctx, publisher)
}

func NewOperation(
	path string,
	operation string,
	cloudName string,
	manifest ManifestReference,
	project ResourceReference,
	agentName string,
) *OperationStore {
	return &OperationStore{
		Path: path,
		Receipt: ReceiptV2{
			SchemaVersion: SchemaVersionV2,
			ID:            newID(),
			Operation:     operation,
			Status:        "started",
			StartedAt:     time.Now().UTC(),
			Cloud:         cloudName,
			Manifest:      manifest,
			Project:       project,
			Agent:         AgentReleaseChange{Name: agentName},
		},
	}
}

func OperationPath(manifestPath, operation, agentName string, now time.Time) string {
	base := filepath.Dir(manifestPath)
	name := fmt.Sprintf(
		"%s-%s-%s.json",
		now.UTC().Format("20060102T150405.000000000Z"),
		safeComponent(operation),
		safeComponent(agentName),
	)
	return filepath.Join(base, ".foundry-agent-manager", "receipts", name)
}

func safeComponent(value string) string {
	value = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' ||
			r == '_' {
			return r
		}
		return '-'
	}, value)
	value = strings.Trim(value, "-")
	if value == "" {
		return "operation"
	}
	return value
}

func (s *OperationStore) RegisterSecret(value string) {
	if len(value) < redact.MinLength {
		return
	}
	for _, existing := range s.secrets {
		if existing == value {
			return
		}
	}
	s.secrets = append(s.secrets, value)
}

func (s *OperationStore) Redact(text string) string {
	return redact.Text(text, s.secrets...)
}

func (s *OperationStore) AddStep(action, status, details string) error {
	s.Receipt.Steps = append(s.Receipt.Steps, Step{
		Time:    time.Now().UTC(),
		Action:  action,
		Status:  status,
		Details: s.Redact(details),
	})
	return s.Save()
}

func (s *OperationStore) AddResource(change ResourceChange) error {
	change.Reconciliation = s.Redact(change.Reconciliation)
	s.Receipt.Resources = append(s.Receipt.Resources, change)
	return s.Save()
}

func (s *OperationStore) AddExternalAction(action ExternalAction) error {
	action.Reconciliation = s.Redact(action.Reconciliation)
	s.Receipt.ExternalActions = append(s.Receipt.ExternalActions, action)
	return s.Save()
}

func (s *OperationStore) Complete(status string, err error) error {
	now := time.Now().UTC()
	s.Receipt.Status = status
	s.Receipt.CompletedAt = &now
	if err != nil {
		s.Receipt.Error = s.Redact(err.Error())
	}
	if err := s.Save(); err != nil {
		return err
	}
	data, err := s.encoded()
	if err != nil {
		return err
	}
	return publish(s.publication, data)
}

func (s *OperationStore) Save() error {
	if s.Path == "" {
		return nil
	}
	data, err := s.encoded()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("failed to create receipt directory: %w", err)
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(s.Path), ".receipt-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temporary receipt: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("failed to secure temporary receipt: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("failed to write operation receipt: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("failed to flush operation receipt: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("failed to close operation receipt: %w", err)
	}
	if err := replaceFile(tempPath, s.Path); err != nil {
		return fmt.Errorf("failed to finalize operation receipt: %w", err)
	}
	if err := syncDirectory(filepath.Dir(s.Path)); err != nil {
		return fmt.Errorf("failed to flush operation receipt directory: %w", err)
	}
	return nil
}

func (s *OperationStore) encoded() ([]byte, error) {
	data, err := json.MarshalIndent(s.Receipt, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode operation receipt: %w", err)
	}
	return redact.Bytes(data, s.secrets...), nil
}
