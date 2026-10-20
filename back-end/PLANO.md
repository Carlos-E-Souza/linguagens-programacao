# Plano de Implementação do Back-end

Este documento divide a implementação do servidor em fases. Cada fase termina com algo executável e testável, e
as fases seguintes se apoiam nas anteriores. As tarefas usam checkboxes para acompanhar o progresso.

O protocolo WebSocket definido aqui também é usado pelo cliente (ver `front-end/PLANO.md`), então qualquer mudança
nele deve ser refletida nos dois planos.

---

## Decisões de projeto

| Tema                | Decisão                                                                                              |
|---------------------|------------------------------------------------------------------------------------------------------|
| Roteamento HTTP     | `http.ServeMux` com padrões do Go 1.22+ (`"POST /api/conversations/{id}/members"`, `r.PathValue`)     |
| Banco               | SQLite com `modernc.org/sqlite`, modo WAL, `busy_timeout`, migrações SQL embutidas com `embed`        |
| Autenticação        | Senhas com bcrypt, JWT HS256 com expiração, enviado no header `Authorization: Bearer <token>`        |
| Autenticação no WS  | O cliente é Go (camada do Wails), então envia o mesmo header `Authorization` no handshake do `/ws`   |
| Formato do WS       | JSON, um envelope `{"tipo": "...", "dados": {...}}` por frame de texto                               |
| Logging             | `log/slog` da biblioteca padrão                                                                      |
| IDs                 | `INTEGER` autoincremento para usuários, conversas e mensagens; UUID gerado no cliente (`id_cliente`) |
| Ordenação           | Número de sequência por conversa, atribuído pelo Hub (única goroutine, sem condição de corrida)      |
| Não lidas           | `conversas.ultima_sequencia - membros_conversa.ultima_lida`                                          |

---

## Modelo de dados

```sql
usuarios (
  id             INTEGER PRIMARY KEY,
  nome_usuario   TEXT NOT NULL UNIQUE,
  nome_exibicao  TEXT NOT NULL,
  hash_senha     TEXT NOT NULL,
  criado_em      TIMESTAMP NOT NULL
)

conversas (
  id                INTEGER PRIMARY KEY,
  tipo              TEXT NOT NULL CHECK (tipo IN ('individual', 'grupo')),
  nome              TEXT,                      -- só para grupos
  criada_por        INTEGER NOT NULL REFERENCES usuarios(id),
  ultima_sequencia  INTEGER NOT NULL DEFAULT 0,
  criada_em         TIMESTAMP NOT NULL
)

membros_conversa (
  conversa_id       INTEGER NOT NULL REFERENCES conversas(id),
  usuario_id        INTEGER NOT NULL REFERENCES usuarios(id),
  papel             TEXT NOT NULL CHECK (papel IN ('admin', 'membro')),
  ultima_entregue   INTEGER NOT NULL DEFAULT 0,  -- maior sequência entregue a este membro
  ultima_lida       INTEGER NOT NULL DEFAULT 0,  -- maior sequência lida por este membro
  entrou_em         TIMESTAMP NOT NULL,
  PRIMARY KEY (conversa_id, usuario_id)
)

mensagens (
  id            INTEGER PRIMARY KEY,
  conversa_id   INTEGER NOT NULL REFERENCES conversas(id),
  sequencia     INTEGER NOT NULL,
  remetente_id  INTEGER NOT NULL REFERENCES usuarios(id),
  id_cliente    TEXT NOT NULL,                 -- UUID gerado no cliente, evita duplicatas
  conteudo      TEXT NOT NULL,
  criada_em     TIMESTAMP NOT NULL,
  UNIQUE (conversa_id, sequencia),
  UNIQUE (remetente_id, id_cliente)
)
```

Uma conversa individual entre A e B deve ser única: ao criar, verificar se já existe e devolver a existente.

---

## Protocolo WebSocket

Todo frame é um envelope JSON:

```json
{ "tipo": "mensagem.enviar", "dados": { } }
```

### Cliente → servidor

| Tipo                 | Dados                                              | Efeito                                                     |
|----------------------|----------------------------------------------------|------------------------------------------------------------|
| `mensagem.enviar`    | `id_cliente`, `conversa_id`, `conteudo`            | Envia uma mensagem; o servidor responde com `mensagem.ack` |
| `mensagem.entregue`  | `conversa_id`, `ate_sequencia`                     | Confirma o recebimento até aquela sequência                |
| `mensagem.lida`      | `conversa_id`, `ate_sequencia`                     | Marca como lidas até aquela sequência                      |
| `digitando`          | `conversa_id`, `ativo`                             | Indica que o usuário começou/parou de digitar              |
| `sincronizar`        | `conversas: [{conversa_id, ultima_sequencia}]`     | Pede as mensagens recebidas enquanto estava offline        |

### Servidor → cliente

| Tipo                 | Dados                                                         | Quando                                                    |
|----------------------|---------------------------------------------------------------|-----------------------------------------------------------|
| `mensagem.ack`       | `id_cliente`, `id`, `conversa_id`, `sequencia`, `criada_em`   | Mensagem aceita (inclusive reenvio de duplicata)          |
| `mensagem.nova`      | mensagem completa                                             | Nova mensagem em uma conversa do usuário                  |
| `mensagem.status`    | `conversa_id`, `usuario_id`, `ate_sequencia`, `status`        | Um membro confirmou entrega (`entregue`) ou leitura (`lida`) |
| `presenca`           | `usuario_id`, `online`                                        | Um contato entrou ou saiu                                 |
| `digitando`          | `conversa_id`, `usuario_id`, `ativo`                          | Um membro está digitando                                  |
| `conversa.atualizada`| conversa com membros                                          | Conversa criada, membro adicionado ou removido            |
| `sincronizacao.fim`  | —                                                             | Fim do envio das mensagens pedidas em `sincronizar`       |
| `erro`               | `codigo`, `mensagem`, `id_cliente` (opcional)                 | Requisição inválida (ex.: não é membro da conversa)       |
| `servidor.desligando`| —                                                             | Graceful shutdown iniciado; o cliente deve reconectar     |

"Contatos" de um usuário = todos os usuários que compartilham ao menos uma conversa com ele.

---

## Fase 0 — Fundamentos

- [ ] `configuracao`: struct `Configuracao` e função `Carregar()` que lê `ENDERECO_SERVIDOR`, `CAMINHO_BANCO`,
      `SEGREDO_JWT`, `WORKERS_PERSISTENCIA` com valores padrão; falhar se `SEGREDO_JWT` estiver vazio fora do modo dev.
- [ ] Adicionar variáveis para os parâmetros de tempo: `INTERVALO_PING` (padrão 30s), `TIMEOUT_PONG` (60s),
      `EXPIRACAO_TOKEN` (24h), `TAMANHO_FILA_CLIENTE` (256), `TAMANHO_FILA_PERSISTENCIA` (4096).
- [ ] `cmd/servidor/main.go`: carregar configuração, configurar `slog`, criar `http.Server` com `ReadHeaderTimeout`,
      e esqueleto do graceful shutdown com `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`.
- [ ] Endpoint `GET /api/saude` retornando `200` (útil para testes e para o cliente verificar o servidor).
- [ ] `middleware`: logging de requisições (método, caminho, status, duração) e recuperação de `panic`.
- [ ] Utilitários em `rotas`: `responderJSON(w, status, v)`, `responderErro(w, status, codigo, mensagem)`,
      `lerJSON(r, &v)` com `http.MaxBytesReader` e `DisallowUnknownFields`.

**Pronto quando:** `go run ./cmd/servidor` sobe, responde em `/api/saude` e encerra limpo com Ctrl+C.

---

## Fase 1 — Banco de dados

- [ ] Escrever `migracoes/0001_inicial.sql` com o modelo de dados acima e os índices
      (`mensagens(conversa_id, sequencia)`, `membros_conversa(usuario_id)`).
- [ ] `banco.Abrir(ctx, caminho)`: abrir SQLite com `PRAGMA journal_mode=WAL`, `foreign_keys=ON`,
      `busy_timeout=5000`; configurar `SetMaxOpenConns` adequado ao SQLite.
- [ ] `banco.Migrar(ctx, db)`: embutir `migracoes/*.sql` com `embed`, tabela `versao_esquema`, aplicar em transação
      apenas as migrações pendentes.
- [ ] `modelos`: structs `Usuario`, `Conversa`, `Membro`, `Mensagem` com tags JSON em português.
- [ ] Repositórios em `banco` (todos recebendo `context.Context`):
  - [ ] Usuários: `CriarUsuario`, `BuscarUsuarioPorNome`, `BuscarUsuarioPorID`, `PesquisarUsuarios(termo, limite)`.
  - [ ] Conversas: `CriarConversa` (em transação, com membros), `BuscarConversaIndividual(a, b)`,
        `ListarConversasDoUsuario` (com contador de não lidas e última mensagem), `BuscarConversa`.
  - [ ] Membros: `AdicionarMembros`, `RemoverMembro`, `ListarMembros`, `EhMembro`, `ListarContatos(usuarioID)`.
  - [ ] Mensagens: `InserirMensagens` em lote (`INSERT ... ON CONFLICT DO NOTHING`),
        `ListarMensagens(conversaID, antesDaSequencia, limite)`, `ListarMensagensApos(conversaID, sequencia)`,
        `UltimaSequencia(conversaID)`.
  - [ ] Status: `AtualizarEntregue` e `AtualizarLida` (só avançam: `MAX(atual, nova)`).
- [ ] Testes dos repositórios com banco em memória (`file::memory:?cache=shared`) ou arquivo temporário (`t.TempDir()`).

**Pronto quando:** o servidor cria o banco na primeira execução e os testes dos repositórios passam.

---

## Fase 2 — Autenticação

- [ ] `autenticacao.GerarHash(senha)` e `autenticacao.ConferirSenha(hash, senha)` com bcrypt.
- [ ] `autenticacao.GerarToken(usuarioID)` e `autenticacao.ValidarToken(token)` com `jwt/v5`, HS256, claims `sub` e `exp`;
      rejeitar algoritmos diferentes de HS256.
- [ ] `POST /api/auth/register`: validar `nome_usuario` (3–32 caracteres, `[a-z0-9_]`), `nome_exibicao` e `senha`
      (mínimo 8); responder `201` com o usuário; `409` se o nome já existir.
- [ ] `POST /api/auth/login`: responder `200` com `{ token, usuario }`; `401` com mensagem genérica se falhar.
- [ ] `middleware.Autenticar`: extrair o Bearer token, validar e colocar o `usuarioID` no `context.Context`
      (chave de tipo não exportado); função `UsuarioDoContexto(ctx)`.
- [ ] Testes: hash/conferência, token expirado, token com assinatura errada, handlers com `httptest`.

**Pronto quando:** é possível cadastrar, logar e acessar uma rota protegida com o token.

---

## Fase 3 — API REST

Todas as rotas abaixo usam `middleware.Autenticar`.

- [ ] `GET /api/users?busca=<termo>`: busca por `nome_usuario`/`nome_exibicao`, limite de 20, excluindo o próprio usuário;
      incluir `online` consultando o Hub.
- [ ] `GET /api/conversations`: conversas do usuário com membros, última mensagem e `nao_lidas`, ordenadas pela última atividade.
- [ ] `POST /api/conversations`:
  - [ ] `{ "tipo": "individual", "membros": [id] }` → devolve a conversa existente (`200`) ou cria (`201`).
  - [ ] `{ "tipo": "grupo", "nome": "...", "membros": [ids] }` → cria com o criador como `admin`.
  - [ ] Validar que os usuários existem; notificar os membros online com `conversa.atualizada`.
- [ ] `POST /api/conversations/{id}/members`: só grupos, só membros podem adicionar; notificar via Hub.
- [ ] `DELETE /api/conversations/{id}/members/{userId}`: o próprio usuário pode sair; um `admin` pode remover outros;
      notificar via Hub e atualizar o cache de membros do Hub.
- [ ] `GET /api/conversations/{id}/messages?antes=<sequencia>&limite=<n>`: paginação por sequência
      (padrão 50, máximo 100), só para membros (`403` caso contrário), resposta em ordem crescente de sequência.
- [ ] Testes de cada handler com `httptest` cobrindo sucesso, `400`, `401`, `403` e `404`.

**Pronto quando:** todas as rotas da tabela do README funcionam via `curl`.

---

## Fase 4 — Hub e conexões WebSocket

- [ ] `hub.Cliente`: `usuarioID`, conexão, canal `enviar chan []byte` com buffer (`TAMANHO_FILA_CLIENTE`),
      `context.CancelFunc` próprio.
- [ ] `hub.Hub` com:
  - [ ] Canais `registrar`, `desregistrar`, `entrada` (mensagens vindas dos clientes) e `notificar`
        (eventos vindos da API REST, ex.: membros alterados).
  - [ ] `map[int64]map[*Cliente]struct{}` de usuários conectados (um usuário pode ter mais de uma conexão).
  - [ ] Cache `map[int64]map[int64]struct{}` de membros por conversa, carregado do banco sob demanda.
  - [ ] `sync.RWMutex` protegendo as estruturas lidas por outras goroutines (ex.: `EstaOnline(usuarioID)` usado pela API).
  - [ ] `Executar(ctx)`: laço com `select` sobre `registrar`, `desregistrar`, `entrada`, `notificar` e `ctx.Done()`.
- [ ] Handler `GET /ws`: autenticar pelo header `Authorization`, `websocket.Accept`, limitar tamanho da mensagem
      (`SetReadLimit`), criar o `Cliente`, registrar no Hub e iniciar as goroutines:
  - [ ] `lerMensagens`: lê frames, decodifica o envelope, valida e envia para `hub.entrada`; ao sair, desregistra.
  - [ ] `escreverMensagens`: consome `cliente.enviar` e escreve com timeout por escrita (`context.WithTimeout`).
- [ ] Cliente lento: se `cliente.enviar` estiver cheio, o Hub não bloqueia — fecha a conexão com
      `websocket.StatusPolicyViolation` e desregistra.
- [ ] Ao registrar a primeira conexão de um usuário: enviar `presenca {online: true}` aos contatos online.
- [ ] Ao desregistrar a última conexão: enviar `presenca {online: false}`.
- [ ] Testes do Hub com clientes falsos (sem rede) e um teste de integração com `httptest.NewServer` e dois clientes WS.

**Pronto quando:** dois clientes conectam, cada um recebe a presença do outro e a desconexão é detectada.

---

## Fase 5 — Envio de mensagens

- [ ] Tratar `mensagem.enviar` no Hub:
  - [ ] Verificar se o remetente é membro da conversa (senão, `erro` com o `id_cliente`).
  - [ ] Validar o conteúdo (não vazio, tamanho máximo, ex.: 4000 caracteres).
  - [ ] Deduplicação: manter um cache recente `(remetente_id, id_cliente) → ack`; se já existir, reenviar o mesmo ACK
        sem gerar nova mensagem. O `UNIQUE (remetente_id, id_cliente)` do banco é a garantia final.
  - [ ] Atribuir a próxima `sequencia` da conversa (contador em memória inicializado com `UltimaSequencia` do banco).
  - [ ] Preencher remetente e `criada_em` (horário do servidor, UTC).
  - [ ] Responder ao remetente com `mensagem.ack`.
  - [ ] Colocar a mensagem na fila de persistência (Fase 6).
  - [ ] Entregar `mensagem.nova` a todas as conexões dos membros online, inclusive outras conexões do remetente.
- [ ] Atualizar `conversas.ultima_sequencia` junto com a gravação das mensagens.
- [ ] Testes: ordem das sequências com envios concorrentes, reenvio do mesmo `id_cliente`, envio para conversa alheia.

**Pronto quando:** mensagens chegam em tempo real aos membros conectados, na ordem, sem duplicatas.

---

## Fase 6 — Persistência com worker pool

- [ ] `persistencia.Pool`: canal `fila chan modelos.Mensagem` com buffer (`TAMANHO_FILA_PERSISTENCIA`)
      e `WORKERS_PERSISTENCIA` goroutines.
- [ ] Cada worker acumula mensagens em lote (até N mensagens ou T milissegundos, com `time.Ticker`)
      e grava com `InserirMensagens` em uma transação.
- [ ] Tratar falhas de gravação: tentar novamente com backoff limitado e registrar em log o que não puder ser gravado.
- [ ] `Enfileirar(msg)`: se a fila estiver cheia, aplicar backpressure (bloquear com timeout) em vez de descartar.
- [ ] `Encerrar(ctx)`: fechar a fila, esperar os workers esvaziarem tudo com `sync.WaitGroup`, respeitando o prazo do `ctx`.
- [ ] Atenção à ordem: como vários workers gravam em paralelo, a ordem na tabela não importa —
      a ordem é dada pela coluna `sequencia`.
- [ ] Testes: todas as mensagens enfileiradas são gravadas após `Encerrar`; comportamento com fila cheia.

**Pronto quando:** o histórico via `GET /api/conversations/{id}/messages` contém as mensagens enviadas pelo WS.

---

## Fase 7 — Status, presença e "digitando"

- [ ] `mensagem.entregue`: atualizar `ultima_entregue` do membro e enviar `mensagem.status {status: "entregue"}`
      ao remetente (e demais membros, para grupos).
- [ ] `mensagem.lida`: atualizar `ultima_lida` (e `ultima_entregue`, se menor) e enviar `mensagem.status {status: "lida"}`.
- [ ] Gravar as atualizações de status de forma assíncrona (pela fila de persistência ou fila própria),
      agrupando várias confirmações da mesma conversa.
- [ ] Em grupos, a resposta de `GET /api/conversations` inclui `ultima_entregue`/`ultima_lida` de cada membro para o
      cliente calcular o status ("entregue" quando todos receberam, "lida" quando todos leram).
- [ ] `digitando`: repassar apenas aos outros membros online da conversa, sem persistir;
      limitar a frequência por usuário (ex.: no máximo 1 evento a cada 2s).
- [ ] `GET /api/users` e a lista de membros incluem o estado `online` atual.

**Pronto quando:** o cliente consegue mostrar enviada → entregue → lida, presença e "digitando...".

---

## Fase 8 — Sincronização após reconexão

- [ ] Tratar `sincronizar`: para cada conversa informada, buscar `ListarMensagensApos(conversa_id, ultima_sequencia)`
      e enviar como `mensagem.nova` em ordem; conversas do usuário não informadas também são incluídas
      (ex.: grupo criado enquanto estava offline).
- [ ] Limitar a quantidade por conversa (ex.: 500) — além disso o cliente usa a paginação REST.
- [ ] Enviar `sincronizacao.fim` ao terminar.
- [ ] Mensagens ainda na fila de persistência também devem ser consideradas: consultar o banco **e** um buffer em memória
      das mensagens recentes ainda não gravadas, ou forçar a gravação antes da consulta.
- [ ] Teste: cliente desconecta, recebe mensagens enquanto offline, reconecta e recebe todas, em ordem e sem duplicatas.

**Pronto quando:** nenhuma mensagem é perdida em uma queda de conexão.

---

## Fase 9 — Heartbeat e limpeza de conexões

- [ ] Na goroutine de escrita, um `time.Ticker` com `INTERVALO_PING` chama `conn.Ping(ctx)` com timeout `TIMEOUT_PONG`.
- [ ] Se o ping falhar, cancelar o contexto do cliente, fechar a conexão e desregistrar no Hub.
- [ ] Garantir que as duas goroutines do cliente terminem quando qualquer uma delas falhar (contexto compartilhado).
- [ ] Teste: cliente que para de responder é removido do Hub e os contatos recebem `presenca {online: false}`.
- [ ] Verificar ausência de vazamento de goroutines após conexões e desconexões repetidas (`runtime.NumGoroutine`).

**Pronto quando:** conexões mortas são detectadas e liberadas em até `INTERVALO_PING + TIMEOUT_PONG`.

---

## Fase 10 — Graceful shutdown

Ordem de encerramento ao receber `SIGINT`/`SIGTERM`:

- [ ] 1. Parar de aceitar novas conexões e requisições: `http.Server.Shutdown(ctx)` com prazo (ex.: 15s).
- [ ] 2. Enviar `servidor.desligando` a todos os clientes e fechar as conexões com `websocket.StatusGoingAway`.
- [ ] 3. Parar o Hub (cancelar seu contexto) e esperar o laço terminar.
- [ ] 4. Encerrar o pool de persistência, gravando todas as mensagens pendentes.
- [ ] 5. Fechar o banco.
- [ ] Usar um `sync.WaitGroup` para as goroutines de longa duração (Hub, workers, conexões) e `context` para o prazo total.
- [ ] Teste: enviar mensagens e, em seguida, `SIGTERM` — todas devem estar no banco após o encerramento.

**Pronto quando:** desligar o servidor não perde mensagens aceitas (que receberam ACK).

---

## Fase 11 — Qualidade e desempenho

- [ ] `go test -race ./...` sem falhas em todas as fases.
- [ ] Teste de carga em `cmd/carga`: abre N conexões WebSocket (ex.: 1.000–5.000), envia mensagens em grupos e mede
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
