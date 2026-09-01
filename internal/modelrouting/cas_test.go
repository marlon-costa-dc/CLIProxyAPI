package modelrouting

import "testing"

func TestActiveETagRoundTripPreservesCompleteIdentity(t *testing.T) {
	t.Parallel()
	identity := *transitionIdentity(7, 'c')
	etag, err := ActiveETag(identity)
	if err != nil {
		t.Fatalf("ActiveETag() error = %v", err)
	}
	parsed, err := ParseActiveETag(etag)
	if err != nil {
		t.Fatalf("ParseActiveETag() error = %v", err)
	}
	if parsed == nil || *parsed != identity {
		t.Fatalf("ParseActiveETag() = %+v, want %+v", parsed, identity)
	}
}

func TestParseActiveETagRejectsNonCanonicalOrPartialPreconditions(t *testing.T) {
	t.Parallel()
	tests := []string{
		"*",
		"W/\"aihub-v2.invalid\"",
		"\"aihub-v1.invalid\"",
		"\"aihub-v2.invalid\", \"aihub-v2.other\"",
		"\"aihub-v2.e30\"",
	}
	for _, value := range tests {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			if parsed, err := ParseActiveETag(value); err == nil {
				t.Fatalf("ParseActiveETag(%q) = %+v, want rejection", value, parsed)
			}
		})
	}
}
