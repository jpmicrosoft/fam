// Package grounding validates managed document-grounding definitions.
package grounding

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/netcheck"
)

const (
	MetadataManagedBy   = "foundry_agent_manager"
	MetadataLogicalName = "logical_name"
	MetadataDesiredHash = "desired_hash"
	MetadataValue       = "true"

	AttributeManaged  = "fam_managed"
	AttributePathHash = "fam_path_hash"
	AttributeSHA256   = "fam_sha256"
	AttributeFilename = "fam_filename"

	managedUploadPrefix       = "fam-"
	maxManagedUploadBaseBytes = 120
)

var namePattern = regexp.MustCompile(
	`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,62}[A-Za-z0-9])?$`,
)

var supportedExtensions = map[string]struct{}{
	".c": {}, ".cpp": {}, ".cs": {}, ".css": {}, ".doc": {}, ".docx": {},
	".html": {}, ".java": {}, ".js": {}, ".json": {}, ".md": {}, ".pdf": {},
	".php": {}, ".pptx": {}, ".py": {}, ".rb": {}, ".sh": {}, ".tex": {},
	".ts": {}, ".txt": {},
}

// File is one local document managed in a Foundry vector store.
type File struct {
	Path     string `json:"path" yaml:"path"`
	Filename string `json:"filename" yaml:"filename"`
	PathHash string `json:"pathHash" yaml:"pathHash"`
	SHA256   string `json:"sha256,omitempty" yaml:"sha256,omitempty"`
	Size     int64  `json:"size,omitempty" yaml:"size,omitempty"`
}

// VectorStore is one logical manager-owned document grounding source.
type VectorStore struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Files       []File `json:"files" yaml:"files"`
	DesiredHash string `json:"desiredHash,omitempty" yaml:"desiredHash,omitempty"`
}

// Build validates and optionally inspects all declared vector stores.
func Build(raw []map[string]interface{}, baseDir string, inspectFiles bool) ([]VectorStore, error) {
	definitions := make([]VectorStore, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for index, document := range raw {
		definition, err := buildOne(document, baseDir, inspectFiles)
		if err != nil {
			if errs.IsKind(err, "security") {
				return nil, errs.SecurityWrap(err, "grounding.vector_stores[%d]", index)
			}
			return nil, errs.ManifestWrap(err, "grounding.vector_stores[%d]", index)
		}
		key := strings.ToLower(definition.Name)
		if _, exists := seen[key]; exists {
			return nil, errs.Manifest(
				"grounding.vector_stores[%d]: duplicate name %q",
				index,
				definition.Name,
			)
		}
		seen[key] = struct{}{}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func buildOne(
	document map[string]interface{},
	baseDir string,
	inspectFiles bool,
) (VectorStore, error) {
	name := stringValue(document, "name")
	if !namePattern.MatchString(name) {
		return VectorStore{}, fmt.Errorf(
			"name %q must be 1-64 alphanumeric, dot, underscore, or hyphen characters and start and end with an alphanumeric character",
			name,
		)
	}
	rawFiles := mapSlice(document, "files")
	if len(rawFiles) == 0 {
		return VectorStore{}, fmt.Errorf("at least one file is required")
	}
	definition := VectorStore{
		Name:        name,
		Description: stringValue(document, "description"),
		Files:       make([]File, 0, len(rawFiles)),
	}
	seenPaths := make(map[string]struct{}, len(rawFiles))
	for index, rawFile := range rawFiles {
		relative := filepath.Clean(stringValue(rawFile, "path"))
		if relative == "." || relative == "" {
			return VectorStore{}, fmt.Errorf("files[%d].path is required", index)
		}
		if err := netcheck.ValidateRelativeFileReference(
			relative,
			fmt.Sprintf("grounding.vector_stores[%s].files[%d].path", name, index),
		); err != nil {
			return VectorStore{}, err
		}
		extension := strings.ToLower(filepath.Ext(relative))
		if _, supported := supportedExtensions[extension]; !supported {
			return VectorStore{}, fmt.Errorf(
				"files[%d].path %q uses unsupported extension %q",
				index,
				relative,
				extension,
			)
		}
		normalized := filepath.ToSlash(relative)
		if _, exists := seenPaths[strings.ToLower(normalized)]; exists {
			return VectorStore{}, fmt.Errorf(
				"files[%d].path %q is duplicated",
				index,
				relative,
			)
		}
		seenPaths[strings.ToLower(normalized)] = struct{}{}
		pathSum := sha256.Sum256([]byte(normalized))
		file := File{
			Path:     normalized,
			Filename: filepath.Base(relative),
			PathHash: hex.EncodeToString(pathSum[:]),
		}
		if inspectFiles {
			opened, info, err := netcheck.OpenContainedFile(
				baseDir,
				relative,
				fmt.Sprintf("grounding vector store %q file %q", name, relative),
				netcheck.MaxGroundingFileBytes,
			)
			if err != nil {
				return VectorStore{}, err
			}
			sum := sha256.New()
			_, copyErr := io.Copy(sum, opened)
			closeErr := opened.Close()
			if copyErr != nil {
				return VectorStore{}, fmt.Errorf("failed to hash %q: %w", relative, copyErr)
			}
			if closeErr != nil {
				return VectorStore{}, fmt.Errorf("failed to close %q after hashing: %w", relative, closeErr)
			}
			file.Size = info.Size()
			file.SHA256 = hex.EncodeToString(sum.Sum(nil))
		}
		definition.Files = append(definition.Files, file)
	}
	if inspectFiles {
		definition.DesiredHash = desiredHash(definition)
	}
	return definition, nil
}

func desiredHash(definition VectorStore) string {
	files := append([]File(nil), definition.Files...)
	sort.Slice(files, func(i, j int) bool {
		return files[i].PathHash < files[j].PathHash
	})
	payload := struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Files       []File `json:"files"`
	}{
		Name:        definition.Name,
		Description: definition.Description,
		Files:       files,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Metadata returns the manager ownership metadata for a completed sync.
func Metadata(definition VectorStore, desiredHash string) map[string]interface{} {
	return map[string]interface{}{
		MetadataManagedBy:   MetadataValue,
		MetadataLogicalName: definition.Name,
		MetadataDesiredHash: desiredHash,
	}
}

// InProgressMetadata marks a store as manager-owned but not yet deployable.
func InProgressMetadata(definition VectorStore) map[string]interface{} {
	return Metadata(definition, "")
}

// FileAttributes identifies one managed vector-store attachment without
// exposing the local path.
func FileAttributes(file File) map[string]interface{} {
	return map[string]interface{}{
		AttributeManaged:  MetadataValue,
		AttributePathHash: file.PathHash,
		AttributeSHA256:   file.SHA256,
		AttributeFilename: file.Filename,
	}
}

// ManagedUploadFilename encodes stable file identity in the service-retained
// filename because some Foundry project APIs omit vector-store attributes.
func ManagedUploadFilename(file File) string {
	return managedUploadPrefix + file.PathHash + "-" + file.SHA256 + "-" +
		truncateFilename(file.Filename, maxManagedUploadBaseBytes)
}

// FileAttributesFromUploadFilename reconstructs manager attributes from a
// filename produced by ManagedUploadFilename.
func FileAttributesFromUploadFilename(filename string) (map[string]interface{}, bool) {
	if !strings.HasPrefix(filename, managedUploadPrefix) {
		return nil, false
	}
	parts := strings.SplitN(strings.TrimPrefix(filename, managedUploadPrefix), "-", 3)
	if len(parts) != 3 || !isSHA256(parts[0]) || !isSHA256(parts[1]) || parts[2] == "" {
		return nil, false
	}
	return map[string]interface{}{
		AttributeManaged:  MetadataValue,
		AttributePathHash: parts[0],
		AttributeSHA256:   parts[1],
		AttributeFilename: parts[2],
	}, true
}

// ManagedStore reports whether metadata identifies a manager-owned logical store.
func ManagedStore(metadata map[string]interface{}, name string) bool {
	return stringValue(metadata, MetadataManagedBy) == MetadataValue &&
		strings.EqualFold(stringValue(metadata, MetadataLogicalName), name)
}

// DesiredStore reports whether the remote metadata matches the local content.
func DesiredStore(metadata map[string]interface{}, definition VectorStore) bool {
	return ManagedStore(metadata, definition.Name) &&
		stringValue(metadata, MetadataDesiredHash) == definition.DesiredHash
}

// ManagedFile reports whether attributes identify a manager-owned attachment.
func ManagedFile(attributes map[string]interface{}) bool {
	return stringValue(attributes, AttributeManaged) == MetadataValue
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func truncateFilename(filename string, maxBytes int) string {
	if len([]byte(filename)) <= maxBytes {
		return filename
	}
	extension := filepath.Ext(filename)
	maxStemBytes := maxBytes - len([]byte(extension))
	if maxStemBytes <= 0 {
		return truncateUTF8(filename, maxBytes)
	}
	stem := strings.TrimSuffix(filename, extension)
	return truncateUTF8(stem, maxStemBytes) + extension
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	for len([]byte(value)) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}

func stringValue(document map[string]interface{}, key string) string {
	value, _ := document[key].(string)
	return strings.TrimSpace(value)
}

func mapSlice(document map[string]interface{}, key string) []map[string]interface{} {
	raw, _ := document[key].([]interface{})
	result := make([]map[string]interface{}, 0, len(raw))
	for _, item := range raw {
		if mapped, ok := item.(map[string]interface{}); ok {
			result = append(result, mapped)
		}
	}
	return result
}
