package update

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	errs "foundry-agent-manager/internal/errors"
)

const releaseEndpoint = "https://api.github.com/repos/jpmicrosoft/fam/releases/"

type asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type release struct {
	TagName    string  `json:"tag_name"`
	Draft      *bool   `json:"draft"`
	Prerelease *bool   `json:"prerelease"`
	Assets     []asset `json:"assets"`
}

func assetEndpoint(id int64) string {
	return releaseEndpoint + "assets/" + strconv.FormatInt(id, 10)
}

func (u *Updater) release(ctx context.Context) (release, error) {
	endpoint := releaseEndpoint + "latest"
	if u.options.Version != "" {
		endpoint = releaseEndpoint + "tags/v" + u.wanted.String()
	}
	data, err := u.get(ctx, endpoint, true, u.limits.metadata)
	if err != nil {
		return release{}, err
	}
	var result release
	if err := uniqueJSON(data); err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, errs.Security("GitHub release metadata is malformed")
	}
	v, err := parseVersion(result.TagName)
	if err != nil || result.TagName != "v"+v.String() ||
		result.Draft == nil || result.Prerelease == nil || *result.Draft || *result.Prerelease {
		return result, errs.Security("GitHub release is not a published stable v-prefixed numeric version")
	}
	if u.options.Version != "" && v != u.wanted {
		return result, errs.Security("GitHub release tag does not match the requested version")
	}
	return result, nil
}

func (u *Updater) selectAssets(release release, target version) (asset, asset, error) {
	preferred := "fam_" + target.String() + archiveSuffix(u.goos, u.arch)
	legacy := "foundry-agent-manager_" + target.String() + archiveSuffix(u.goos, u.arch)
	if len(release.Assets) > 128 {
		return asset{}, asset{}, errs.Security("GitHub release exceeds the asset count limit")
	}
	names := make(map[string]asset, len(release.Assets))
	ids := make(map[int64]bool, len(release.Assets))
	for _, item := range release.Assets {
		if item.ID <= 0 || ids[item.ID] || !isSafeRootName(item.Name) {
			return asset{}, asset{}, errs.Security("GitHub release contains invalid or duplicate asset identifiers")
		}
		if _, exists := names[item.Name]; exists {
			return asset{}, asset{}, errs.Security("GitHub release contains duplicate asset names")
		}
		names[item.Name], ids[item.ID] = item, true
	}
	archive, found := names[preferred]
	if !found {
		archive, found = names[legacy]
	}
	if !found {
		return asset{}, asset{}, errs.NotFound("stable release does not contain the FAM archive for this platform")
	}
	checksum, found := names["SHA256SUMS"]
	if !found {
		return asset{}, asset{}, errs.Security("stable release is missing mandatory SHA256SUMS")
	}
	return archive, checksum, nil
}

func checksumFor(data []byte, name string) ([32]byte, error) {
	var result [32]byte
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		// Accept the text and binary forms emitted by sha256sum, not substring
		// matches, paths, escaped filenames, or arbitrary whitespace suffixes.
		if len(line) < 67 || line[64] != ' ' || (line[65] != ' ' && line[65] != '*') {
			return result, errs.Security("SHA256SUMS contains a malformed checksum line")
		}
		digest, err := hex.DecodeString(line[:64])
		if err != nil || len(digest) != len(result) || !isSafeRootName(line[66:]) {
			return result, errs.Security("SHA256SUMS contains an invalid hash or filename")
		}
		if line[66:] != name {
			continue
		}
		if found {
			return result, errs.Security("SHA256SUMS contains duplicate archive checksums")
		}
		copy(result[:], digest)
		found = true
	}
	if !found {
		return result, errs.Security("SHA256SUMS is missing the exact archive filename")
	}
	return result, nil
}

// encoding/json normally accepts duplicate keys. Release selection must not
// depend on which of two conflicting security fields happens to appear last.
func uniqueJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := jsonValue(decoder, 0); err != nil {
		return errs.Security("GitHub release metadata contains malformed, duplicate, or excessively nested fields")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errs.Security("GitHub release metadata has trailing content")
	}
	return nil
}

func jsonValue(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return errs.Security("JSON nesting limit exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errs.Security("invalid JSON key")
			}
			for _, c := range name {
				if c > 127 {
					return errs.Security("non-ASCII GitHub metadata key")
				}
			}
			// encoding/json matches struct fields case-insensitively.
			name = strings.ToLower(name)
			if keys[name] {
				return errs.Security("duplicate JSON key")
			}
			keys[name] = true
			if err := jsonValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := jsonValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errs.Security("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}
