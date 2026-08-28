package modelrouting

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

const activeETagPrefix = "aihub-v2."

// ActiveETag encodes the complete identity as one strong HTTP entity tag.
func ActiveETag(identity ActiveIdentityV2) (string, error) {
	if errValidate := identity.Validate(); errValidate != nil {
		return "", errValidate
	}
	canonical, errCanonical := canonicalJSON(identity)
	if errCanonical != nil {
		return "", fmt.Errorf("canonicalize active identity: %w", errCanonical)
	}
	token := base64.RawURLEncoding.EncodeToString(canonical)
	return `"` + activeETagPrefix + token + `"`, nil
}

// ParseActiveETag accepts exactly one strong v2 identity tag.
func ParseActiveETag(value string) (*ActiveIdentityV2, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' || strings.Contains(value, ",") {
		return nil, fmt.Errorf("If-Match must contain exactly one quoted strong aihub-v2 identity")
	}
	opaque := value[1 : len(value)-1]
	if !strings.HasPrefix(opaque, activeETagPrefix) {
		return nil, fmt.Errorf("If-Match uses an unsupported identity version")
	}
	raw, errDecode := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(opaque, activeETagPrefix))
	if errDecode != nil {
		return nil, fmt.Errorf("decode If-Match identity: %w", errDecode)
	}
	var identity ActiveIdentityV2
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if errJSON := decoder.Decode(&identity); errJSON != nil {
		return nil, fmt.Errorf("decode If-Match identity JSON: %w", errJSON)
	}
	if errValidate := identity.Validate(); errValidate != nil {
		return nil, errValidate
	}
	reencoded, errETag := ActiveETag(identity)
	if errETag != nil {
		return nil, errETag
	}
	if reencoded != value {
		return nil, fmt.Errorf("If-Match identity is not canonically encoded")
	}
	return &identity, nil
}

func (identity ActiveIdentityV2) Validate() error {
	if identity.Generation == 0 {
		return fmt.Errorf("active identity generation must be positive")
	}
	for name, digest := range map[string]string{
		"snapshot_digest": identity.SnapshotDigest,
		"projection_digest": identity.ProjectionDigest,
		"config_digest": identity.ConfigDigest,
	} {
		if !digestPattern.MatchString(digest) {
			return fmt.Errorf("active identity %s must be sha256:<64 lowercase hex>", name)
		}
	}
	return nil
}

func canonicalJSON(value any) ([]byte, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&decoded); errDecode != nil {
		return nil, errDecode
	}
	return json.Marshal(decoded)
}
