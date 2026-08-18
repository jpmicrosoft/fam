// Package receipt persists deployment progress and compensation outcomes.
package receipt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"foundry-agent-manager/internal/redact"
)

const SchemaVersion = "foundry-agent-manager/receipt/v1"

type Step struct {
	Time    time.Time `json:"time"`
	Action  string    `json:"action"`
	Status  string    `json:"status"`
	Details string    `json:"details,omitempty"`
}

type ProjectChange struct {
	Name        string `json:"name,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Created     bool   `json:"created"`
	Compensated bool   `json:"compensated"`
}

type APIMChange struct {
	Name          string `json:"name,omitempty"`
	ExistedBefore bool   `json:"existedBefore"`
	Action        string `json:"action,omitempty"`
	Compensated   bool   `json:"compensated"`
}

type AgentChange struct {
	Name                string        `json:"name"`
	ID                  string        `json:"id,omitempty"`
	Version             string        `json:"version,omitempty"`
	CreatedVersion      string        `json:"createdVersion,omitempty"`
	LatestVersionBefore string        `json:"latestVersionBefore,omitempty"`
	LatestVersionAfter  string        `json:"latestVersionAfter,omitempty"`
	ActiveVersionBefore string        `json:"activeVersionBefore,omitempty"`
	ActiveVersionAfter  string        `json:"activeVersionAfter,omitempty"`
	SelectorBefore      SelectorState `json:"selectorBefore,omitempty"`
	SelectorAfter       SelectorState `json:"selectorAfter,omitempty"`
	Changed             bool          `json:"changed"`
	Compensated         bool          `json:"compensated"`
}

type SmokeResult struct {
	Attempted  bool   `json:"attempted"`
	Succeeded  bool   `json:"succeeded"`
	ResponseID string `json:"responseId,omitempty"`
}

// Receipt is the durable record of one deployment attempt.
type Receipt struct {
	SchemaVersion string                 `json:"schemaVersion"`
	ID            string                 `json:"id"`
	Status        string                 `json:"status"`
	StartedAt     time.Time              `json:"startedAt"`
	CompletedAt   *time.Time             `json:"completedAt,omitempty"`
	Cloud         string                 `json:"cloud"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	Manifest      string                 `json:"manifest"`
	ManifestHash  string                 `json:"manifestHash"`
	DesiredHash   string                 `json:"desiredHash"`
	Project       ProjectChange          `json:"project"`
	APIM          APIMChange             `json:"apim"`
	Agent         AgentChange            `json:"agent"`
	Smoke         SmokeResult            `json:"smoke"`
	Steps         []Step                 `json:"steps"`
	Error         string                 `json:"error,omitempty"`
}

// Store writes one receipt atomically after each state transition.
//
// Receipts are durable operator artifacts, so every value written through the
// store is filtered for credentials registered with RegisterSecret. Operator
// trust configuration (approved hosts and audiences) is deliberately never
// recorded here.
type Store struct {
	Path        string
	Receipt     Receipt
	secrets     []string
	publication *publicationTarget
}

// SetPublisher configures an optional external sink for the terminal redacted
// receipt. Intermediate state transitions remain local-only.
func (s *Store) SetPublisher(ctx context.Context, publisher Publisher) {
	s.publication = newPublicationTarget(ctx, publisher)
}

// RegisterSecret records a credential that must never appear in the receipt.
func (s *Store) RegisterSecret(value string) {
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

// Redact removes every registered credential from operator-visible text.
func (s *Store) Redact(text string) string {
	return redact.Text(text, s.secrets...)
}

func New(path, cloudName, manifestPath, manifestHash, desiredHash, agentName string) *Store {
	return &Store{
		Path: path,
		Receipt: Receipt{
			SchemaVersion: SchemaVersion,
			ID:            newID(),
			Status:        "started",
			StartedAt:     time.Now().UTC(),
			Cloud:         cloudName,
			Manifest:      manifestPath,
			ManifestHash:  manifestHash,
			DesiredHash:   desiredHash,
			Agent:         AgentChange{Name: agentName},
		},
	}
}

func DefaultPath(manifestPath, agentName string, now time.Time) string {
	base := filepath.Dir(manifestPath)
	safeName := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, agentName)
	name := fmt.Sprintf("%s-%s.json", now.UTC().Format("20060102T150405.000000000Z"), safeName)
	return filepath.Join(base, ".foundry-agent-manager", "receipts", name)
}

func (s *Store) AddStep(action, status, details string) error {
	s.Receipt.Steps = append(s.Receipt.Steps, Step{
		Time:    time.Now().UTC(),
		Action:  action,
		Status:  status,
		Details: s.Redact(details),
	})
	return s.Save()
}

func (s *Store) Complete(status string, err error) error {
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

func (s *Store) Save() error {
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
		return fmt.Errorf("failed to write deployment receipt: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("failed to flush deployment receipt: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("failed to close deployment receipt: %w", err)
	}
	if err := replaceFile(tempPath, s.Path); err != nil {
		return fmt.Errorf("failed to finalize deployment receipt: %w", err)
	}
	if err := syncDirectory(filepath.Dir(s.Path)); err != nil {
		return fmt.Errorf("failed to flush deployment receipt directory: %w", err)
	}
	return nil
}

func (s *Store) encoded() ([]byte, error) {
	data, err := json.MarshalIndent(s.Receipt, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode deployment receipt: %w", err)
	}
	// Final central sweep: no registered credential reaches the durable file,
	// even if a caller mutated Receipt fields directly.
	data = redact.Bytes(data, s.secrets...)
	return data, nil
}

func newID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("receipt-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}
