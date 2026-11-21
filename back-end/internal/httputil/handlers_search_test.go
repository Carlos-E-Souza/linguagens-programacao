package httputil

import (
	"errors"
	"strings"
	"testing"
)

func testNormalizeSearchTerm(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		want      string
		wantError error
	}{
		{"plain", "reunião", "reunião", nil},
		{"trims edges", "  oi mundo  ", "oi mundo", nil},
		{"collapses inner whitespace", "oi \t\n  mundo", "oi mundo", nil},
		{"nbsp counts as whitespace", "oi  mundo", "oi mundo", nil},
		{"empty", "", "", errSearchTermTooShort},
		{"only spaces", "   \t ", "", errSearchTermTooShort},
		{"one rune is too short", "a", "", errSearchTermTooShort},
		{"one multibyte rune is too short", "ã", "", errSearchTermTooShort},
		{"two runes is the minimum", "ão", "ão", nil},
		{"limit counts runes, not bytes", strings.Repeat("ã", maxSearchTermRunes), strings.Repeat("ã", maxSearchTermRunes), nil},
		{"one rune over the limit", strings.Repeat("ã", maxSearchTermRunes+1), "", errSearchTermTooLong},
		{"invalid utf-8", "ab\xff\xfe", "", errSearchTermInvalid},
		{"wildcards are kept literal here", "100%_ok", "100%_ok", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeSearchTerm(tc.raw)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("normalizeSearchTerm(%q) error  = %v, want %v", tc.raw, err, tc.wantError)
			}
			if got != tc.want {
				t.Errorf("normalizeSearcTerm(%q) = %q, want %q", tc.raw, got, tc.want)
			}

		})
	}
}
