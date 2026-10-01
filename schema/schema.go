// Package schema exposes the canonical manifest schema embedded in the binary.
package schema

import _ "embed"

//go:embed manifest.schema.json
var manifest []byte

//go:embed publication.schema.json
var publication []byte

//go:embed skills.schema.json
var skills []byte

// Bytes returns a copy of the embedded manifest schema.
func Bytes() []byte {
	return append([]byte(nil), manifest...)
}

// PublicationBytes returns a copy of the embedded publication schema.
func PublicationBytes() []byte {
	return append([]byte(nil), publication...)
}

// SkillsBytes returns a copy of the embedded Hosted Skill declaration schema.
func SkillsBytes() []byte {
	return append([]byte(nil), skills...)
}
