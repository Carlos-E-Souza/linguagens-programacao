package httputil

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Limites do termo de busca de mensagens. O mínimo protege o servidor: a
// coluna body não tem índice que o LIKE '%termo%' consiga usar, então um
// termo de uma letra casaria quase todas as linhas. O máximo barra query
// strings absurdas.
const (
	minSearchTermRunes = 2
	maxSearchTermRunes = 100
)

// defaultSearchPage e maxSearchPage limitam uma página de resultados da busca.
const (
	defaultSearchPage = 20
	maxSearchPage     = 50
)

var (
	errSearchTermTooShort = errors.New("search term must have at least 2 characters")
	errSearchTermTooLong  = errors.New("search term must have at most 100 characters")
	errSearchTermInvalid  = errors.New("search term is not valid UTF-8")
)

// normalizeSearchTerm limpa um termo de busca bruto e valida o tamanho.
//
// É pura: mesma entrada, mesma saída, sem I/O. Espaços nas pontas são
// removidos e sequências de espaços internos viram um só, então
// "oi   mundo " e "oi mundo" buscam a mesma coisa. O tamanho é contado em
// runas, não em bytes, porque "ã" é um caractere para o usuário mas ocupa
// dois bytes. Os curingas do LIKE NÃO são tratados aqui; quem os escapa é
// db.escapeLike, na fronteira com a consulta.
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

// handleSearchMessages atende GET /api/conversations/{id}/messages/search?q=<termo>.
//
// A participação é verificada antes de qualquer outra coisa, exatamente
// como em handleListMessages, então quem não é membro recebe o mesmo 404 de
// uma conversa inexistente. Os resultados vêm do mais novo para o mais
// antigo; "before" é o cursor de sequência.
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
