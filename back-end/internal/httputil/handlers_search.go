package httputil

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"
)

const (
	minSearchTermRunes = 2
	maxSearchTermRunes = 100
)

// defaultSearchPage and maxSearchPage bound a page of search results.
const (
	defaultSearchPage = 20
	maxSearchPage     = 50
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

// handleSearchMessages answers GET /api/conversations/{id}/messages/search?q=<term>.
//
// Membership is checked before anything else, exactly like
// handleListMessages, so a non-member gets the same 404 as for a missing
// conversation. Results are newest first; "before" is the sequence cursor.
func (s *Server) handleSearchMessages(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.currentUser(w, r)
	if !ok {
		return
	}

	conv, ok := s.requireMembership(w, r, userID)
	if !ok {
		return
	}

	term, err := normalizeSearchTerm(r.URL.Query().Get("q"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_term", err.Error())
		return
	}

	before, err := queryInt64(r, "before", 0)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_before", "before must be a non-negative integer")
		return
	}

	limit, err := queryInt(r, "limit", defaultSearchPage)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
		return
	}
	if limit > maxSearchPage {
		limit = maxSearchPage
	}
	if limit < 1 {
		limit = defaultSearchPage
	}

	messages, err := s.store.SearchMessages(r.Context(), conv.ID, term, before, limit)
	if err != nil {
		logRequestError(r, "search_messages", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not search messages")
		return
	}

	WriteJSON(w, http.StatusOK, messages)
}
