package modelrouting

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

const SchemaVersion = 3

//go:embed model-routing-v3.schema.json
var routingSchemaJSON []byte

// SchemaJSON returns a defensive copy of the canonical CLIProxy-owned schema.
func SchemaJSON() []byte {
	return append([]byte(nil), routingSchemaJSON...)
}

// SchemaDigest returns the identity of the exact embedded schema artifact.
func SchemaDigest() string {
	digest := sha256.Sum256(routingSchemaJSON)
	return "sha256:" + hex.EncodeToString(digest[:])
}
