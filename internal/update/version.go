package update

import (
	"strings"

	errs "foundry-agent-manager/internal/errors"
)

type version [3]string

func parseVersion(input string) (version, error) {
	var result version
	if len(input) > 63 {
		return result, errs.Config("update version exceeds the numeric version length limit")
	}
	parts := strings.Split(strings.TrimPrefix(input, "v"), ".")
	if len(parts) != len(result) {
		return result, errs.Config("update version must be a stable major.minor.patch version, optionally prefixed with v")
	}
	for i, part := range parts {
		// Compare decimal strings rather than depending on the host's integer width.
		if part == "" || len(part) > 20 || (len(part) > 1 && part[0] == '0') ||
			(len(part) == 20 && part > "18446744073709551615") {
			return version{}, errs.Config("update version has an invalid or overflowing numeric component")
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return version{}, errs.Config("update version must not contain prerelease, build metadata, or nonnumeric components")
			}
		}
		result[i] = part
	}
	return result, nil
}

func (v version) String() string { return strings.Join(v[:], ".") }

func (v version) compare(other version) int {
	for i := range v {
		if len(v[i]) < len(other[i]) {
			return -1
		}
		if len(v[i]) > len(other[i]) {
			return 1
		}
		if comparison := strings.Compare(v[i], other[i]); comparison != 0 {
			return comparison
		}
	}
	return 0
}
