# Plano de Implementação do Front-end (Cliente Desktop)

Este documento divide a implementação do cliente desktop em fases. O cliente tem duas camadas:

- **Camada Go (Wails)** — `main.go`, `aplicacao.go` e `internal/`: fala com o servidor (REST e WebSocket), mantém a
  reconexão, a fila de mensagens pendentes e o cache local, e emite eventos para a interface com `runtime.EventsEmit`.
- **Interface (Vite + HTML/CSS/JS puros)** — `frontend/`: telas e componentes, sem frameworks. Chama os métodos Go
  expostos pelo Wails (`wailsjs/go/main/App`) e escuta os eventos com `EventsOn` (`wailsjs/runtime`).

A interface **nunca** fala diretamente com o servidor: toda comunicação passa pela camada Go.

O protocolo WebSocket (tipos de envelope e campos) está definido em `back-end/PLANO.md`.

**Convenção de nomes:** identificadores em inglês em ambos os lados — protocolo, chaves JSON, banco, variáveis de
ambiente e também os nomes internos do front-end (métodos expostos, eventos do Wails, pacotes Go e módulos
JavaScript) —, conforme a decisão registrada em `back-end/PLANO.md`. A prosa, os comentários e os textos da interface
ficam em português. Renomeações aplicadas no front-end: `Aplicacao`→`App`, `internal/clientews`→`internal/wsclient`,
`internal/eventos`→`internal/events`, `internal/fila`→`internal/queue`, `ponte.js`→`bridge.js`,
`estado.js`→`state.js`, `telas/`→`screens/`, `componentes/`→`components/`, `utilitarios/`→`utils/`.

O padrão de projeto da interface (fluxo unidirecional, contrato de componente, BEM e tokens) está descrito no
[`README.md`](README.md#padrão-de-projeto-da-interface).

---

## Decisões de projeto

| Tema                    | Decisão                                                                                               |
|-------------------------|-------------------------------------------------------------------------------------------------------|
| Endereço do servidor    | Variável `SERVER_URL` (padrão `http://localhost:8080`); o WS usa o mesmo host com `ws://`/`wss://` |
| Cache local e fila      | SQLite (`modernc.org/sqlite`) em `os.UserConfigDir()/chat-client/<username>.db`                       |
| Token                   | Guardado no cache local para manter a sessão; apagado ao sair                                         |
| IDs das mensagens       | `client_id` = UUID v4 gerado na camada Go no momento do envio                                        |
| Status da mensagem      | `pending` (na fila) → `sent` (ACK) → `delivered` → `read`; `failed` para erro definitivo do servidor  |
| Reconexão               | Backoff exponencial com jitter: 1s, 2s, 4s… até 30s                                                   |
| Estado da interface     | Um módulo `state.js` com store simples (objeto + assinantes), sem bibliotecas                         |
| Estrutura do JS         | Módulos ES (`import`/`export`) empacotados pelo Vite                                                  |

---

## Métodos expostos para a interface (`App`)

| Método                                        | Retorno                     | Descrição                                          | Fase |
|-----------------------------------------------|-----------------------------|----------------------------------------------------|------|
| `Register(username, displayName, password)`   | `User`                      | Cadastro no servidor                               | 2 ✔  |
| `Login(username, password)`                   | `User`                      | Login, abre o cache local e conecta o WebSocket    | 2 ✔  |
| `RestoreSession()`                            | `User` ou `null`            | Usa o token salvo, se ainda válido                 | 3 ✔  |
| `Logout()`                                    | —                           | Desconecta, apaga o token                          | 2 ✔  |
| `CurrentUser()`                               | `User` ou `null`            | Usuário logado                                     | 2 ✔  |
| `SearchUsers(term)`                           | `[]User`                    | Para iniciar conversas e criar grupos              | | 9 ✔  |
| `ListConversations()`                         | `[]Conversation`            | Do cache, atualizado do servidor em segundo plano  | 4 ✔  |
| `CreateDirectConversation(userID)`            | `Conversation`              | Cria ou devolve a existente                        | | 9 ✔  |
| `CreateGroup(name, memberIDs)`                | `Conversation`              | Cria um grupo                                      | | 9 ✔  |
| `AddMembers(conversationID, memberIDs)`       | —                           | Adiciona membros a um grupo                        | | 9 ✔  |
| `RemoveMember(conversationID, userID)`        | —                           | Remove membro ou sai do grupo                      | | 9 ✔  |
| `LoadMessages(conversationID, before)`        | `[]Message`                 | Cache primeiro; busca no servidor o que faltar     | 4 ✔  |
| `SendMessage(conversationID, body)`           | `Message` (status `pending`)| Coloca na fila e envia se conectado                | 6 ✔  |
| `RetryMessage(clientID)`                      | `Message` (status `pending`)| Recoloca na fila uma mensagem `failed`             | 6 ✔  |
| `MarkAsRead(conversationID, upToSequence)`    | —                           | Envia `message.read`                               | 7 ✔  |
| `ConnectionState()`                           | `string`                    | `connected`, `connecting`, `disconnected`          | 5 ✔  |

Os erros devolvidos pelos métodos já vêm com texto amigável em português, pronto para exibir.

## Eventos emitidos para a interface (`internal/events`)

| Evento                 | Dados                                                    |
|------------------------|----------------------------------------------------------|
| `connection:state`     | `connected` / `connecting` / `disconnected`              |
| `message:new`          | mensagem completa                                        |
| `messages:synced`      | lote de mensagens recuperadas após reconexão            |
| `message:updated`      | `conversation_id`, `client_id`, `id`, `sequence`, `status`, `created_at`, `error` (só em `failed`) |
| `message:status`       | `conversation_id`, `user_id`, `up_to_sequence`, `status` — `up_to_sequence` já é a posição comum a todos os outros membros |
| `conversation:updated` | conversa com membros e contador de não lidas (também quando o servidor manda `conversation.updated`) |
| `conversation:removed` | `conversation_id` (o usuário saiu ou foi removido)       |
| `presence`             | `user_id`, `online`                                      |
| `session:expired`      | — (token inválido; a interface volta para o login)       |

---

> **Estado:** as Fases 0–9 estão implementadas, e da Fase 10 falta só executar o roteiro manual com duas janelas
> (descrito no `README.md`). Os testes de unidade usam servidores falsos (`httptest`); `e2e_test.go` (tag `e2e`)
> percorre o mesmo roteiro sem a janela, com três clientes contra o back-end real, e passa. A interface também roda
> com `mock-bridge.js`.

## Fase 0 — Preparação

- [x] Remover o exemplo do template (`Saudar`, logo, `app.css`) e criar a estrutura da interface:
  ```
  frontend/src/
    main.js              ponto de entrada: decide entre tela de login e tela de chat
    state.js             store da aplicação (usuário, conversas, mensagens, conexão)
    actions.js           ações: único módulo que chama a ponte e altera o store
    bridge.js            encapsula as chamadas aos métodos Go e o registro dos eventos
    mock-bridge.js       dados falsos para desenvolver no navegador sem o Wails
    screens/
      login.js           login e cadastro
      chat.js            layout principal (barra lateral + janela de chat)
    components/
      avatar.js
      conversation-list.js
      chat-window.js
      message-bubble.js
      connection-indicator.js
      message-input.js
      modal.js, user-search.js, member-picker.js     (Fase 9)
      new-conversation-modal.js, group-modal.js, group-details.js   (Fase 9)
    utils/
      dom.js             helpers para criar elementos e escapar texto
      dates.js           formatação de horários
      icons.js           ícones SVG constantes
      conversations.js   título, iniciais, ordenação e junção de mensagens
    styles/
      tokens.css         cores, espaçamentos, fontes (tema claro/escuro)
      base.css
      login.css
      chat.css
      dialogs.css        janelas modais (Fase 9)
  ```
- [x] Configuração na camada Go: `SERVER_URL` lida em `NewApp`, com valor padrão.
- [x] Ajustar `options.App` em `main.go`: título "Chat", tamanho mínimo da janela, `OnShutdown` para encerrar a conexão.
- [x] `internal/events`: constantes com os nomes dos eventos e structs dos payloads.
- [x] Modelos do cliente em `internal/api`: `User`, `Conversation`, `Member`, `Message`, com as mesmas
      tags JSON do servidor.

**Pronto quando:** `wails dev` abre uma janela vazia com a nova estrutura e sem o exemplo.

---

## Fase 1 — Cliente REST (`internal/api`)

- [x] `api.Client` com `http.Client` (timeout de 10s), URL base e token.
- [x] Método interno `request(ctx, method, path, body, &out)`: serializa JSON, adiciona
      `Authorization: Bearer`, converte respostas de erro em um tipo `APIError{Status, Code, Message}`.
- [x] Um método por endpoint: `Register`, `Login`, `SearchUsers`, `ListConversations`, `CreateConversation`,
      `AddMembers`, `RemoveMember`, `ListMessages`.
- [x] Resposta `401` dispara o evento `session:expired`.
- [x] Testes com `httptest.NewServer` simulando o servidor.

**Pronto quando:** os métodos funcionam contra o servidor real (Fase 3 do back-end).

---

## Fase 2 — Login e cadastro

- [x] `App.Register`, `App.Login`, `App.Logout`, `App.CurrentUser`.
- [x] `screens/login.js`: formulário com alternância entre "Entrar" e "Criar conta", validação básica, mensagens de erro
      vindas do servidor, botão desabilitado durante a requisição.
- [x] `main.js`: após o login, troca para a tela de chat; ao receber `session:expired` ou ao sair, volta para o login.
- [x] Estilo da tela de login.

**Pronto quando:** é possível criar uma conta, entrar e sair pela interface.

---

## Fase 3 — Cache local (`internal/cache`)

- [x] Abrir um SQLite por usuário em `os.UserConfigDir()/chat-client/` ao entrar; fechar ao sair.
- [x] Tabelas: `session` (token, usuário), `conversations`, `members`, `messages`
      (com `client_id`, `sequence`, `status`) e `send_queue`.
- [x] Funções: `SaveConversations`, `ReplaceConversations`, `ListConversations`, `SaveMessages` (upsert por
      `sender_id`/`client_id`), `ListMessages(conversationID, before, limit)`, `LastSequence(conversationID)`, `UpdateStatus`.
- [x] `App.RestoreSession`: lê o token salvo e, se válido, entra direto na tela de chat.
- [x] Testes com banco em `t.TempDir()`.

**Pronto quando:** reabrir o aplicativo mantém a sessão e mostra as conversas do cache antes de falar com o servidor.

---

## Fase 4 — Lista de conversas e histórico

- [x] `App.ListConversations`: devolve o cache imediatamente e atualiza do servidor em segundo plano,
      emitindo `conversation:updated` para o que mudar e `conversation:removed` para o que sumiu.
- [x] `App.LoadMessages`: lê do cache; se faltar histórico, busca via `GET /api/conversations/{id}/messages`
      paginado e salva no cache.
- [x] `screens/chat.js`: layout com barra lateral (lista de conversas) e área da conversa aberta.
- [x] `components/conversation-list.js`: nome (do grupo ou do outro usuário), prévia da última mensagem, horário,
      contador de não lidas, indicador de presença em conversas individuais; ordenada pela última atividade.
- [x] `components/chat-window.js`: cabeçalho com nome e membros/presença, lista de mensagens ordenada por `sequence`,
      rolagem infinita para cima carregando páginas anteriores, rolagem automática para o fim em mensagens novas.
- [x] `components/message-bubble.js`: conteúdo (sempre como texto, nunca `innerHTML`), remetente em grupos, horário
      e ícone de status nas mensagens próprias.

**Pronto quando:** a lista de conversas e o histórico aparecem, com paginação.

---

## Fase 5 — Conexão WebSocket (`internal/wsclient`)

- [x] `wsclient.Client` com `Run(ctx)` usando `websocket.Dial` com o header `Authorization`.
- [x] Goroutine de leitura: decodifica envelopes e repassa para um canal `Received()`.
- [x] Goroutine de escrita: consome um canal interno alimentado por `Send` e escreve com timeout.
- [x] Heartbeat: `time.Ticker` enviando `Ping`; se falhar, considera a conexão morta.
- [x] Laço de reconexão: ao cair, emitir `connection:state = connecting` na primeira tentativa; se ela falhar,
      emitir `disconnected` ("Sem conexão. Tentando reconectar automaticamente.") e seguir tentando com backoff
      exponencial e jitter, sem voltar a `connecting` a cada tentativa — com o servidor fora do ar, a faixa não fica
      presa em "Conectando…". Ao conectar, emitir `connected` e disparar a sincronização (Fase 8).
- [x] Tratar `server.shutdown` reconectando após um pequeno atraso.
- [x] Se o handshake falhar com `401`, parar as tentativas e emitir `session:expired`.
- [x] Encerramento limpo ao sair ou fechar o aplicativo (`OnShutdown`), cancelando o contexto.
- [x] `components/connection-indicator.js`: faixa "Conectando..." / "Sem conexão" quando não estiver conectado.
- [x] Testes com um servidor WebSocket em `httptest`: reconexão após queda, parada em `401`.

**Pronto quando:** derrubar e subir o servidor faz o cliente reconectar sozinho.

---

## Fase 6 — Envio de mensagens e fila de pendentes (`internal/queue`)

- [x] `App.SendMessage`: gera `client_id`, salva no cache com status `pending`, coloca na fila
      (`send_queue` no SQLite, para sobreviver ao fechamento do app), envia se conectado e devolve a mensagem para a
      interface exibir na hora.
- [x] Ao receber `message.ack`: remover da fila, gravar `id`, `sequence` e `created_at` no cache, status `sent`,
      emitir `message:updated`.
- [x] Ao receber `error` com `client_id`: marcar como `failed` (ex.: não é mais membro) e remover da fila. O motivo
      exibido é traduzido pelo código do erro (`not_a_member`, `conversation_not_found`, `empty_body`,
      `body_too_long`); o texto do servidor, em inglês, só vai para o log. `internal_error` é falha temporária: a
      mensagem continua na fila e é reenviada pela verificação periódica.
- [x] Reenvio: mensagens sem ACK após um timeout (ex.: 10s) são reenviadas; após reconexão, toda a fila é reenviada
      na ordem original. O servidor descarta duplicatas pelo `client_id`.
- [x] Interface: `components/message-input.js` com Enter para enviar e Shift+Enter para quebrar linha; mensagens
      pendentes aparecem com ícone de relógio; opção de tentar novamente para `failed`.
- [x] Testes da fila: ordem de reenvio, remoção por ACK, persistência entre reinícios.

Detalhes da implementação: `queue.Queue` persiste pelo `cache` (mensagem `pending` e entrada em `send_queue` na
mesma transação), envia na ordem e para na primeira falha para nada passar à frente; `ResendAll` roda depois de
`sync.end` a cada conexão e `RetryDue` a cada 2s reenvia o que está há 10s sem ACK. Na interface, um rascunho por conversa,
contador a partir de 3700 caracteres (limite 4000) e "Tentar novamente" abaixo da bolha `failed` (`RetryMessage`).
No mock, `mockChat.disconnect()`/`mockChat.reconnect()` no console simulam a queda, e mensagens começando com
`/falha` são recusadas.

**Pronto quando:** mensagens enviadas offline são entregues ao reconectar, sem duplicatas.

---

## Fase 7 — Mensagens recebidas, status e presença

- [x] Ao receber `message.new`: salvar no cache, emitir `message:new`, atualizar a conversa (prévia, ordem,
      não lidas) e enviar `message.delivered` ao servidor.
- [x] Se a conversa estiver aberta e a janela em foco, chamar `MarkAsRead` com a maior sequência visível;
      ao abrir uma conversa, marcar como lida até a última mensagem.
- [x] `message.status`: atualizar o status das mensagens próprias até `up_to_sequence`
      (em grupos, "entregue"/"lida" só quando todos os membros confirmarem); ícones ✓ enviada, ✓✓ entregue, ✓✓ azul lida.
- [x] `presence`: atualizar o indicador online/offline na lista, no cabeçalho e na lista de membros (detalhes do
      grupo, Fase 9, que acompanham o store ao vivo).
- [x] Notificação visual (contador e destaque) para mensagens em conversas não abertas.

Detalhes da implementação (`live.go` e `internal/cache/status.go`):

- `message.new` fora da sincronização é gravada; se for de outro membro e ainda não estava no cache, soma uma não
  lida e gera `message.delivered`. Gravar e contar a não lida acontecem na mesma transação (`SaveReceived`), então
  a mesma mensagem vinda ao vivo e pelo REST não conta duas vezes. Se não houver buraco desde o cursor da Fase 8,
  ela também avança o cursor; com buraco, `fillGap` busca na hora pelo REST o trecho entre o cursor e a última
  sequência conhecida (uma busca por conversa de cada vez). Mensagem de conversa desconhecida recarrega a lista.
- O status das mensagens próprias é calculado no cache a partir de `last_delivered`/`last_read` dos membros: o
  mínimo entre os outros membros define até onde tudo foi entregue/lido. O mesmo cálculo roda ao salvar conversas
  e histórico do REST e ao gravar ACKs, então o status também sai certo após reconectar.
- `message:status` vai para a interface já com essa posição comum (um evento `delivered` e um `read`), seguido de
  `conversation:updated`. O servidor não envia ao usuário as próprias confirmações, então leituras feitas em outra
  conexão do mesmo usuário só aparecem na próxima atualização da lista pelo REST.
- `message.read` e `message.delivered` não passam pela fila: perdidos numa queda, a leitura é repetida
  pela interface quando a conversa aberta volta com não lidas; uma confirmação de entrega perdida não é repetida,
  mas a leitura posterior também marca a mensagem como entregue no servidor.
- Interface: o total de não lidas aparece no título da janela e o contador da conversa pulsa ao receber mensagem.
  No mock, `mockChat.receive(11, 'Oi', 3)` e `mockChat.presence(3, true)` simulam os eventos; na conversa com
  Bruno, enviar uma mensagem gera entregue, lida e uma resposta.

> A indicação de "digitando" (`SetTyping`, evento `typing`) foi retirada do projeto, no cliente e no servidor.

**Pronto quando:** dois clientes conversam vendo status e presença em tempo real.

---

## Fase 8 — Sincronização após reconexão

- [x] Ao conectar, enviar `sync` com o cursor confirmado de cada conversa do cache.
- [x] Durante o backfill, juntar as `message.new` sem duplicatas; fora dele, a Fase 7 trata as mensagens ao vivo.
- [x] Ao receber `sync.end`: recarregar a lista de conversas via REST (para membros e grupos novos)
      e reenviar a fila de pendentes.
- [x] Detectar buracos na sequência de uma conversa e buscar o trecho faltante via REST — na sincronização
      (`completeSync`) e também com a conexão aberta (`fillGap`, Fase 7).

O backfill é conferido por REST, inclusive quando ultrapassa o limite de 500 mensagens por conversa do WebSocket.
Se o `sync.end` não chegar em 15s (o servidor não o envia quando a fila da conexão enche ou a leitura do banco
falha), a sincronização é encerrada do mesmo jeito: `completeSync` confere tudo pelo REST, e o que chegar depois do
prazo é tratado como mensagem ao vivo. Sem esse prazo, as mensagens ao vivo ficariam presas no buffer.
O cursor persiste no SQLite e só avança depois da faixa completa; ao fim, envia `message.delivered` até a última
mensagem recebida de cada conversa. O teste `e2e_test.go` confere a mensagem recebida offline contra o back-end real.

**Pronto quando:** mensagens recebidas enquanto o cliente estava offline aparecem após reconectar, em ordem.

---

## Fase 9 — Conversas e grupos

- [x] `components/new-conversation-modal.js`: busca de usuários com debounce (300ms), clique abre/cria conversa individual.
- [x] `components/group-modal.js`: nome do grupo, busca e seleção de vários membros, criação.
- [x] Tela de detalhes do grupo: lista de membros com presença, adicionar membros, remover membro (admin) e sair do grupo.
- [x] Ao receber `conversation.updated`: atualizar cache e interface; se o usuário foi removido, fechar a conversa
      e retirá-la da lista.

Detalhes da implementação (`conversations.go`, `internal/cache/conversations.go` e os componentes):

- `SearchUsers`, `CreateDirectConversation`, `CreateGroup`, `AddMembers` e `RemoveMember` traduzem os erros do
  servidor para português. A conversa criada entra no cache e na interface na hora (`storeConversation`).
- `conversation.updated` não traz não lidas, última mensagem nem presença: `ApplyConversationUpdate` troca nome e
  membros e mantém o resto, reaproveitando a presença já conhecida de cada pessoa. Conversa nova entra com o cursor
  de sincronização na última sequência (o histórico anterior vem sob demanda). Sem o usuário entre os membros, a
  conversa é apagada do cache com as mensagens e o que estava na fila para ela.
- Depois de adicionar ou remover membros não há busca extra pelo REST: a resposta dela podia chegar depois de uma
  mudança seguinte e sobrescrever um `conversation.updated` mais novo (o teste de ponta a ponta pegou isso). O
  servidor sempre envia o evento, e a sincronização de cada reconexão atualiza a lista inteira.
- Interface: botões **+** (nova conversa) e de grupo ao lado de "Conversas", e **ⓘ** no cabeçalho dos grupos. As
  janelas usam `<dialog>` (foco preso dentro, Esc e clique fora fecham). Remover membro e sair do grupo pedem um
  segundo clique. Os detalhes acompanham o store e fecham sozinhos se o grupo sumir.
- Mock: `SearchUsers` encontra Bruno, Carla, Diego, Elisa e Fábio; criar, adicionar e remover emitem os mesmos
  eventos da camada Go.

**Pronto quando:** é possível criar conversas individuais e grupos e gerenciar seus membros.

---

## Fase 10 — Acabamento e testes

- [x] Estilo final: tema escuro/claro com variáveis CSS (`tokens.css`, segue o tema do sistema), layout responsivo
      ao redimensionar a janela (`@media` em `chat.css`), estados vazios ("Nenhuma conversa ainda", "Selecione uma
      conversa", "Nenhuma mensagem ainda").
- [x] Acessibilidade básica: navegação por teclado, foco visível (`:focus-visible`), `aria-label` nos botões de ícone.
- [x] Tratamento de erros: mensagens amigáveis para falhas de rede e do servidor (`friendly()` nos métodos de `App`,
      `sendErrorText` para mensagens recusadas).
- [x] Desempenho: atualizar apenas os elementos alterados do DOM, sem recriar a lista inteira a cada evento
      (`reconcileChildren`, itens da lista e bolhas reaproveitados pela chave).
- [x] Desempenho: limitar quantas mensagens ficam no DOM em conversas longas. `chat-window.js` mostra as últimas
      150; rolar até o topo mostra mais 100 de cada vez (primeiro as que já estão na memória, depois páginas do
      servidor), e voltar ao fim corta de novo para 150.
- [x] Desempenho: evitar reler todas as conversas do cache a cada evento. `GetConversation` agora lê só a conversa
      pedida (uma consulta, mais membros e última mensagem); antes chamava `ListConversations` inteira a cada
      mensagem, confirmação e leitura.
- [x] `go test -race ./...` (com `-tags webkit2_41`) passando.
- [ ] Roteiro de teste manual com dois clientes: login, conversa individual, grupo, queda do servidor, envio offline,
      reconexão, status entregue/lida, presença. O roteiro está no `README.md` ("Teste com dois
      clientes"); falta executá-lo com as janelas. O mesmo percurso, sem a janela, roda em `e2e_test.go` e passa.
- [x] `wails build` gerando o binário final (`build/bin/chat-client`); `README.md` com uso, teste com dois clientes
      e roteiro.

Limitação conhecida: as atualizações da lista pelo REST que continuam existindo (ao entrar, ao sincronizar e ao
entrar num grupo que já tinha mensagens) também podem, em raras corridas, gravar um estado anterior a um
`conversation.updated` que chegou enquanto a resposta estava a caminho. A próxima atualização corrige.

---

## Ordem sugerida e dependências com o back-end

| Fase do front-end              | Depende de (back-end)                 |
|--------------------------------|---------------------------------------|
| 0 Preparação                   | —                                     |
| 1 Cliente REST, 2 Login        | Fase 2 (autenticação)                 |
| 3 Cache, 4 Conversas/histórico | Fase 3 (API REST)                     |
| 5 Conexão WebSocket            | Fase 4 (Hub e /ws)                    |
| 6 Envio e fila                 | Fase 5 (envio) e Fase 6 (persistência)|
| 7 Status e presença            | Fase 7                                |
| 8 Sincronização                | Fase 8                                |
| 9 Conversas e grupos           | Fase 3                                |
| 10 Acabamento                  | Fases 9 e 10 (heartbeat, shutdown)    |

Enquanto o back-end não estiver pronto, a interface pode ser desenvolvida com dados falsos em `mock-bridge.js` (`VITE_MOCK=true wails dev` ou `npm run dev` em `frontend/`).
