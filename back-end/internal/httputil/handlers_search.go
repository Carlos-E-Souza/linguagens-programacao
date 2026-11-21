package httputil

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	minSearchTermRunes = 2
	maxSearchTermRunes = 100
)

var (
	errSearchTermTooShort = errors.New("The search term must have at least 2 characters")
	errSearchTermTooLong  = errors.New("The search term must not exceed 100 characters")
	errSearchTermInvalid  = errors.New("The searcht term contains invalid symbols")
)

// normalizeSearchTerm cleans a raw search term and validates its length.
//
// It is pure: same input, same output, no I/O. Leading and trailing
// whitespace is dropped and inner runs of whitespace collapse to a single
// space, so "oi   mundo " and "oi mundo" search for the same thing. Length
// is counted in runes, not bytes, because "ã" is one character to the user
// but two bytes. LIKE wildcards are NOT handled here; db.escapeLike does
// that at the query boundary.
func normalizeSearchTerm(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", errSearchTermInvalid
	}

	term := strings.Join(strings.Fields(raw), " ")

	switch n := utf8.RuneCountInString(term); {
	case n < minSearchTermRunes:
		return "", errSearchTermTooShort

	case n > maxSearchTermRunes:
		return "", errSearchTermTooLong

	}

	return term, nil

}
