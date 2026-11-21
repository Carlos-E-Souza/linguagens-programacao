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
