# Plano de Implementação do Back-end

Este documento divide a implementação do servidor em fases. Cada fase termina com algo executável e testável, e
as fases seguintes se apoiam nas anteriores. As tarefas usam checkboxes para acompanhar o progresso.

O protocolo WebSocket definido aqui também é usado pelo cliente (ver `front-end/PLANO.md`), então qualquer mudança
nele deve ser refletida nos dois planos.

**Convenção de nomes (decisão de equipe):** todo o projeto é escrito em inglês — nomes de pacotes, arquivos,
funções, variáveis, variáveis de ambiente, colunas e tabelas do banco, endpoints da API, tipos do envelope WebSocket
e chaves JSON. O código não mistura idiomas: um mesmo dado mantém o mesmo nome desde a coluna do SQL até o objeto
JavaScript. A **prosa da documentação permanece em português**; apenas os identificadores citados nela são em inglês.
Lista de renomeações aplicadas em relação ao rascunho original: `configuracao`→`config`, `banco`→`db`,
`modelos`→`models`, `rotas`→`httputil`, `persistencia`→`store`, `hub`→`hub` (já em inglês),
`middleware`→`middleware` (já em inglês), `autenticacao`→`auth`, `migracoes`→`migrations`,
`cmd/servidor`→`cmd/server`. No banco e no protocolo: `usuarios`→`users`, `conversas`→`conversations`,
`membros_conversa`→`conversation_members`, `mensagens`→`messages`, `tipo`→`type`, `dados`→`data`,
`presenca`→`presence`, `digitando`→`typing` (ver a tabela completa de renomeações adiante).

---

## Tabela de renomeações (rascunho em português → padrão em inglês)

Esta tabela é o contrato de migração: o nome à esquerda não deve aparecer em nenhum arquivo do repositório, exceto
nesta coluna. Use-a para conferir se algum identificador ficou para trás.

### Pacotes e arquivos

| Antes (português)   | Depois (inglês)        | Observação                                  |
|---------------------|------------------------|---------------------------------------------|
| `cmd/servidor`      | `cmd/server`           | ponto de entrada                            |
| `internal/configuracao` | `internal/config`  | leitura do ambiente                         |
| `internal/banco`    | `internal/db`          | conexão e repositórios                      |
| `internal/modelos`  | `internal/models`      | tipos do domínio                            |
| `internal/rotas`    | `internal/httputil`    | handlers REST e helpers de JSON             |
| `internal/autenticacao` | `internal/auth`    | JWT e bcrypt                                |
| `internal/persistencia` | `internal/store`   | worker pool de gravação                     |
| `internal/middleware` | `internal/middleware` | já em inglês                              |
| `internal/hub`      | `internal/hub`         | já em inglês                                |
| `migracoes/`        | `migrations/`          | scripts SQL                                 |
| `dependencias.go`   | (removido)             | imports provisórios não são mais necessários |

### Banco de dados

| Antes (português)      | Depois (inglês)         | Tipo      |
|------------------------|-------------------------|-----------|
| `usuarios`             | `users`                 | tabela    |
| `conversas`            | `conversations`         | tabela    |
| `membros_conversa`     | `conversation_members`  | tabela    |
| `mensagens`            | `messages`              | tabela    |
| `versao_esquema`       | `schema_version`        | tabela    |
| `nome_usuario`         | `username`              | coluna    |
| `nome_exibicao`        | `display_name`          | coluna    |
| `hash_senha`           | `password_hash`         | coluna    |
| `criado_em`            | `created_at`            | coluna    |
| `criada_em`            | `created_at`            | coluna    |
| `tipo`                 | `type`                  | coluna    |
| `nome`                 | `name`                  | coluna    |
| `criada_por`           | `created_by`            | coluna    |
| `ultima_sequencia`     | `last_sequence`         | coluna    |
| `conversa_id`          | `conversation_id`       | coluna    |
| `usuario_id`           | `user_id`               | coluna    |
| `papel`                | `role`                  | coluna    |
| `ultima_entregue`      | `last_delivered`        | coluna    |
| `ultima_lida`          | `last_read`             | coluna    |
| `entrou_em`            | `joined_at`             | coluna    |
| `sequencia`            | `sequence`              | coluna    |
| `remetente_id`         | `sender_id`             | coluna    |
| `conteudo`             | `body`                  | coluna    |
| `'individual'`         | `'direct'`              | valor     |
| `'grupo'`              | `'group'`               | valor     |
| `'membro'`             | `'member'`              | valor     |

### Protocolo WebSocket e API REST

| Antes (português)      | Depois (inglês)         | Contexto                        |
|------------------------|-------------------------|---------------------------------|
| `tipo`                 | `type`                  | envelope                        |
| `dados`                | `data`                  | envelope                        |
| `mensagem.enviar`      | `message.send`          | cliente → servidor              |
| `mensagem.entregue`    | `message.delivered`     | cliente → servidor              |
| `mensagem.lida`        | `message.read`          | cliente → servidor              |
| `digitando`            | `typing`                | ambos os sentidos               |
| `sincronizar`          | `sync`                  | cliente → servidor              |
| `mensagem.ack`         | `message.ack`           | servidor → cliente              |
| `mensagem.nova`        | `message.new`           | servidor → cliente              |
| `mensagem.status`      | `message.status`        | servidor → cliente              |
| `presenca`             | `presence`              | servidor → cliente              |
| `conversa.atualizada`  | `conversation.updated`  | servidor → cliente              |
| `sincronizacao.fim`    | `sync.end`              | servidor → cliente              |
| `erro`                 | `error`                 | servidor → cliente              |
| `servidor.desligando`  | `server.shutdown`       | servidor → cliente              |
| `codigo`               | `code`                  | payload de `error`              |
| `mensagem`             | `message`               | payload de `error`              |
| `conversa_id`          | `conversation_id`       | payloads                        |
| `usuario_id`           | `user_id`               | payloads                        |
| `ate_sequencia`        | `up_to_sequence`        | payloads de confirmação         |
| `ativo`                | `active`                | payload de `typing`             |
| `conversas`            | `conversations`         | payload de `sync`               |
| `ultima_sequencia`     | `last_sequence`         | payload de `sync`               |
| `criada_em`            | `created_at`            | payload de `message.ack`        |
| `conteudo`             | `body`                  | payload de `message.send`       |
| `nao_lidas`            | `unread_count`          | resposta REST                   |
| `busca`                | `search`                | query string                    |
| `antes`                | `before`                | query string                    |
| `limite`               | `limit`                 | query string                    |
| `saude`                | `health`                | endpoint `/api/health`          |
| `usuario`              | `user`                  | resposta de login               |
| `'pendente'`           | `'pending'`             | status da mensagem              |
| `'enviada'`            | `'sent'`                | status da mensagem              |
| `'entregue'`           | `'delivered'`           | status da mensagem              |
| `'lida'`               | `'read'`                | status da mensagem              |
| `'falhou'`             | `'failed'`              | status da mensagem              |

### Variáveis de ambiente

| Antes (português)      | Depois (inglês)         |
|------------------------|-------------------------|
| `ENDERECO_SERVIDOR`    | `SERVER_ADDRESS`        |
| `CAMINHO_BANCO`        | `DATABASE_PATH`         |
| `SEGREDO_JWT`          | `JWT_SECRET`            |
| `WORKERS_PERSISTENCIA` | `PERSISTENCE_WORKERS`   |
| `URL_SERVIDOR`         | `SERVER_URL`            |

---

## Decisões de projeto

| Tema                | Decisão                                                                                              |
|---------------------|------------------------------------------------------------------------------------------------------|
| Roteamento HTTP     | `http.ServeMux` com padrões do Go 1.22+ (`"POST /api/conversations/{id}/members"`, `r.PathValue`)     |
| Banco               | SQLite com `modernc.org/sqlite`, modo WAL, `busy_timeout`, migrations SQL embutidas com `embed`        |
| Autenticação        | Senhas com bcrypt, JWT HS256 com expiração, enviado no header `Authorization: Bearer <token>`        |
| Autenticação no WS  | O cliente é Go (camada do Wails), então envia o mesmo header `Authorization` no handshake do `/ws`   |
| Formato do WS       | JSON, um envelope `{"type": "...", "data": {...}}` por frame de texto                              |
| Logging             | `log/slog` da biblioteca padrão                                                                      |
| IDs                 | `INTEGER` autoincremento para usuários, conversas e mensagens; UUID gerado no cliente (`client_id`)  |
| Ordenação           | Número de sequência por conversa, atribuído pelo Hub (única goroutine, sem condição de corrida)      |
| Não lidas           | `conversations.last_sequence - conversation_members.last_read`                                       |

---

## Modelo de dados

```sql
users (
  id             INTEGER PRIMARY KEY,
  username       TEXT NOT NULL UNIQUE,
  display_name   TEXT NOT NULL,
  password_hash  TEXT NOT NULL,
  created_at     TIMESTAMP NOT NULL
)

conversations (
  id            INTEGER PRIMARY KEY,
  type          TEXT NOT NULL CHECK (type IN ('direct', 'group')),
  name          TEXT,                       -- grupos apenas
  created_by    INTEGER NOT NULL REFERENCES users(id),
  last_sequence INTEGER NOT NULL DEFAULT 0,
  created_at    TIMESTAMP NOT NULL
)

conversation_members (
  conversation_id INTEGER NOT NULL REFERENCES conversations(id),
  user_id         INTEGER NOT NULL REFERENCES users(id),
  role            TEXT NOT NULL CHECK (role IN ('admin', 'member')),
  last_delivered  INTEGER NOT NULL DEFAULT 0,  -- maior sequência entregue a este membro
  last_read       INTEGER NOT NULL DEFAULT 0,  -- maior sequência lida por este membro
  joined_at       TIMESTAMP NOT NULL,
  PRIMARY KEY (conversation_id, user_id)
)

messages (
  id              INTEGER PRIMARY KEY,
  conversation_id INTEGER NOT NULL REFERENCES conversations(id),
  sequence        INTEGER NOT NULL,
  sender_id       INTEGER NOT NULL REFERENCES users(id),
  client_id       TEXT NOT NULL,               -- UUID gerado no cliente, evita duplicatas
  body            TEXT NOT NULL,
  created_at      TIMESTAMP NOT NULL,
  UNIQUE (conversation_id, sequence),
  UNIQUE (sender_id, client_id)
)
```

O arquivo `migrations/0001_initial.sql` é a fonte de verdade deste modelo; a listagem acima é o resumo legível.

Uma conversa individual entre A e B deve ser única: ao criar, verificar se já existe e devolver a existente.

---

## Protocolo WebSocket

Todo frame é um envelope JSON:

```json
{ "type": "message.send", "data": { } }
```

### Cliente → servidor

| Tipo                 | Dados                                            | Efeito                                                     |
|----------------------|--------------------------------------------------|------------------------------------------------------------|
| `message.send`       | `client_id`, `conversation_id`, `body`           | Envia uma mensagem; o servidor responde com `message.ack`  |
| `message.delivered`  | `conversation_id`, `up_to_sequence`              | Confirma o recebimento até aquela sequência                |
| `message.read`       | `conversation_id`, `up_to_sequence`              | Marca como lidas até aquela sequência                      |
| `typing`             | `conversation_id`, `active`                      | Indica que o usuário começou/parou de digitar              |
| `sync`               | `conversations: [{conversation_id, last_sequence}]` | Pede as mensagens recebidas enquanto estava offline      |

### Servidor → cliente

| Tipo                  | Dados                                                       | Quando                                                     |
|-----------------------|-------------------------------------------------------------|------------------------------------------------------------|
| `message.ack`         | `client_id`, `id`, `conversation_id`, `sequence`, `created_at` | Mensagem aceita (inclusive reenvio de duplicata)         |
| `message.new`         | mensagem completa                                           | Nova mensagem em uma conversa do usuário                   |
| `message.status`      | `conversation_id`, `user_id`, `up_to_sequence`, `status`    | Um membro confirmou entrega (`delivered`) ou leitura (`read`) |
| `presence`            | `user_id`, `online`                                         | Um contato entrou ou saiu                                  |
| `typing`              | `conversation_id`, `user_id`, `active`                      | Um membro está digitando                                   |
| `conversation.updated`| conversa com membros                                        | Conversa criada, membro adicionado ou removido             |
| `sync.end`            | —                                                           | Fim do envio das mensagens pedidas em `sync`               |
| `error`               | `code`, `message`, `client_id` (opcional)                   | Requisição inválida (ex.: não é membro da conversa)        |
| `server.shutdown`     | —                                                           | Graceful shutdown iniciado; o cliente deve reconectar      |

Valores de `status`: `pending` (na fila), `sent` (ACK), `delivered` e `read`; `failed` para erro definitivo do
servidor. A interface é responsável por traduzir esses valores para o texto exibido ao usuário.

"Contatos" de um usuário = todos os usuários que compartilham ao menos uma conversa com ele.

---

## Fase 0 — Fundamentos

- [ ] `config`: struct `Config` e função `Load()` que lê `SERVER_ADDRESS`, `DATABASE_PATH`,
      `JWT_SECRET`, `PERSISTENCE_WORKERS` com valores padrão; falhar se `JWT_SECRET` estiver vazio fora do
      modo dev (`DEV_MODE=true`).
- [ ] Adicionar variáveis para os parâmetros de tempo: `PING_INTERVAL` (padrão 30s), `PONG_TIMEOUT` (60s),
      `TOKEN_EXPIRY` (24h), `CLIENT_QUEUE_SIZE` (256), `PERSISTENCE_QUEUE_SIZE` (4096), `MESSAGE_LIMIT_CHARS`
      (4000), `TYPING_COOLDOWN` (2s).

      > **Estado atual:** `internal/config` já implementa `SERVER_ADDRESS`, `DATABASE_PATH`, `JWT_SECRET`,
      > `DEV_MODE`, `PERSISTENCE_WORKERS`, `PING_INTERVAL`, `PONG_TIMEOUT`, `TOKEN_EXPIRY`, `CLIENT_QUEUE_SIZE` e
      > `PERSISTENCE_QUEUE_SIZE`, e o `.env.example` lista todas as dez. `MESSAGE_LIMIT_CHARS` e `TYPING_COOLDOWN`
      > ainda **não** existem no código: entram junto das Fases 5 e 7, que são as que de fato os usam.
- [ ] `cmd/server/main.go`: carregar configuração, configurar `slog`, criar `http.Server` com `ReadHeaderTimeout`,
      e esqueleto do graceful shutdown com `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`.
- [ ] Endpoint `GET /api/health` retornando `200` (útil para testes e para o cliente verificar o servidor).
- [ ] `middleware`: logging de requisições (método, caminho, status, duração) e recuperação de `panic`.
- [ ] Utilitários em `httputil`: `WriteJSON(w, status, v)`, `WriteError(w, status, code, message)`,
      `ReadJSON(r, &v)` com `http.MaxBytesReader` e `DisallowUnknownFields`.

**Pronto quando:** `go run ./cmd/server` sobe, responde em `/api/health` e encerra limpo com Ctrl+C.

---

## Fase 1 — Banco de dados

- [ ] Escrever `migrations/0001_initial.sql` com o modelo de dados acima e os índices
      (`messages(conversation_id, sequence)`, `conversation_members(user_id)`).
- [ ] `db.Open(ctx, path)`: abrir SQLite com `PRAGMA journal_mode=WAL`, `foreign_keys=ON`,
      `busy_timeout=5000`; configurar `SetMaxOpenConns` adequado ao SQLite.
- [ ] `db.Migrate(ctx, db)`: embutir `migrations/*.sql` com `embed`, tabela `schema_version`, aplicar em transação
      apenas as migrations pendentes.
- [ ] `models`: structs `User`, `Conversation`, `Member`, `Message` com tags JSON em inglês.
- [ ] Repositórios em `db` (todos recebendo `context.Context`):
  - [ ] Usuários: `CreateUser`, `GetUserByUsername`, `GetUserByID`, `SearchUsers(term, limit)`.
  - [ ] Conversas: `CreateConversation` (em transação, com membros), `GetDirectConversation(a, b)`,
        `ListUserConversations` (com contador de não lidas e última mensagem), `GetConversation`.
  - [ ] Membros: `AddMembers`, `RemoveMember`, `ListMembers`, `IsMember`, `ListContacts(userID)`.
  - [ ] Mensagens: `InsertMessages` em lote (`INSERT ... ON CONFLICT DO NOTHING`),
        `ListMessages(conversationID, beforeSequence, limit)`, `ListMessagesAfter(conversationID, sequence)`,
        `LastSequence(conversationID)`.
  - [ ] Status: `MarkDelivered` e `MarkRead` (só avançam: `MAX(atual, nova)`).
- [ ] Testes dos repositórios com banco em memória (`file::memory:?cache=shared`) ou arquivo temporário (`t.TempDir()`).

**Pronto quando:** o servidor cria o banco na primeira execução e os testes dos repositórios passam.

---

## Fase 2 — Autenticação

- [ ] `auth.HashPassword(password)` e `auth.CheckPassword(hash, password)` com bcrypt.
- [ ] `auth.GenerateToken(userID)` e `auth.ValidateToken(token)` com `jwt/v5`, HS256, claims `sub` e `exp`;
      rejeitar algoritmos diferentes de HS256.
- [ ] `POST /api/auth/register`: validar `username` (3–32 caracteres, `[a-z0-9_]`), `display_name` e `password`
      (mínimo 8); responder `201` com o usuário; `409` se o nome já existir.
- [ ] `POST /api/auth/login`: responder `200` com `{ token, user }`; `401` com mensagem genérica se falhar.
- [ ] `middleware.Authenticate`: extrair o Bearer token, validar e colocar o `userID` no `context.Context`
      (chave de tipo não exportado); função `UserFromContext(ctx)`.
- [ ] Testes: hash/conferência, token expirado, token com assinatura errada, handlers com `httptest`.

**Pronto quando:** é possível cadastrar, logar e acessar uma rota protegida com o token.

---

## Fase 3 — API REST

Todas as rotas abaixo usam `middleware.Authenticate`.

- [ ] `GET /api/users?search=<termo>`: busca por `username`/`display_name`, limite de 20, excluindo o próprio usuário;
      incluir `online` consultando o Hub.
- [ ] `GET /api/conversations`: conversas do usuário com membros, última mensagem e `unread_count`, ordenadas pela última atividade.
- [ ] `POST /api/conversations`:
  - [ ] `{ "type": "direct", "members": [id] }` → devolve a conversa existente (`200`) ou cria (`201`).
  - [ ] `{ "type": "group", "name": "...", "members": [ids] }` → cria com o criador como `admin`.
  - [ ] Validar que os usuários existem; notificar os membros online com `conversation.updated`.
- [ ] `POST /api/conversations/{id}/members`: só grupos, só membros podem adicionar; notificar via Hub.
- [ ] `DELETE /api/conversations/{id}/members/{userId}`: o próprio usuário pode sair; um `admin` pode remover outros;
      notificar via Hub e atualizar o cache de membros do Hub.
- [ ] `GET /api/conversations/{id}/messages?before=<sequence>&limit=<n>`: paginação por sequência
      (padrão 50, máximo 100), só para membros (`403` caso contrário), resposta em ordem crescente de sequência.
- [ ] Testes de cada handler com `httptest` cobrindo sucesso, `400`, `401`, `403` e `404`.

**Pronto quando:** todas as rotas da tabela do README funcionam via `curl`.

---

## Fase 4 — Hub e conexões WebSocket

- [ ] `hub.Client`: `userID`, conexão, canal `send chan []byte` com buffer (`CLIENT_QUEUE_SIZE`),
      `context.CancelFunc` próprio.
- [ ] `hub.Hub` com:
  - [ ] Canais `register`, `unregister`, `incoming` (mensagens vindas dos clientes) e `notify`
        (eventos vindos da API REST, ex.: membros alterados).
  - [ ] `map[int64]map[*Client]struct{}` de usuários conectados (um usuário pode ter mais de uma conexão).
  - [ ] Cache `map[int64]map[int64]struct{}` de membros por conversa, carregado do banco sob demanda.
  - [ ] `sync.RWMutex` protegendo as estruturas lidas por outras goroutines (ex.: `IsOnline(userID)` usado pela API).
  - [ ] `Run(ctx)`: laço com `select` sobre `register`, `unregister`, `incoming`, `notify` e `ctx.Done()`.
- [ ] Handler `GET /ws`: autenticar pelo header `Authorization`, `websocket.Accept`, limitar tamanho da mensagem
      (`SetReadLimit`), criar o `Client`, registrar no Hub e iniciar as goroutines:
  - [ ] `readMessages`: lê frames, decodifica o envelope, valida e envia para `hub.incoming`; ao sair, desregistra.
  - [ ] `writeMessages`: consome `client.send` e escreve com timeout por escrita (`context.WithTimeout`).
- [ ] Cliente lento: se `client.send` estiver cheio, o Hub não bloqueia — fecha a conexão com
      `websocket.StatusPolicyViolation` e desregistra.
- [ ] Ao registrar a primeira conexão de um usuário: enviar `presence {online: true}` aos contatos online.
- [ ] Ao desregistrar a última conexão: enviar `presence {online: false}`.
- [ ] Testes do Hub com clientes falsos (sem rede) e um teste de integração com `httptest.NewServer` e dois clientes WS.

**Pronto quando:** dois clientes conectam, cada um recebe a presença do outro e a desconexão é detectada.

---

## Fase 5 — Envio de mensagens

- [ ] Tratar `message.send` no Hub:
  - [ ] Verificar se o remetente é membro da conversa (senão, `error` com o `client_id`).
  - [ ] Validar o conteúdo (não vazio, tamanho máximo, ex.: 4000 caracteres).
  - [ ] Deduplicação: manter um cache recente `(sender_id, client_id) → ack`; se já existir, reenviar o mesmo ACK
        sem gerar nova mensagem. O `UNIQUE (sender_id, client_id)` do banco é a garantia final.
  - [ ] Atribuir a próxima `sequence` da conversa (contador em memória inicializado com `LastSequence` do banco).
  - [ ] Preencher remetente e `created_at` (horário do servidor, UTC).
  - [ ] Responder ao remetente com `message.ack`.
  - [ ] Colocar a mensagem na fila de persistência (Fase 6).
  - [ ] Entregar `message.new` a todas as conexões dos membros online, inclusive outras conexões do remetente.
- [ ] Atualizar `conversations.last_sequence` junto com a gravação das mensagens.
- [ ] Testes: ordem das sequências com envios concorrentes, reenvio do mesmo `client_id`, envio para conversa alheia.

**Pronto quando:** mensagens chegam em tempo real aos membros conectados, na ordem, sem duplicatas.

---

## Fase 6 — Persistência com worker pool

- [ ] `store.Pool`: canal `queue chan models.Message` com buffer (`PERSISTENCE_QUEUE_SIZE`)
      e `PERSISTENCE_WORKERS` goroutines.
- [ ] Cada worker acumula mensagens em lote (até N mensagens ou T milissegundos, com `time.Ticker`)
      e grava com `InsertMessages` em uma transação.
- [ ] Tratar falhas de gravação: tentar novamente com backoff limitado e registrar em log o que não puder ser gravado.
- [ ] `Enqueue(msg)`: se a fila estiver cheia, aplicar backpressure (bloquear com timeout) em vez de descartar.
- [ ] `Shutdown(ctx)`: fechar a fila, esperar os workers esvaziarem tudo com `sync.WaitGroup`, respeitando o prazo do `ctx`.
- [ ] Atenção à ordem: como vários workers gravam em paralelo, a ordem na tabela não importa —
      a ordem é dada pela coluna `sequence`.
- [ ] Testes: todas as mensagens enfileiradas são gravadas após `Shutdown`; comportamento com fila cheia.

**Pronto quando:** o histórico via `GET /api/conversations/{id}/messages` contém as mensagens enviadas pelo WS.

---

## Fase 7 — Status, presença e "digitando"

- [ ] `message.delivered`: atualizar `last_delivered` do membro e enviar `message.status {status: "delivered"}`
      ao remetente (e demais membros, para grupos).
- [ ] `message.read`: atualizar `last_read` (e `last_delivered`, se menor) e enviar `message.status {status: "read"}`.
- [ ] Gravar as atualizações de status de forma assíncrona (pela fila de persistência ou fila própria),
      agrupando várias confirmações da mesma conversa.
- [ ] Em grupos, a resposta de `GET /api/conversations` inclui `last_delivered`/`last_read` de cada membro para o
      cliente calcular o status (entregue quando todos receberam, lida quando todos leram).
- [ ] `typing`: repassar apenas aos outros membros online da conversa, sem persistir;
      limitar a frequência por usuário (`TYPING_COOLDOWN`, ex.: no máximo 1 evento a cada 2s).
- [ ] `GET /api/users` e a lista de membros incluem o estado `online` atual.

**Pronto quando:** o cliente consegue mostrar enviada → entregue → lida, presença e "digitando...".

---

## Fase 8 — Sincronização após reconexão

- [ ] Tratar `sync`: para cada conversa informada, buscar `ListMessagesAfter(conversation_id, last_sequence)`
      e enviar como `message.new` em ordem; conversas do usuário não informadas também são incluídas
      (ex.: grupo criado enquanto estava offline).
- [ ] Limitar a quantidade por conversa (ex.: 500) — além disso o cliente usa a paginação REST.
- [ ] Enviar `sync.end` ao terminar.
- [ ] Mensagens ainda na fila de persistência também devem ser consideradas: consultar o banco **e** um buffer em memória
      das mensagens recentes ainda não gravadas, ou forçar a gravação antes da consulta.
- [ ] Teste: cliente desconecta, recebe mensagens enquanto offline, reconecta e recebe todas, em ordem e sem duplicatas.

**Pronto quando:** nenhuma mensagem é perdida em uma queda de conexão.

---

## Fase 9 — Heartbeat e limpeza de conexões

- [ ] Na goroutine de escrita, um `time.Ticker` com `PING_INTERVAL` chama `conn.Ping(ctx)` com timeout `PONG_TIMEOUT`.
- [ ] Se o ping falhar, cancelar o contexto do cliente, fechar a conexão e desregistrar no Hub.
- [ ] Garantir que as duas goroutines do cliente terminem quando qualquer uma delas falhar (contexto compartilhado).
- [ ] Teste: cliente que para de responder é removido do Hub e os contatos recebem `presence {online: false}`.
- [ ] Verificar ausência de vazamento de goroutines após conexões e desconexões repetidas (`runtime.NumGoroutine`).

**Pronto quando:** conexões mortas são detectadas e liberadas em até `PING_INTERVAL + PONG_TIMEOUT`.

---

## Fase 10 — Graceful shutdown

Ordem de encerramento ao receber `SIGINT`/`SIGTERM`:

- [ ] 1. Parar de aceitar novas conexões e requisições: `http.Server.Shutdown(ctx)` com prazo (ex.: 15s).
- [ ] 2. Enviar `server.shutdown` a todos os clientes e fechar as conexões com `websocket.StatusGoingAway`.
- [ ] 3. Parar o Hub (cancelar seu contexto) e esperar o laço terminar.
- [ ] 4. Encerrar o pool de persistência, gravando todas as mensagens pendentes.
- [ ] 5. Fechar o banco.
- [ ] Usar um `sync.WaitGroup` para as goroutines de longa duração (Hub, workers, conexões) e `context` para o prazo total.
- [ ] Teste: enviar mensagens e, em seguida, `SIGTERM` — todas devem estar no banco após o encerramento.

**Pronto quando:** desligar o servidor não perde mensagens aceitas (que receberam ACK).

---

## Fase 11 — Qualidade e desempenho

- [ ] `go test -race ./...` sem falhas em todas as fases.
- [ ] Teste de carga em `cmd/loadgen`: abre N conexões WebSocket (ex.: 1.000–5.000), envia mensagens em grupos e mede
      latência (p50/p99), mensagens por segundo, memória e número de goroutines.
- [ ] Expor `net/http/pprof` em um endereço separado apenas em modo dev, para analisar CPU e memória.
- [ ] Revisar limites: tamanho de mensagem, taxa de mensagens por cliente, tamanho dos buffers.
- [ ] Atualizar o `README.md` do back-end com instruções de execução, variáveis novas e resultados do teste de carga.

---

## Ordem sugerida e dependências

```
Fase 0 → Fase 1 → Fase 2 → Fase 3
                          ↘
                   Fase 4 → Fase 5 → Fase 6 → Fase 7 → Fase 8
                          ↘
                           Fase 9 → Fase 10 → Fase 11
```

O cliente pode começar a ser integrado ao fim da Fase 3 (REST) e da Fase 5 (mensagens em tempo real).
