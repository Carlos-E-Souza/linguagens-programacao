// Aplica a busca de mensagens (servidor, cliente Go e interface) sobre a versão atual do repositório.
//
// Uso, na RAIZ do repositório (a pasta que contém README.md, back-end/ e front-end/):
//   node aplicar-busca.mjs            aplica
//   node aplicar-busca.mjs --dry      só mostra o que faria, sem escrever nada
//   node aplicar-busca.mjs --force    também sobrescreve arquivos novos que já existem com conteúdo diferente
//
// Seguro de repetir: o que já foi aplicado é pulado. Preserva o fim de linha de cada arquivo (LF ou CRLF).
// Se um trecho de referência não for encontrado (o arquivo mudou), o script avisa qual e mostra o texto para colar
// à mão; as demais edições continuam.

import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname } from 'node:path';

const spec = {
 "newfiles": {
  "back-end/internal/httputil/handlers_search.go": "package httputil\n\nimport (\n\t\"errors\"\n\t\"net/http\"\n\t\"strings\"\n\t\"unicode/utf8\"\n)\n\n// Limites do termo de busca de mensagens. O mínimo protege o servidor: a\n// coluna body não tem índice que o LIKE '%termo%' consiga usar, então um\n// termo de uma letra casaria quase todas as linhas. O máximo barra query\n// strings absurdas.\nconst (\n\tminSearchTermRunes = 2\n\tmaxSearchTermRunes = 100\n)\n\n// defaultSearchPage e maxSearchPage limitam uma página de resultados da busca.\nconst (\n\tdefaultSearchPage = 20\n\tmaxSearchPage     = 50\n)\n\nvar (\n\terrSearchTermTooShort = errors.New(\"search term must have at least 2 characters\")\n\terrSearchTermTooLong  = errors.New(\"search term must have at most 100 characters\")\n\terrSearchTermInvalid  = errors.New(\"search term is not valid UTF-8\")\n)\n\n// normalizeSearchTerm limpa um termo de busca bruto e valida o tamanho.\n//\n// É pura: mesma entrada, mesma saída, sem I/O. Espaços nas pontas são\n// removidos e sequências de espaços internos viram um só, então\n// \"oi   mundo \" e \"oi mundo\" buscam a mesma coisa. O tamanho é contado em\n// runas, não em bytes, porque \"ã\" é um caractere para o usuário mas ocupa\n// dois bytes. Os curingas do LIKE NÃO são tratados aqui; quem os escapa é\n// db.escapeLike, na fronteira com a consulta.\nfunc normalizeSearchTerm(raw string) (string, error) {\n\tif !utf8.ValidString(raw) {\n\t\treturn \"\", errSearchTermInvalid\n\t}\n\n\tterm := strings.Join(strings.Fields(raw), \" \")\n\n\tswitch n := utf8.RuneCountInString(term); {\n\tcase n < minSearchTermRunes:\n\t\treturn \"\", errSearchTermTooShort\n\tcase n > maxSearchTermRunes:\n\t\treturn \"\", errSearchTermTooLong\n\t}\n\n\treturn term, nil\n}\n\n// handleSearchMessages atende GET /api/conversations/{id}/messages/search?q=<termo>.\n//\n// A participação é verificada antes de qualquer outra coisa, exatamente\n// como em handleListMessages, então quem não é membro recebe o mesmo 404 de\n// uma conversa inexistente. Os resultados vêm do mais novo para o mais\n// antigo; \"before\" é o cursor de sequência.\nfunc (s *Server) handleSearchMessages(w http.ResponseWriter, r *http.Request) {\n\tuserID, ok := s.currentUser(w, r)\n\tif !ok {\n\t\treturn\n\t}\n\n\tconv, ok := s.requireMembership(w, r, userID)\n\tif !ok {\n\t\treturn\n\t}\n\n\tterm, err := normalizeSearchTerm(r.URL.Query().Get(\"q\"))\n\tif err != nil {\n\t\tWriteError(w, http.StatusBadRequest, \"invalid_term\", err.Error())\n\t\treturn\n\t}\n\n\tbefore, err := queryInt64(r, \"before\", 0)\n\tif err != nil {\n\t\tWriteError(w, http.StatusBadRequest, \"invalid_before\", \"before must be a non-negative integer\")\n\t\treturn\n\t}\n\n\tlimit, err := queryInt(r, \"limit\", defaultSearchPage)\n\tif err != nil {\n\t\tWriteError(w, http.StatusBadRequest, \"invalid_limit\", \"limit must be a positive integer\")\n\t\treturn\n\t}\n\tif limit > maxSearchPage {\n\t\tlimit = maxSearchPage\n\t}\n\tif limit < 1 {\n\t\tlimit = defaultSearchPage\n\t}\n\n\tmessages, err := s.store.SearchMessages(r.Context(), conv.ID, term, before, limit)\n\tif err != nil {\n\t\tlogRequestError(r, \"search_messages\", err)\n\t\tWriteError(w, http.StatusInternalServerError, \"internal_error\", \"could not search messages\")\n\t\treturn\n\t}\n\n\tWriteJSON(w, http.StatusOK, messages)\n}\n",
  "back-end/internal/httputil/handlers_search_test.go": "package httputil\n\nimport (\n\t\"context\"\n\t\"encoding/json\"\n\t\"errors\"\n\t\"net/http\"\n\t\"strings\"\n\t\"testing\"\n\t\"time\"\n\n\t\"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models\"\n)\n\nfunc TestNormalizeSearchTerm(t *testing.T) {\n\tcases := []struct {\n\t\tname    string\n\t\traw     string\n\t\twant    string\n\t\twantErr error\n\t}{\n\t\t{\"plain\", \"reunião\", \"reunião\", nil},\n\t\t{\"trims edges\", \"  oi mundo  \", \"oi mundo\", nil},\n\t\t{\"collapses inner whitespace\", \"oi \\t\\n  mundo\", \"oi mundo\", nil},\n\t\t{\"nbsp counts as whitespace\", \"oi  mundo\", \"oi mundo\", nil},\n\t\t{\"empty\", \"\", \"\", errSearchTermTooShort},\n\t\t{\"only spaces\", \"   \\t \", \"\", errSearchTermTooShort},\n\t\t{\"one rune is too short\", \"a\", \"\", errSearchTermTooShort},\n\t\t{\"one multibyte rune is too short\", \"ã\", \"\", errSearchTermTooShort},\n\t\t{\"two runes is the minimum\", \"ão\", \"ão\", nil},\n\t\t{\"limit counts runes, not bytes\", strings.Repeat(\"ã\", maxSearchTermRunes), strings.Repeat(\"ã\", maxSearchTermRunes), nil},\n\t\t{\"one rune over the limit\", strings.Repeat(\"ã\", maxSearchTermRunes+1), \"\", errSearchTermTooLong},\n\t\t{\"invalid utf-8\", \"ab\\xff\\xfe\", \"\", errSearchTermInvalid},\n\t\t{\"wildcards are kept literal here\", \"100%_ok\", \"100%_ok\", nil},\n\t}\n\tfor _, tc := range cases {\n\t\tt.Run(tc.name, func(t *testing.T) {\n\t\t\tgot, err := normalizeSearchTerm(tc.raw)\n\t\t\tif !errors.Is(err, tc.wantErr) {\n\t\t\t\tt.Fatalf(\"normalizeSearchTerm(%q) error = %v, want %v\", tc.raw, err, tc.wantErr)\n\t\t\t}\n\t\t\tif got != tc.want {\n\t\t\t\tt.Errorf(\"normalizeSearchTerm(%q) = %q, want %q\", tc.raw, got, tc.want)\n\t\t\t}\n\t\t})\n\t}\n}\n\nfunc TestSearchMessagesEndpoint(t *testing.T) {\n\tf := newFixture(t)\n\talice := f.register(\"alice\")\n\tbob := f.register(\"bob\")\n\tf.register(\"eve\")\n\taliceToken := f.login(\"alice\")\n\teveToken := f.login(\"eve\")\n\n\trec := f.do(http.MethodPost, \"/api/conversations\", aliceToken, createConversationRequest{\n\t\tType: models.ConversationDirect, Members: []int64{bob.ID},\n\t})\n\tvar conv models.Conversation\n\tif err := json.Unmarshal(rec.Body.Bytes(), &conv); err != nil {\n\t\tt.Fatalf(\"decode conversation: %v\", err)\n\t}\n\n\tnow := time.Now().UTC()\n\tif _, err := f.store.InsertMessages(context.Background(), []models.Message{\n\t\t{ConversationID: conv.ID, Sequence: 1, SenderID: alice.ID, ClientID: \"c1\", Body: \"Bom dia\", CreatedAt: now},\n\t\t{ConversationID: conv.ID, Sequence: 2, SenderID: bob.ID, ClientID: \"c2\", Body: \"Reunião às 15h\", CreatedAt: now},\n\t\t{ConversationID: conv.ID, Sequence: 3, SenderID: alice.ID, ClientID: \"c3\", Body: \"reunião adiada\", CreatedAt: now},\n\t}); err != nil {\n\t\tt.Fatalf(\"InsertMessages() failed: %v\", err)\n\t}\n\n\tpath := \"/api/conversations/\" + itoa(conv.ID) + \"/messages/search\"\n\n\trec = f.do(http.MethodGet, path+\"?q=reuni%C3%A3o\", aliceToken, nil)\n\tif got, want := rec.Code, http.StatusOK; got != want {\n\t\tt.Fatalf(\"status: got %d, want %d (%s)\", got, want, rec.Body)\n\t}\n\tvar found []models.Message\n\tif err := json.Unmarshal(rec.Body.Bytes(), &found); err != nil {\n\t\tt.Fatalf(\"decode results: %v\", err)\n\t}\n\tif len(found) != 2 || found[0].Sequence != 3 || found[1].Sequence != 2 {\n\t\tt.Errorf(\"results = %+v, want sequences [3 2]\", found)\n\t}\n\n\t// Resultado vazio é \"[]\", nunca \"null\": o cliente itera sobre ele.\n\trec = f.do(http.MethodGet, path+\"?q=zzzz\", aliceToken, nil)\n\tif got := rec.Body.String(); got != \"[]\\n\" && got != \"[]\" {\n\t\tt.Errorf(\"empty result body = %q, want []\", got)\n\t}\n\n\t// Quem não é membro não pode descobrir que a conversa existe.\n\trec = f.do(http.MethodGet, path+\"?q=reuni%C3%A3o\", eveToken, nil)\n\tif got, want := rec.Code, http.StatusNotFound; got != want {\n\t\tt.Errorf(\"non-member status: got %d, want %d\", got, want)\n\t}\n\n\t// Entrada inválida é erro do cliente.\n\tfor _, query := range []string{\"\", \"?q=a\", \"?q=%20%20\", \"?q=oi&before=abc\", \"?q=oi&limit=-1\"} {\n\t\trec = f.do(http.MethodGet, path+query, aliceToken, nil)\n\t\tif got, want := rec.Code, http.StatusBadRequest; got != want {\n\t\t\tt.Errorf(\"query %q status: got %d, want %d (%s)\", query, got, want, rec.Body)\n\t\t}\n\t}\n\n\t// Um limit acima do máximo é reduzido, não obedecido.\n\trec = f.do(http.MethodGet, path+\"?q=oi&limit=99999\", aliceToken, nil)\n\tif got, want := rec.Code, http.StatusOK; got != want {\n\t\tt.Errorf(\"clamped limit status: got %d, want %d (%s)\", got, want, rec.Body)\n\t}\n}\n",
  "back-end/internal/db/search_test.go": "package db\n\nimport (\n\t\"context\"\n\t\"fmt\"\n\t\"slices\"\n\t\"testing\"\n\t\"time\"\n\n\t\"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models\"\n)\n\nfunc TestSearchMessages(t *testing.T) {\n\tctx := context.Background()\n\ts := newTestStore(t)\n\n\tana := mustCreateUser(t, s, \"ana\")\n\tbob := mustCreateUser(t, s, \"bob\")\n\teve := mustCreateUser(t, s, \"eve\")\n\tconv := mustCreateDirect(t, s, ana, bob)\n\tother := mustCreateDirect(t, s, ana, eve)\n\n\tnow := time.Now().UTC()\n\tbodies := []string{\"Bom dia\", \"Reunião às 15h\", \"preço: 100% garantido\", \"arquivo_final.pdf\", \"reunião adiada\"}\n\tvar batch []models.Message\n\tfor i, body := range bodies {\n\t\tbatch = append(batch, models.Message{\n\t\t\tConversationID: conv.ID, Sequence: int64(i + 1), SenderID: ana.ID,\n\t\t\tClientID: fmt.Sprintf(\"c%d\", i+1), Body: body, CreatedAt: now,\n\t\t})\n\t}\n\tbatch = append(batch, models.Message{\n\t\tConversationID: other.ID, Sequence: 1, SenderID: ana.ID,\n\t\tClientID: \"other-1\", Body: \"reunião secreta\", CreatedAt: now,\n\t})\n\tif _, err := s.InsertMessages(ctx, batch); err != nil {\n\t\tt.Fatalf(\"InsertMessages() failed: %v\", err)\n\t}\n\n\tcases := []struct {\n\t\tname   string\n\t\tterm   string\n\t\tbefore int64\n\t\tlimit  int\n\t\twant   []int64\n\t}{\n\t\t{\"substring, newest first\", \"reunião\", 0, 20, []int64{5, 2}},\n\t\t{\"ASCII is case-insensitive\", \"BOM\", 0, 20, []int64{1}},\n\t\t{\"percent is literal\", \"100%\", 0, 20, []int64{3}},\n\t\t{\"underscore is literal, not a wildcard\", \"arq_ivo\", 0, 20, nil},\n\t\t{\"never reads another conversation\", \"secreta\", 0, 20, nil},\n\t\t{\"before is an exclusive cursor\", \"reunião\", 5, 20, []int64{2}},\n\t\t{\"limit\", \"reunião\", 0, 1, []int64{5}},\n\t\t{\"no match\", \"zzzz\", 0, 20, nil},\n\t}\n\tfor _, tc := range cases {\n\t\tt.Run(tc.name, func(t *testing.T) {\n\t\t\tgot, err := s.SearchMessages(ctx, conv.ID, tc.term, tc.before, tc.limit)\n\t\t\tif err != nil {\n\t\t\t\tt.Fatalf(\"SearchMessages() failed: %v\", err)\n\t\t\t}\n\t\t\tvar sequences []int64\n\t\t\tfor _, m := range got {\n\t\t\t\tsequences = append(sequences, m.Sequence)\n\t\t\t}\n\t\t\tif !slices.Equal(sequences, tc.want) {\n\t\t\t\tt.Errorf(\"sequences = %v, want %v\", sequences, tc.want)\n\t\t\t}\n\t\t})\n\t}\n}\n",
  "front-end/internal/api/client_search_test.go": "package api\n\nimport (\n\t\"context\"\n\t\"net/http\"\n\t\"testing\"\n)\n\nfunc TestSearchMessagesSendsTermAndCursor(t *testing.T) {\n\tclient := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {\n\t\tif r.Method != http.MethodGet || r.URL.Path != \"/api/conversations/7/messages/search\" {\n\t\t\tt.Errorf(\"requisição inesperada: %s %s\", r.Method, r.URL.Path)\n\t\t}\n\t\tquery := r.URL.Query()\n\t\tif got := query.Get(\"q\"); got != \"reunião às 15h\" {\n\t\t\tt.Errorf(\"q = %q, esperava o termo decodificado\", got)\n\t\t}\n\t\tif got := query.Get(\"before\"); got != \"40\" {\n\t\t\tt.Errorf(\"before = %q, esperava 40\", got)\n\t\t}\n\t\tif got := query.Get(\"limit\"); got != \"20\" {\n\t\t\tt.Errorf(\"limit = %q, esperava 20\", got)\n\t\t}\n\t\tw.Write([]byte(`[{\"id\":1,\"conversation_id\":7,\"sequence\":39,\"sender_id\":2,\"client_id\":\"c\",\"body\":\"reunião às 15h\"}]`))\n\t})\n\tclient.SetToken(\"tok\")\n\n\tmessages, err := client.SearchMessages(context.Background(), 7, \"reunião às 15h\", 40, 20)\n\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n\tif len(messages) != 1 || messages[0].Sequence != 39 {\n\t\tt.Errorf(\"mensagens inesperadas: %+v\", messages)\n\t}\n}\n\nfunc TestSearchMessagesOmitsCursorAndLimitWhenZero(t *testing.T) {\n\tclient := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {\n\t\tquery := r.URL.Query()\n\t\tif query.Has(\"before\") || query.Has(\"limit\") {\n\t\t\tt.Errorf(\"não deveria enviar before nem limit: %q\", r.URL.RawQuery)\n\t\t}\n\t\tw.Write([]byte(`[]`))\n\t})\n\n\tmessages, err := client.SearchMessages(context.Background(), 7, \"oi\", 0, 0)\n\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n\tif len(messages) != 0 {\n\t\tt.Errorf(\"esperava lista vazia, veio %+v\", messages)\n\t}\n}\n",
  "front-end/frontend/src/utils/search.js": "// Utilitários da busca de mensagens.\n\n/** Escapa os caracteres que têm significado especial em uma expressão regular. */\nfunction escapeRegExp(text) {\n  return text.replace(/[.*+?^${}()|[\\]\\\\]/g, '\\\\$&');\n}\n\n/**\n * Divide text em trechos para destacar as ocorrências de term (sem diferenciar maiúsculas, como a busca do\n * servidor). Devolve [{ text, match }]; juntar os `text` na ordem reproduz o original. Sem termo, um trecho só.\n * Quem renderiza deve usar textContent (nunca innerHTML): o texto da mensagem não é confiável.\n */\nexport function highlightParts(text, term) {\n  const source = String(text ?? '');\n  if (source === '') return [];\n  const needle = String(term ?? '').trim().split(/\\s+/).filter(Boolean).join(' ');\n  if (needle === '') return [{ text: source, match: false }];\n\n  const parts = [];\n  let last = 0;\n  for (const found of source.matchAll(new RegExp(escapeRegExp(needle), 'gi'))) {\n    if (found.index > last) parts.push({ text: source.slice(last, found.index), match: false });\n    parts.push({ text: found[0], match: true });\n    last = found.index + found[0].length;\n  }\n  if (last < source.length) parts.push({ text: source.slice(last), match: false });\n  return parts;\n}\n",
  "front-end/frontend/src/utils/search.test.js": "import test from 'node:test';\nimport assert from 'node:assert/strict';\nimport { highlightParts } from './search.js';\n\nconst join = (parts) => parts.map((p) => p.text).join('');\n\ntest('sem termo devolve o texto inteiro, sem destaque', () => {\n  assert.deepEqual(highlightParts('Bom dia', ''), [{ text: 'Bom dia', match: false }]);\n  assert.deepEqual(highlightParts('Bom dia', '   '), [{ text: 'Bom dia', match: false }]);\n});\n\ntest('texto vazio devolve lista vazia', () => {\n  assert.deepEqual(highlightParts('', 'oi'), []);\n});\n\ntest('marca a ocorrência e preserva o resto', () => {\n  assert.deepEqual(highlightParts('Bom dia, tudo bem?', 'dia'), [\n    { text: 'Bom ', match: false },\n    { text: 'dia', match: true },\n    { text: ', tudo bem?', match: false },\n  ]);\n});\n\ntest('ignora maiúsculas e preserva a grafia original', () => {\n  assert.deepEqual(highlightParts('REUNIÃO adiada', 'reunião'), [\n    { text: 'REUNIÃO', match: true },\n    { text: ' adiada', match: false },\n  ]);\n});\n\ntest('marca todas as ocorrências, inclusive coladas', () => {\n  const parts = highlightParts('ababab', 'ab');\n  assert.deepEqual(parts.filter((p) => p.match).length, 3);\n  assert.equal(join(parts), 'ababab');\n});\n\ntest('caracteres especiais de regex são literais', () => {\n  assert.deepEqual(highlightParts('custa 3.5 (ou 3x5)', '3.5'), [\n    { text: 'custa ', match: false },\n    { text: '3.5', match: true },\n    { text: ' (ou 3x5)', match: false },\n  ]);\n  assert.deepEqual(highlightParts('a+b=c', 'a+b').filter((p) => p.match).map((p) => p.text), ['a+b']);\n  assert.doesNotThrow(() => highlightParts('qualquer', '(['));\n  assert.doesNotThrow(() => highlightParts('qualquer', '\\\\'));\n});\n\ntest('espaços do termo são normalizados como no servidor', () => {\n  assert.deepEqual(highlightParts('oi mundo', '  oi   mundo '), [{ text: 'oi mundo', match: true }]);\n});\n\ntest('concatenar as partes sempre reproduz o texto original', () => {\n  for (const [text, term] of [['İstanbul i̇', 'i'], ['a\\nb', 'b'], ['😀 oi 😀', '😀'], ['xx', 'x']]) {\n    assert.equal(join(highlightParts(text, term)), text);\n  }\n});\n",
  "front-end/frontend/src/components/message-search.js": "import { h, reconcileChildren, replaceChildren, setText, toggleClass } from '../utils/dom.js';\nimport { formatListTime } from '../utils/dates.js';\nimport { icon } from '../utils/icons.js';\nimport { highlightParts } from '../utils/search.js';\n\n// Espera depois da última tecla antes de buscar no servidor.\nconst DEBOUNCE_MS = 300;\n// Os mesmos limites do servidor (normalizeSearchTerm): abaixo de 2 caracteres nem vale a requisição.\nconst MIN_CHARS = 2;\nconst MAX_CHARS = 100;\n// Tamanho da página do servidor (searchPageSize em app.go). Uma página cheia significa que pode haver mais.\nconst PAGE_SIZE = 20;\n\nlet nextId = 0;\n\n/**\n * Painel de busca dentro de uma conversa: campo, lista de resultados (do mais novo ao mais antigo) com o trecho\n * encontrado destacado, e \"Carregar mais\".\n * props: { search(term, before) → Promise<Message[]>, senderName(senderId) → string, onClose() }\n */\nexport function createMessageSearch(props) {\n  const id = `message-search-${++nextId}`;\n  const input = h('input', {\n    class: 'field__input message-search__input',\n    id,\n    type: 'search',\n    autocomplete: 'off',\n    spellcheck: 'false',\n    placeholder: 'Buscar nesta conversa',\n    'aria-label': 'Buscar mensagens nesta conversa',\n    'aria-controls': `${id}-results`,\n  });\n  const closeButton = h(\n    'button',\n    {\n      class: 'button button--icon',\n      type: 'button',\n      'aria-label': 'Fechar busca',\n      title: 'Fechar busca',\n      on: { click: () => props.onClose?.() },\n    },\n    icon('close'),\n  );\n  const status = h('p', { class: 'message-search__status', role: 'status' });\n  const list = h('ul', { class: 'message-search__results', id: `${id}-results`, 'aria-label': 'Resultados da busca' });\n  const moreButton = h(\n    'button',\n    { class: 'button button--link message-search__more', type: 'button', on: { click: () => loadMore() } },\n    'Carregar mais',\n  );\n  const el = h(\n    'div',\n    { class: 'message-search' },\n    h('div', { class: 'message-search__bar' }, icon('search'), input, closeButton),\n    status,\n    list,\n    moreButton,\n  );\n\n  const items = new Map();\n  let term = '';\n  let results = [];\n  let hasMore = false;\n  let loading = false;\n  let error = '';\n  let timer = null;\n  // Cada busca nova ganha um número; respostas de buscas antigas (digitou de novo) são descartadas.\n  let request = 0;\n\n  input.addEventListener('input', () => {\n    clearTimeout(timer);\n    request++;\n    term = input.value.trim().split(/\\s+/).filter(Boolean).join(' ');\n    results = [];\n    hasMore = false;\n    error = '';\n    if ([...term].length < MIN_CHARS) {\n      loading = false;\n      render();\n      return;\n    }\n    loading = true;\n    render();\n    timer = setTimeout(run, DEBOUNCE_MS);\n  });\n  input.addEventListener('keydown', (event) => {\n    if (event.key === 'Escape') {\n      event.preventDefault();\n      props.onClose?.();\n    }\n  });\n\n  async function run() {\n    const current = request;\n    try {\n      const found = await props.search(term, 0);\n      if (current !== request) return;\n      results = found;\n      hasMore = found.length >= PAGE_SIZE;\n      error = '';\n    } catch (err) {\n      if (current !== request) return;\n      results = [];\n      hasMore = false;\n      error = err?.message || 'Não foi possível buscar agora.';\n    }\n    loading = false;\n    render();\n  }\n\n  async function loadMore() {\n    if (loading || !hasMore || results.length === 0) return;\n    const current = request;\n    // Os resultados vêm do mais novo para o mais antigo: a última sequência é a menor, e é o cursor da próxima página.\n    const before = results[results.length - 1].sequence;\n    loading = true;\n    error = '';\n    render();\n    try {\n      const found = await props.search(term, before);\n      if (current !== request) return;\n      results = results.concat(found);\n      hasMore = found.length >= PAGE_SIZE;\n    } catch (err) {\n      if (current !== request) return;\n      error = err?.message || 'Não foi possível buscar agora.';\n    }\n    loading = false;\n    render();\n  }\n\n  function createItem(message) {\n    const sender = h('span', { class: 'message-search__sender' });\n    const time = h('span', { class: 'message-search__time' });\n    const body = h('p', { class: 'message-search__body' });\n    const li = h('li', { class: 'message-search__item' }, h('div', { class: 'message-search__meta' }, sender, time), body);\n    return { li, sender, time, body, message, shownTerm: null };\n  }\n\n  function render() {\n    const seen = new Set();\n    const nodes = results.map((message) => {\n      seen.add(message.id);\n      let item = items.get(message.id);\n      if (!item) {\n        item = createItem(message);\n        items.set(message.id, item);\n      }\n      setText(item.sender, props.senderName?.(message.sender_id) ?? '');\n      setText(item.time, formatListTime(message.created_at));\n      if (item.shownTerm !== term) {\n        // Sempre nós de texto e <mark>: o corpo da mensagem não é confiável e nunca entra como HTML.\n        replaceChildren(\n          item.body,\n          highlightParts(message.body, term).map((part) => (part.match ? h('mark', {}, part.text) : part.text)),\n        );\n        item.shownTerm = term;\n      }\n      return item.li;\n    });\n    for (const key of items.keys()) if (!seen.has(key)) items.delete(key);\n    reconcileChildren(list, nodes);\n\n    const searching = [...term].length >= MIN_CHARS;\n    if (loading) replaceChildren(status, h('span', { class: 'spinner', 'aria-hidden': 'true' }), 'Buscando…');\n    else if (error) setText(status, error);\n    else if (term === '') setText(status, 'Digite para buscar nesta conversa.');\n    else if (!searching) setText(status, `Digite pelo menos ${MIN_CHARS} caracteres.`);\n    else if (results.length === 0) setText(status, 'Nenhuma mensagem encontrada.');\n    else setText(status, `${results.length}${hasMore ? '+' : ''} ${results.length === 1 ? 'resultado' : 'resultados'}`);\n    toggleClass(status, 'message-search__status--error', Boolean(error) && !loading);\n\n    moreButton.hidden = !hasMore || results.length === 0;\n    moreButton.disabled = loading;\n  }\n\n  input.maxLength = MAX_CHARS;\n  render();\n  return {\n    el,\n    focus: () => input.focus(),\n    destroy() {\n      clearTimeout(timer);\n      request++;\n    },\n  };\n}\n"
 },
 "edits": [
  {
   "file": "back-end/internal/db/messages.go",
   "name": "SearchMessages (consulta)",
   "done": "func (s *Store) SearchMessages",
   "before": [
    "// MarkDelivered avança a marca de entrega do membro.",
    "// MarkDelivered advances the member's delivered watermark."
   ],
   "add": "// SearchMessages devolve até limit mensagens de uma conversa cujo texto\n// contém term, da mais nova para a mais antiga, opcionalmente anteriores a\n// beforeSequence.\n//\n// Ao contrário de ListMessages, não inverte a página: uma lista de\n// resultados de busca é mais legível com o acerto mais recente em cima, e a\n// página seguinte é pedida com before = a menor sequência recebida. O termo\n// entra como parâmetro e seus curingas do LIKE são escapados, então é\n// sempre texto literal. O LIKE do SQLite ignora maiúsculas só em ASCII.\nfunc (s *Store) SearchMessages(ctx context.Context, conversationID int64, term string, beforeSequence int64, limit int) ([]models.Message, error) {\n\tpattern := \"%\" + escapeLike(term) + \"%\"\n\n\tquery := `\n\t\tSELECT ` + messageColumns + `\n\t\tFROM messages\n\t\tWHERE conversation_id = ?\n\t\t  AND body LIKE ? ESCAPE '\\'\n\t\t  AND (? = 0 OR sequence < ?)\n\t\tORDER BY sequence DESC\n\t\tLIMIT ?`\n\n\trows, err := s.db.QueryContext(ctx, query, conversationID, pattern, beforeSequence, beforeSequence, limit)\n\tif err != nil {\n\t\treturn nil, fmt.Errorf(\"search messages: %w\", err)\n\t}\n\tdefer rows.Close()\n\n\treturn scanMessages(rows)\n}\n\n"
  },
  {
   "file": "back-end/internal/httputil/server.go",
   "name": "rota da busca",
   "done": "s.handleSearchMessages",
   "after": [
    "\tprotected.HandleFunc(\"GET /api/conversations/{id}/messages\", s.handleListMessages)\n"
   ],
   "add": "\tprotected.HandleFunc(\"GET /api/conversations/{id}/messages/search\", s.handleSearchMessages)\n"
  },
  {
   "file": "back-end/internal/httputil/api_test.go",
   "name": "rota na tabela de rotas protegidas",
   "done": "\"/api/conversations/1/messages/search\"",
   "after": [
    "\t\t{http.MethodGet, \"/api/conversations/1/messages\"},\n"
   ],
   "add": "\t\t{http.MethodGet, \"/api/conversations/1/messages/search\"},\n"
  },
  {
   "file": "front-end/internal/api/client.go",
   "name": "SearchMessages do cliente HTTP",
   "done": "func (c *Client) SearchMessages",
   "before": [
    "// request serializa body em JSON"
   ],
   "add": "// SearchMessages busca, em uma conversa, as mensagens cujo texto contém term, da mais nova para a mais antiga\n// (GET /api/conversations/{id}/messages/search). before = 0 pede as mais recentes.\nfunc (c *Client) SearchMessages(ctx context.Context, conversationID int64, term string, before int64, limit int) ([]Message, error) {\n\tquery := url.Values{}\n\tquery.Set(\"q\", term)\n\tif before > 0 {\n\t\tquery.Set(\"before\", strconv.FormatInt(before, 10))\n\t}\n\tif limit > 0 {\n\t\tquery.Set(\"limit\", strconv.Itoa(limit))\n\t}\n\tpath := fmt.Sprintf(\"/api/conversations/%d/messages/search?%s\", conversationID, query.Encode())\n\tvar messages []Message\n\terr := c.request(ctx, http.MethodGet, path, nil, &messages)\n\treturn messages, err\n}\n\n"
  },
  {
   "file": "front-end/conversations.go",
   "name": "App.SearchMessages",
   "done": "func (a *App) SearchMessages",
   "before": [
    "// CreateDirectConversation abre a conversa individual"
   ],
   "add": "// searchPageSize é quantos resultados uma busca traz por vez (o servidor limita em 50).\nconst searchPageSize = 20\n\n// SearchMessages busca mensagens de uma conversa no servidor, da mais nova para a mais antiga. before = 0 pede os\n// resultados mais recentes; para a página seguinte, passe a menor sequência já recebida. Só funciona com conexão:\n// o cache local guarda apenas parte do histórico.\nfunc (a *App) SearchMessages(conversationID int64, term string, before int64) ([]api.Message, error) {\n\ts := a.current()\n\tif s == nil {\n\t\treturn nil, errNoSession\n\t}\n\tmessages, err := a.api.SearchMessages(s.ctx, conversationID, term, before, searchPageSize)\n\tif err != nil {\n\t\treturn nil, friendly(err, map[int]string{\n\t\t\thttp.StatusBadRequest: \"Digite de 2 a 100 caracteres para buscar.\",\n\t\t\thttp.StatusNotFound:   \"Esta conversa não existe mais.\",\n\t\t})\n\t}\n\treturn nonNil(messages), nil\n}\n\n"
  },
  {
   "file": "front-end/frontend/wailsjs/go/main/App.js",
   "name": "binding SearchMessages (App.js)",
   "done": "export function SearchMessages",
   "before": [
    "export function SearchUsers(arg1) {"
   ],
   "add": "export function SearchMessages(arg1, arg2, arg3) {\n  return window['go']['main']['App']['SearchMessages'](arg1, arg2, arg3);\n}\n\n"
  },
  {
   "file": "front-end/frontend/wailsjs/go/main/App.d.ts",
   "name": "binding SearchMessages (App.d.ts)",
   "done": "export function SearchMessages",
   "before": [
    "export function SearchUsers(arg1:string)"
   ],
   "add": "export function SearchMessages(arg1:number,arg2:string,arg3:number):Promise<Array<api.Message>>;\n\n"
  },
  {
   "file": "front-end/frontend/src/bridge.js",
   "name": "bridge.searchMessages",
   "done": "searchMessages: call(",
   "after": [
    "  retryMessage: call('RetryMessage'),\n"
   ],
   "add": "  searchMessages: call('SearchMessages'),\n"
  },
  {
   "file": "front-end/frontend/src/mock-bridge.js",
   "name": "mock SearchMessages",
   "done": "async SearchMessages(",
   "before": [
    "    async MarkAsRead(conversationId, upToSequence) {"
   ],
   "add": "    async SearchMessages(conversationId, term, before) {\n      await delay(250);\n      const needle = String(term ?? '').trim().toLowerCase();\n      if (needle.length < 2) throw 'Digite de 2 a 100 caracteres para buscar.';\n      const upper = before || Infinity;\n      return structuredClone(\n        (histories[conversationId] ?? [])\n          .filter((m) => m.sequence < upper && m.body.toLowerCase().includes(needle))\n          .reverse()\n          .slice(0, 20),\n      );\n    },\n"
  },
  {
   "file": "front-end/frontend/src/actions.js",
   "name": "ação searchMessages",
   "done": "export async function searchMessages",
   "before": [
    "/** Abre a conversa individual com a pessoa, criando-a se preciso. */"
   ],
   "add": "/** Busca mensagens de uma conversa no servidor, da mais nova para a mais antiga. before = 0 pede as mais recentes. */\nexport async function searchMessages(conversationId, term, before = 0) {\n  try {\n    return await bridge.searchMessages(conversationId, term, before);\n  } catch (error) {\n    throw new Error(errorMessage(error));\n  }\n}\n\n"
  },
  {
   "file": "front-end/frontend/src/components/chat-window.js",
   "name": "import do painel de busca",
   "done": "import { createMessageSearch }",
   "after": [
    "import { createMessageInput } from './message-input.js';\n"
   ],
   "add": "import { createMessageSearch } from './message-search.js';\n"
  },
  {
   "file": "front-end/frontend/src/components/chat-window.js",
   "name": "documentação da prop onSearch",
   "done": "onShowDetails(), onSearch(term, before)",
   "replace": [
    " *          onSend(body), onRetryMessage(clientId), onShowDetails() }"
   ],
   "with_": " *          onSend(body), onRetryMessage(clientId), onShowDetails(), onSearch(term, before) }"
  },
  {
   "file": "front-end/frontend/src/components/chat-window.js",
   "name": "botão de busca e painel",
   "done": "const searchButton = h(",
   "before": [
    "  const header = h(\n"
   ],
   "add": "  const searchButton = h(\n    'button',\n    {\n      class: 'button button--icon chat-window__search',\n      type: 'button',\n      'aria-label': 'Buscar mensagens',\n      'aria-expanded': 'false',\n      title: 'Buscar nesta conversa',\n      on: { click: () => (search ? closeSearch() : openSearch()) },\n    },\n    icon('search'),\n  );\n  const searchPanel = h('div', { class: 'chat-window__search-panel', hidden: true });\n  let search = null;\n\n"
  },
  {
   "file": "front-end/frontend/src/components/chat-window.js",
   "name": "lupa no cabeçalho",
   "done": "    searchButton,\n    detailsButton,",
   "replace": [
    "    h('div', { class: 'chat-window__heading' }, title, subtitle),\n    detailsButton,\n"
   ],
   "with_": "    h('div', { class: 'chat-window__heading' }, title, subtitle),\n    searchButton,\n    detailsButton,\n"
  },
  {
   "file": "front-end/frontend/src/components/chat-window.js",
   "name": "painel entre o cabeçalho e o histórico",
   "done": "header, searchPanel, scroller",
   "replace": [
    "{ class: 'chat-window' }, header, scroller, input.el);"
   ],
   "with_": "{ class: 'chat-window' }, header, searchPanel, scroller, input.el);"
  },
  {
   "file": "front-end/frontend/src/components/chat-window.js",
   "name": "funções openSearch e closeSearch",
   "done": "function openSearch()",
   "before": [
    "  function renderHeader() {\n"
   ],
   "add": "  function openSearch() {\n    search = createMessageSearch({\n      // props é lido na hora da chamada: sempre vale a conversa e o usuário atuais.\n      search: (term, before) => props.onSearch(term, before),\n      senderName: (senderId) => senderName(props.conversation, senderId, props.me),\n      onClose: closeSearch,\n    });\n    searchPanel.replaceChildren(search.el);\n    searchPanel.hidden = false;\n    searchButton.setAttribute('aria-expanded', 'true');\n    search.focus();\n  }\n\n  function closeSearch() {\n    if (!search) return;\n    search.destroy();\n    search = null;\n    searchPanel.replaceChildren();\n    searchPanel.hidden = true;\n    searchButton.setAttribute('aria-expanded', 'false');\n    searchButton.focus();\n  }\n\n"
  },
  {
   "file": "front-end/frontend/src/components/chat-window.js",
   "name": "fechar a busca ao trocar de conversa",
   "done": "      closeSearch();\n      bubbles = new Map();",
   "after": [
    "      conversationId = props.conversation.id;\n"
   ],
   "add": "      closeSearch();\n"
  },
  {
   "file": "front-end/frontend/src/components/chat-window.js",
   "name": "fechar a busca ao destruir a janela",
   "done": "      closeSearch();\n      input.destroy();",
   "replace": [
    "  return { el, update, destroy: () => input.destroy() };\n"
   ],
   "with_": "  return {\n    el,\n    update,\n    destroy: () => {\n      closeSearch();\n      input.destroy();\n    },\n  };\n"
  },
  {
   "file": "front-end/frontend/src/screens/chat.js",
   "name": "import de searchMessages",
   "done": "  searchMessages,\n",
   "after": [
    "  retryMessage,\n"
   ],
   "add": "  searchMessages,\n"
  },
  {
   "file": "front-end/frontend/src/screens/chat.js",
   "name": "prop onSearch da janela",
   "done": "onSearch: (term, before)",
   "after": [
    "      onShowDetails: () => showModal('details'),\n"
   ],
   "add": "      onSearch: (term, before) => searchMessages(id, term, before),\n"
  },
  {
   "file": "front-end/frontend/src/styles/chat.css",
   "name": "estilos da busca (no fim do arquivo)",
   "done": "/* Busca dentro da conversa */",
   "append": "\n/* Busca dentro da conversa */\n.chat-window__search {\n  margin-left: auto;\n}\n\n.chat-window__details {\n  margin-left: 0;\n}\n\n.chat-window__search-panel {\n  display: flex;\n  flex: none;\n  flex-direction: column;\n  min-height: 0;\n  max-height: 45%;\n  border-bottom: 1px solid var(--color-line);\n  background: var(--color-surface);\n}\n\n.chat-window__search-panel[hidden] {\n  display: none;\n}\n\n.message-search {\n  display: flex;\n  flex-direction: column;\n  min-height: 0;\n  padding: var(--space-3) var(--space-5);\n  gap: var(--space-2);\n}\n\n.message-search__bar {\n  display: flex;\n  align-items: center;\n  gap: var(--space-2);\n}\n\n.message-search__input {\n  flex: 1;\n}\n\n.message-search__status {\n  display: flex;\n  align-items: center;\n  gap: var(--space-2);\n  font-size: var(--text-sm);\n  color: var(--color-ink-muted);\n}\n\n.message-search__status--error {\n  color: var(--color-danger);\n}\n\n.message-search__results {\n  min-height: 0;\n  margin: 0;\n  padding: 0;\n  overflow-y: auto;\n  list-style: none;\n  overscroll-behavior: contain;\n}\n\n.message-search__results:empty {\n  display: none;\n}\n\n.message-search__item {\n  padding: var(--space-2) 0;\n  border-top: 1px solid var(--color-line);\n}\n\n.message-search__meta {\n  display: flex;\n  gap: var(--space-2);\n  font-size: var(--text-xs);\n  color: var(--color-ink-subtle);\n}\n\n.message-search__sender {\n  font-weight: var(--weight-medium);\n  color: var(--color-ink-muted);\n}\n\n.message-search__body {\n  margin: 2px 0 0;\n  font-size: var(--text-md);\n  line-height: var(--leading-normal);\n  overflow-wrap: anywhere;\n  white-space: pre-wrap;\n}\n\n.message-search__body mark {\n  padding: 0 1px;\n  border-radius: 2px;\n  background: var(--color-warning-soft);\n  color: var(--color-warning-ink);\n}\n\n.message-search__more {\n  align-self: center;\n}\n"
  },
  {
   "file": "README.md",
   "name": "linha do endpoint na tabela",
   "done": "/api/conversations/{id}/messages/search",
   "after": [
    "| GET    | `/api/conversations/{id}/messages`         | Histórico paginado de mensagens                         |\n"
   ],
   "add": "| GET    | `/api/conversations/{id}/messages/search`  | Busca mensagens da conversa por trecho de texto         |\n"
  },
  {
   "file": "README.md",
   "name": "menção à busca na descrição do cliente",
   "done": "a busca de mensagens dentro da conversa",
   "replace": [
    "exibe a lista de conversas, a janela de chat, a criação de grupos,"
   ],
   "with_": "exibe a lista de conversas, a janela de chat, a busca de mensagens dentro da conversa, a criação de grupos,"
  },
  {
   "file": "back-end/README.md",
   "name": "linha do endpoint na tabela",
   "done": "| GET    | `/api/conversations/{id}/messages/search`",
   "after": [
    "| GET    | `/api/conversations/{id}/messages`         | Histórico paginado de mensagens                  |\n"
   ],
   "add": "| GET    | `/api/conversations/{id}/messages/search`  | Busca mensagens da conversa por trecho de texto  |\n"
  },
  {
   "file": "back-end/README.md",
   "name": "seção \"Busca de mensagens\"",
   "done": "### Busca de mensagens",
   "before": [
    "\n## Configuração\n"
   ],
   "add": "\n### Busca de mensagens\n\n`GET /api/conversations/{id}/messages/search?q=<termo>` devolve as mensagens da conversa cujo texto contém o termo,\nda **mais nova para a mais antiga**, em um array JSON (`[]` quando nada é encontrado). Exige token e que o usuário seja\nmembro da conversa; quem não é membro recebe `404`, igual ao histórico.\n\n| Parâmetro | Obrigatório | Descrição                                                                              |\n|-----------|-------------|----------------------------------------------------------------------------------------|\n| `q`       | sim         | Trecho procurado. De 2 a 100 caracteres, depois de aparar e juntar espaços repetidos    |\n| `before`  | não         | Cursor: só mensagens com `sequence` menor que este valor. Padrão `0` (sem cursor)       |\n| `limit`   | não         | Tamanho da página. Padrão 20, máximo 50                                                 |\n\nPara a página seguinte, repita a chamada com `before` igual à **menor** `sequence` recebida; uma página com `limit`\nitens indica que pode haver mais. Termo inválido (vazio, com 1 caractere, longo demais ou UTF-8 inválido) responde\n`400` com código `invalid_term`; `before` ou `limit` malformados respondem `400` com `invalid_before` ou `invalid_limit`.\n\nLimites conhecidos: o `LIKE` do SQLite ignora maiúsculas **só em ASCII** (`reunião` acha `Reunião`, mas não `REUNIÃO`),\nnão ignora acentos (`reuniao` não acha `reunião`) e varre as mensagens da conversa, sem índice de texto. Os caracteres\n`%`, `_` e `\\` do termo são tratados como texto literal. Uma evolução possível é uma coluna normalizada ou uma tabela\nFTS5 em uma nova migração.\n"
  },
  {
   "file": "back-end/README.md",
   "name": "linhas na tabela \"Identificadores do projeto\"",
   "done": "| `normalizeSearchTerm`",
   "after": [
    "| `body`                   | coluna / payload  | `messages` / `message.send`   | Conteúdo textual da mensagem                          |\n"
   ],
   "add": "| `normalizeSearchTerm`    | função            | `internal/httputil/handlers_search.go` | Apara, junta espaços e valida o termo de busca (2 a 100 caracteres) |\n| `handleSearchMessages`   | método            | `internal/httputil/handlers_search.go` | Atende `GET /api/conversations/{id}/messages/search`  |\n| `minSearchTermRunes`     | constante         | `internal/httputil/handlers_search.go` | Menor termo aceito: 2 caracteres                      |\n| `maxSearchTermRunes`     | constante         | `internal/httputil/handlers_search.go` | Maior termo aceito: 100 caracteres                    |\n| `defaultSearchPage`      | constante         | `internal/httputil/handlers_search.go` | Resultados por página quando `limit` não é enviado (20) |\n| `maxSearchPage`          | constante         | `internal/httputil/handlers_search.go` | Teto de `limit` na busca (50)                         |\n| `errSearchTerm*`         | variáveis         | `internal/httputil/handlers_search.go` | Erros de validação do termo; viram `400 invalid_term`  |\n| `SearchMessages`         | método de `Store` | `internal/db/messages.go`     | Mensagens de uma conversa que contêm o termo (`LIKE`), da mais nova à mais antiga |\n"
  },
  {
   "file": "front-end/README.md",
   "name": "componente na lista da estrutura",
   "done": "busca de mensagens, seletor",
   "replace": [
    "modal, busca de pessoas, seletor de membros,"
   ],
   "with_": "modal, busca de pessoas, busca de mensagens, seletor de membros,"
  },
  {
   "file": "front-end/README.md",
   "name": "utils na lista da estrutura",
   "done": "destaque da busca (search.js)",
   "replace": [
    "regras de apresentação de conversas e validação do cadastro"
   ],
   "with_": "regras de apresentação de conversas, validação do cadastro e destaque da busca (search.js)"
  },
  {
   "file": "front-end/README.md",
   "name": "passo 9 do roteiro de teste manual",
   "done": "9. **Busca:**",
   "after": [
    "   remover alguém (não aparece o botão) e depois sai pelo \"Sair do grupo\".\n"
   ],
   "add": "9. **Busca:** com mensagens trocadas, clique na lupa do cabeçalho da conversa e digite um trecho (2 ou mais\n   caracteres). Os resultados vêm da mais nova para a mais antiga, com o trecho destacado; \"Carregar mais\" aparece\n   com 20 ou mais resultados; **Esc** fecha a busca e trocar de conversa também. A busca exige conexão com o\n   servidor (não usa o cache local). Limite conhecido: `REUNIÃO` não encontra `reunião` e `reuniao` também não.\n"
  }
 ]
};

const dry = process.argv.includes('--dry');
const force = process.argv.includes('--force');

// Trava: só roda na raiz do repositório, para nunca editar o README.md de uma subpasta.
if (!existsSync('back-end/go.mod') || !existsSync('front-end/wails.json')) {
  console.log('✗ Rode este script na RAIZ do repositório (a pasta que contém back-end/ e front-end/).');
  console.log('  Pasta atual: ' + process.cwd());
  process.exit(1);
}

let problems = 0;
let changedFiles = 0;
const toLF = (text) => text.replace(/\r\n/g, '\n');

// 1. Arquivos novos.
for (const [file, content] of Object.entries(spec.newfiles)) {
  if (existsSync(file)) {
    const current = toLF(readFileSync(file, 'utf8'));
    if (current === content) {
      console.log(`= ${file}: já estava aplicado`);
      continue;
    }
    if (!force) {
      console.log(`! ${file}: já existe com conteúdo diferente; não sobrescrevi (use --force se quiser a versão deste script).`);
      continue;
    }
  }
  if (!dry) {
    mkdirSync(dirname(file), { recursive: true });
    writeFileSync(file, content, 'utf8');
  }
  changedFiles++;
  console.log(`+ ${file}: ${dry ? 'seria criado' : 'criado'}`);
}

// 2. Edições em arquivos existentes, agrupadas por arquivo.
const byFile = new Map();
for (const edit of spec.edits) {
  if (!byFile.has(edit.file)) byFile.set(edit.file, []);
  byFile.get(edit.file).push(edit);
}

for (const [file, items] of byFile) {
  if (!existsSync(file)) {
    console.log(`✗ ${file}: arquivo não encontrado. Rode o script na raiz do repositório.`);
    problems++;
    continue;
  }
  const raw = readFileSync(file, 'utf8');
  const crlf = raw.includes('\r\n');
  let text = toLF(raw);
  let changed = false;

  for (const item of items) {
    const label = `${file} → ${item.name}`;
    if (text.includes(item.done)) {
      console.log(`= ${label}: já estava aplicado`);
      continue;
    }

    if (item.append !== undefined) {
      text = text.replace(/\n*$/, '\n') + item.append;
      changed = true;
      console.log(`+ ${label}: ${dry ? 'seria aplicado' : 'aplicado'}`);
      continue;
    }

    const anchors = item.after ?? item.before ?? item.replace;
    const found = anchors.map((a) => [a, text.indexOf(a)]).find(([, i]) => i !== -1);
    if (!found) {
      console.log(`✗ ${label}: trecho de referência não encontrado.`);
      const where = item.after ? 'logo DEPOIS desta linha' : item.before ? 'logo ANTES desta linha' : 'no lugar deste texto (substitua-o)';
      console.log(`  Edite à mão, ${where} do arquivo:`);
      console.log('  ' + anchors[0].trimEnd());
      console.log('  ---- texto a colar ----');
      console.log(item.add ?? item.with_);
      console.log('  -----------------------');
      problems++;
      continue;
    }
    const [anchor, first] = found;
    if (text.indexOf(anchor, first + 1) !== -1) {
      console.log(`✗ ${label}: o trecho de referência aparece mais de uma vez; edite à mão.`);
      problems++;
      continue;
    }
    if (item.after) text = text.slice(0, first + anchor.length) + item.add + text.slice(first + anchor.length);
    else if (item.before) text = text.slice(0, first) + item.add + text.slice(first);
    else text = text.slice(0, first) + item.with_ + text.slice(first + anchor.length);
    changed = true;
    console.log(`+ ${label}: ${dry ? 'seria aplicado' : 'aplicado'}`);
  }

  if (changed) {
    changedFiles++;
    if (!dry) writeFileSync(file, crlf ? text.replace(/\n/g, '\r\n') : text, 'utf8');
  }
}

console.log(
  problems
    ? `\nTerminou com ${problems} aviso(s) acima.`
    : dry
      ? '\nSimulação concluída; nada foi escrito.'
      : '\nPronto. Confira com: git status   e depois rode: make test-back && make test-front && (cd front-end/frontend && npm test)',
);
process.exit(problems ? 1 : 0);
