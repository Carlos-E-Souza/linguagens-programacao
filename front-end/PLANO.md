# Plano de Implementação do Front-end (Cliente Desktop)

Este documento divide a implementação do cliente desktop em fases. O cliente tem duas camadas:

- **Camada Go (Wails)** — `main.go`, `aplicacao.go` e `internal/`: fala com o servidor (REST e WebSocket), mantém a
  reconexão, a fila de mensagens pendentes e o cache local, e emite eventos para a interface com `runtime.EventsEmit`.
- **Interface (Vite + HTML/CSS/JS puros)** — `frontend/`: telas e componentes, sem frameworks. Chama os métodos Go
  expostos pelo Wails (`wailsjs/go/main/Aplicacao`) e escuta os eventos com `EventsOn` (`wailsjs/runtime`).

A interface **nunca** fala diretamente com o servidor: toda comunicação passa pela camada Go.

O protocolo WebSocket (tipos de envelope e campos) está definido em `back-end/PLANO.md`.

**Convenção de nomes:** o protocolo compartilhado — envelope, tipos de evento, chaves JSON, colunas do banco e
variáveis de ambiente — usa inglês em ambos os lados, conforme a decisão registrada em `back-end/PLANO.md`. Os
nomes internos do front-end (métodos expostos, eventos do Wails e módulos JavaScript) ainda aparecem neste documento
em português, porque são a proposta original de interface e a padronização do front-end não faz parte deste escopo;
ao implementá-los, devem seguir a mesma convenção de inglês.

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
| Estado da interface     | Um módulo `estado.js` com store simples (objeto + assinantes), sem bibliotecas                        |
| Estrutura do JS         | Módulos ES (`import`/`export`) empacotados pelo Vite                                                  |

---

## Métodos expostos para a interface (`Aplicacao`)

| Método                                             | Retorno                     | Descrição                                          |
|----------------------------------------------------|-----------------------------|----------------------------------------------------|
| `Cadastrar(nomeUsuario, nomeExibicao, senha)`      | `Usuario`                   | Cadastro no servidor                               |
| `Entrar(nomeUsuario, senha)`                       | `Usuario`                   | Login, abre o cache local e conecta o WebSocket    |
| `RestaurarSessao()`                                | `Usuario` ou `null`         | Usa o token salvo, se ainda válido                 |
| `Sair()`                                           | —                           | Desconecta, apaga o token                          |
| `UsuarioAtual()`                                   | `Usuario`                   | Usuário logado                                     |
| `BuscarUsuarios(termo)`                            | `[]Usuario`                 | Para iniciar conversas e criar grupos              |
| `ListarConversas()`                                | `[]Conversa`                | Do cache, atualizado do servidor em segundo plano  |
| `CriarConversaIndividual(usuarioID)`               | `Conversa`                  | Cria ou devolve a existente                        |
| `CriarGrupo(nome, membrosIDs)`                     | `Conversa`                  | Cria um grupo                                      |
| `AdicionarMembros(conversaID, membrosIDs)`         | —                           | Adiciona membros a um grupo                        |
| `RemoverMembro(conversaID, usuarioID)`             | —                           | Remove membro ou sai do grupo                      |
| `CarregarMensagens(conversaID, antesDaSequencia)`  | `[]Mensagem`                | Cache primeiro; busca no servidor o que faltar     |
| `EnviarMensagem(conversaID, conteudo)`             | `Mensagem` (status pendente)| Coloca na fila e envia se conectado                |
| `MarcarComoLida(conversaID, ateSequencia)`         | —                           | Envia `message.read`                              |
| `InformarDigitando(conversaID, ativo)`             | —                           | Envia `digitando`                                  |
| `EstadoConexao()`                                  | `string`                    | `conectado`, `conectando`, `desconectado`          |

## Eventos emitidos para a interface (`internal/eventos`)

| Evento                 | Dados                                                    |
|------------------------|----------------------------------------------------------|
| `conexao:estado`       | `conectado` / `conectando` / `desconectado`              |
| `mensagem:nova`        | mensagem completa                                        |
| `mensagem:atualizada`  | `client_id`, `id`, `sequence`, `status`                  |
| `mensagem:status`      | `conversation_id`, `user_id`, `up_to_sequence`, `status` |
| `conversa:atualizada`  | conversa com membros e contador de não lidas             |
| `presenca`             | `user_id`, `online`                                      |
| `digitando`            | `conversation_id`, `user_id`, `active`                   |
| `sessao:expirada`      | — (token inválido; a interface volta para o login)       |
| `erro`                 | `mensagem`                                               |

---

## Fase 0 — Preparação

- [ ] Remover o exemplo do template (`Saudar`, logo, `app.css`) e criar a estrutura da interface:
  ```
  frontend/src/
    main.js              ponto de entrada: decide entre tela de login e tela de chat
    estado.js            store da aplicação (usuário, conversas, mensagens, presença, conexão)
    ponte.js             encapsula as chamadas aos métodos Go e o registro dos eventos
    telas/
      login.js           login e cadastro
      chat.js            layout principal (barra lateral + janela de chat)
    componentes/
      lista-conversas.js
      janela-chat.js
      bolha-mensagem.js
      campo-mensagem.js
      modal-nova-conversa.js
      modal-grupo.js
      indicador-conexao.js
    utilitarios/
      dom.js             helpers para criar elementos e escapar texto
      datas.js           formatação de horários
    estilos/
      variaveis.css      cores, espaçamentos, fontes (tema claro/escuro)
      base.css
      login.css
      chat.css
  ```
- [ ] Configuração na camada Go: `SERVER_URL` lida em `NovaAplicacao`, com valor padrão.
- [ ] Ajustar `options.App` em `main.go`: título "Chat", tamanho mínimo da janela, `OnShutdown` para encerrar a conexão.
- [ ] `internal/eventos`: constantes com os nomes dos eventos e structs dos payloads.
- [ ] `modelos` do cliente (pode ficar em `internal/api`): `Usuario`, `Conversa`, `Membro`, `Mensagem`, com as mesmas
      tags JSON do servidor.

**Pronto quando:** `wails dev` abre uma janela vazia com a nova estrutura e sem o exemplo.

---

## Fase 1 — Cliente REST (`internal/api`)

- [ ] `api.Cliente` com `http.Client` (timeout de 10s), URL base e token.
- [ ] Método interno `requisitar(ctx, metodo, caminho, corpo, &resposta)`: serializa JSON, adiciona
      `Authorization: Bearer`, converte respostas de erro em um tipo `APIError{Status, Code, Message}`.
- [ ] Um método por endpoint: `Cadastrar`, `Entrar`, `BuscarUsuarios`, `ListarConversas`, `CriarConversa`,
      `AdicionarMembros`, `RemoverMembro`, `ListarMensagens`.
- [ ] Resposta `401` dispara o evento `sessao:expirada`.
- [ ] Testes com `httptest.NewServer` simulando o servidor.

**Pronto quando:** os métodos funcionam contra o servidor real (Fase 3 do back-end).

---

## Fase 2 — Login e cadastro

- [ ] `Aplicacao.Cadastrar`, `Aplicacao.Entrar`, `Aplicacao.Sair`, `Aplicacao.UsuarioAtual`.
- [ ] `telas/login.js`: formulário com alternância entre "Entrar" e "Criar conta", validação básica, mensagens de erro
      vindas do servidor, botão desabilitado durante a requisição.
- [ ] `main.js`: após o login, troca para a tela de chat; ao receber `sessao:expirada` ou ao sair, volta para o login.
- [ ] Estilo da tela de login.

**Pronto quando:** é possível criar uma conta, entrar e sair pela interface.

---

## Fase 3 — Cache local (`internal/cache`)

- [ ] Abrir um SQLite por usuário em `os.UserConfigDir()/chat-client/` ao entrar; fechar ao sair.
- [ ] Tabelas: `sessao` (token, usuário), `conversas`, `membros`, `mensagens`
      (com `client_id`, `sequencia`, `status`) e `fila_envio`.
- [ ] Funções: `SalvarConversas`, `ListarConversas`, `SalvarMensagens` (upsert por `client_id`/`id`),
      `ListarMensagens(conversaID, antes, limite)`, `UltimaSequencia(conversaID)`, `AtualizarStatus`.
- [ ] `Aplicacao.RestaurarSessao`: lê o token salvo e, se válido, entra direto na tela de chat.
- [ ] Testes com banco em `t.TempDir()`.

**Pronto quando:** reabrir o aplicativo mantém a sessão e mostra as conversas do cache antes de falar com o servidor.

---

## Fase 4 — Lista de conversas e histórico

- [ ] `Aplicacao.ListarConversas`: devolve o cache imediatamente e atualiza do servidor em segundo plano,
      emitindo `conversa:atualizada` para o que mudar.
- [ ] `Aplicacao.CarregarMensagens`: lê do cache; se faltar histórico, busca via `GET /api/conversations/{id}/messages`
      paginado e salva no cache.
- [ ] `telas/chat.js`: layout com barra lateral (lista de conversas) e área da conversa aberta.
- [ ] `componentes/lista-conversas.js`: nome (do grupo ou do outro usuário), prévia da última mensagem, horário,
      contador de não lidas, indicador de presença em conversas individuais; ordenada pela última atividade.
- [ ] `componentes/janela-chat.js`: cabeçalho com nome e membros/presença, lista de mensagens ordenada por `sequencia`,
      rolagem infinita para cima carregando páginas anteriores, rolagem automática para o fim em mensagens novas.
- [ ] `componentes/bolha-mensagem.js`: conteúdo (sempre como texto, nunca `innerHTML`), remetente em grupos, horário
      e ícone de status nas mensagens próprias.

**Pronto quando:** a lista de conversas e o histórico aparecem, com paginação.

---

## Fase 5 — Conexão WebSocket (`internal/clientews`)

- [ ] `clientews.Cliente` com `Conectar(ctx)` usando `websocket.Dial` com o header `Authorization`.
- [ ] Goroutine de leitura: decodifica envelopes e repassa para um canal `Recebidos`.
- [ ] Goroutine de escrita: consome um canal `Enviar` e escreve com timeout.
- [ ] Heartbeat: `time.Ticker` enviando `Ping`; se falhar, considera a conexão morta.
- [ ] Laço de reconexão: ao cair, emitir `conexao:estado = conectando` e tentar novamente com backoff exponencial
      e jitter; ao conectar, emitir `conectado` e disparar a sincronização (Fase 8).
- [ ] Tratar `servidor.desligando` reconectando após um pequeno atraso.
- [ ] Se o handshake falhar com `401`, parar as tentativas e emitir `sessao:expirada`.
- [ ] Encerramento limpo ao sair ou fechar o aplicativo (`OnShutdown`), cancelando o contexto.
- [ ] `componentes/indicador-conexao.js`: faixa "Conectando..." / "Sem conexão" quando não estiver conectado.
- [ ] Testes com um servidor WebSocket em `httptest`: reconexão após queda, parada em `401`.

**Pronto quando:** derrubar e subir o servidor faz o cliente reconectar sozinho.

---

## Fase 6 — Envio de mensagens e fila de pendentes (`internal/fila`)

- [ ] `Aplicacao.EnviarMensagem`: gera `client_id`, salva no cache com status `pending`, coloca na fila
      (`fila_envio` no SQLite, para sobreviver ao fechamento do app), envia se conectado e devolve a mensagem para a
      interface exibir na hora.
- [ ] Ao receber `message.ack`: remover da fila, gravar `id`, `sequencia` e `created_at` no cache, status `sent`,
      emitir `mensagem:atualizada`.
- [ ] Ao receber `erro` com `client_id`: marcar como `failed` (ex.: não é mais membro) e remover da fila.
- [ ] Reenvio: mensagens sem ACK após um timeout (ex.: 10s) são reenviadas; após reconexão, toda a fila é reenviada
      na ordem original. O servidor descarta duplicatas pelo `client_id`.
- [ ] Interface: `componentes/campo-mensagem.js` com Enter para enviar e Shift+Enter para quebrar linha; mensagens
      pendentes aparecem com ícone de relógio; opção de tentar novamente para `failed`.
- [ ] Testes da fila: ordem de reenvio, remoção por ACK, persistência entre reinícios.

**Pronto quando:** mensagens enviadas offline são entregues ao reconectar, sem duplicatas.

---

## Fase 7 — Mensagens recebidas, status, presença e "digitando"

- [ ] Ao receber `message.new`: salvar no cache, emitir `mensagem:nova`, atualizar a conversa (prévia, ordem,
      não lidas) e enviar `message.delivered` ao servidor.
- [ ] Se a conversa estiver aberta e a janela em foco, chamar `MarcarComoLida` com a maior sequência visível;
      ao abrir uma conversa, marcar como lida até a última mensagem.
- [ ] `message.status`: atualizar o status das mensagens próprias até `up_to_sequence`
      (em grupos, "entregue"/"lida" só quando todos os membros confirmarem); ícones ✓ enviada, ✓✓ entregue, ✓✓ azul lida.
- [ ] `presenca`: atualizar o indicador online/offline na lista, no cabeçalho e na lista de membros.
- [ ] `digitando`: no campo de mensagem, enviar `active: true` ao começar a digitar (no máximo a cada 2s) e
      `active: false` após 3s sem digitar ou ao enviar; exibir "Fulano está digitando..." e esconder após 5s sem eventos.
- [ ] Notificação visual (contador e destaque) para mensagens em conversas não abertas.

**Pronto quando:** dois clientes conversam vendo status, presença e "digitando..." em tempo real.

---

## Fase 8 — Sincronização após reconexão

- [ ] Ao conectar, enviar `sync` com `last_sequence` de cada conversa do cache.
- [ ] Processar as `message.new` recebidas como na Fase 7, ignorando as que já estão no cache.
- [ ] Ao receber `sync.end`: recarregar a lista de conversas via REST (para membros e grupos novos)
      e reenviar a fila de pendentes.
- [ ] Detectar buracos na sequência de uma conversa e buscar o trecho faltante via REST.

**Pronto quando:** mensagens recebidas enquanto o cliente estava offline aparecem após reconectar, em ordem.

---

## Fase 9 — Conversas e grupos

- [ ] `componentes/modal-nova-conversa.js`: busca de usuários com debounce (300ms), clique abre/cria conversa individual.
- [ ] `componentes/modal-grupo.js`: nome do grupo, busca e seleção de vários membros, criação.
- [ ] Tela de detalhes do grupo: lista de membros com presença, adicionar membros, remover membro (admin) e sair do grupo.
- [ ] Ao receber `conversa.atualizada`: atualizar cache e interface; se o usuário foi removido, fechar a conversa
      e retirá-la da lista.

**Pronto quando:** é possível criar conversas individuais e grupos e gerenciar seus membros.

---

## Fase 10 — Acabamento e testes

- [ ] Estilo final: tema escuro/claro com variáveis CSS, layout responsivo ao redimensionar a janela, estados vazios
      ("Nenhuma conversa", "Selecione uma conversa").
- [ ] Acessibilidade básica: navegação por teclado, foco visível, `aria-label` nos botões de ícone.
- [ ] Tratamento de erros: mensagens amigáveis para falhas de rede e do servidor.
- [ ] Desempenho: atualizar apenas os elementos alterados do DOM (não recriar a lista inteira a cada evento);
      limitar quantas mensagens ficam no DOM em conversas longas.
- [ ] `go test -race ./...` (com `-tags webkit2_41`) passando.
- [ ] Roteiro de teste manual com dois clientes: login, conversa individual, grupo, queda do servidor, envio offline,
      reconexão, status entregue/lida, presença, "digitando...".
- [ ] `wails build` gerando o binário final; atualizar o `README.md` do front-end com instruções de uso.

---

## Ordem sugerida e dependências com o back-end

| Fase do front-end              | Depende de (back-end)                 |
|--------------------------------|---------------------------------------|
| 0 Preparação                   | —                                     |
| 1 Cliente REST, 2 Login        | Fase 2 (autenticação)                 |
| 3 Cache, 4 Conversas/histórico | Fase 3 (API REST)                     |
| 5 Conexão WebSocket            | Fase 4 (Hub e /ws)                    |
| 6 Envio e fila                 | Fase 5 (envio) e Fase 6 (persistência)|
| 7 Status, presença, digitando  | Fase 7                                |
| 8 Sincronização                | Fase 8                                |
| 9 Conversas e grupos           | Fase 3                                |
| 10 Acabamento                  | Fases 9 e 10 (heartbeat, shutdown)    |

Enquanto o back-end não estiver pronto, a interface pode ser desenvolvida com dados falsos em `ponte.js`.
