# Cliente Desktop do Chat (front-end)

Cliente desktop do chat em tempo real, feito com [Wails](https://wails.io). Tem duas camadas:

- **Camada Go** (`main.go`, `app.go`, `internal/`): fala com o servidor por REST e WebSocket, mantém a reconexão,
  o cache local em SQLite e emite eventos para a interface.
- **Interface** (`frontend/`): HTML, CSS e JavaScript puros (módulos ES empacotados pelo Vite), sem frameworks.
  Nunca fala direto com o servidor: tudo passa pela camada Go.

> **Convenção de nomes:** identificadores em inglês (pacotes, arquivos, funções, eventos, módulos JS, chaves JSON),
> prosa em português (comentários, documentação e textos da interface), conforme `back-end/PLANO.md`. O plano de
> implementação do cliente está em [`PLANO.md`](PLANO.md).

## Estrutura

```
main.go, app.go          App exposta à interface (métodos viram wailsjs/go/main/App.js)
live.go, sync.go         mensagens ao vivo, status, presença e sincronização após reconexão
conversations.go         busca de pessoas, conversas individuais, grupos e membros
internal/
  api/                   cliente REST e modelos (User, Conversation, Member, Message)
  cache/                 SQLite local por usuário: sessão, conversas, membros, mensagens, fila
  events/                nomes e payloads dos eventos emitidos para a interface
  wsclient/              conexão WebSocket com heartbeat e reconexão (backoff exponencial com jitter)
  queue/                 fila de mensagens pendentes: envio em ordem, reenvio sem ACK e após reconexão
frontend/src/
  main.js                ponto de entrada: troca de tela conforme store.screen
  state.js               store (estado imutável + assinantes)
  actions.js             ações: único módulo que chama a ponte e altera o store
  bridge.js              ponte com o Go (wailsjs) e nomes dos eventos
  mock-bridge.js         dados falsos para desenvolver no navegador sem o Wails
  screens/               telas (login, chat)
  components/            componentes (avatar, lista de conversas, janela, bolha, campo de mensagem, indicador de conexão,
                         modal, busca de pessoas, nova conversa, novo grupo, detalhes do grupo)
  utils/                 dom, datas, ícones e regras de apresentação de conversas
  styles/                tokens.css, base.css, login.css, chat.css, dialogs.css
```

## Padrão de projeto da interface

Todo código novo da interface segue estas regras.

**Fluxo unidirecional.** A interface chama uma ação (`actions.js`); a ação chama a ponte (`bridge.js`) e grava o
resultado no store (`state.js`); telas assinam o store e repassam aos componentes a fatia de estado que cada um usa.
Eventos do Go (`bridge.on(EVENTS.x, ...)`) também entram por `actions.js`. Componentes nunca chamam a ponte nem
alteram o store; recebem callbacks (`onSelect`, `onLoadOlder`) nas props.

**Contrato de componente.** Cada componente é uma função `createX(props)` que devolve `{ el, update(props), destroy() }`:

- `el` é criado uma vez; `update` altera só o que mudou (`setText`, `toggleClass`), sem recriar a árvore.
- Listas são indexadas por chave (`id` da conversa, `client_id` da mensagem) e reordenadas com `reconcileChildren`.
- Texto do usuário ou do servidor entra sempre como nó de texto (`h()`/`setText`), **nunca** por `innerHTML`.
  A única marcação inserida é a dos ícones constantes de `utils/icons.js`.

**CSS.** Classes em BEM (`.bloco`, `.bloco__elemento`, `.bloco--modificador`), um bloco por componente. Valores de
cor, espaçamento, raio, fonte e sombra vêm só de variáveis de `styles/tokens.css`; o tema escuro troca apenas as cores
em `prefers-color-scheme: dark`. O atributo `hidden` sempre vence o `display` das classes.

**Design.** Fonte do sistema (o app deve parecer nativo), escala de espaçamento de 4px, cor de destaque azul-petróleo
(`--color-accent`) reservada para ações primárias, não lidas e status "lida". Raios diferentes por hierarquia:
controles `--radius-sm`, cartões `--radius-lg`, bolhas `--radius-bubble` com o canto do remetente reto na primeira
mensagem de cada grupo. Toda tela tem estado vazio, de carregamento e de erro com uma ação para sair dele.
Acessibilidade: foco visível, `aria-label` em botões de ícone, `role="log"` no histórico, `role="alert"` nos erros e
`prefers-reduced-motion` respeitado.

**Textos.** Português, frases curtas em tom direto, com o verbo da ação no botão ("Entrar", "Criar conta"). Erros
dizem o que aconteceu e como resolver; a camada Go já entrega a mensagem pronta para exibir.

## Configuração

| Variável     | Padrão                  | Descrição                                                    |
|--------------|-------------------------|--------------------------------------------------------------|
| `SERVER_URL` | `http://localhost:8080` | Endereço do servidor; o WebSocket usa `ws://`/`wss://` + `/ws` |

O cache local fica em `os.UserConfigDir()/chat-client/<username>.db` (no Linux, `~/.config/chat-client/`; no
Windows, `%AppData%\chat-client\`). O arquivo `last-user` dessa pasta diz qual sessão reabrir: duas instâncias na
mesma máquina precisam de pastas diferentes (`XDG_CONFIG_HOME` no Linux, `APPDATA` no Windows).

## Uso

- **Entrar ou criar conta** na tela inicial. A sessão fica salva: ao reabrir o app, ele entra direto.
- **Nova conversa:** botão **+** ao lado de "Conversas"; busque pelo nome ou `@usuário` e clique na pessoa.
- **Novo grupo:** botão de pessoas ao lado do **+**; dê um nome, escolha as pessoas e clique em "Criar grupo".
- **Detalhes do grupo:** botão **ⓘ** no cabeçalho da conversa. Mostra os membros com presença e papel; qualquer
  membro adiciona pessoas e sai do grupo; só administradores removem outros membros. Remover e sair pedem um
  segundo clique para confirmar.
- **Mensagens:** Enter envia, Shift+Enter quebra a linha. Ícones: relógio (enviando), ✓ (enviada), ✓✓ (entregue),
  ✓✓ azul (lida). Sem conexão, as mensagens ficam pendentes e saem sozinhas quando a conexão volta.

## Comandos

Todos os comandos são executados dentro de `front-end/`. A tag `webkit2_41`, necessária em distribuições que só
oferecem o WebKitGTK 4.1 (como o Ubuntu 24.04+), já está em `build:tags` no `wails.json`; comandos `go` diretos
precisam dela explicitamente.

```bash
# Desenvolvimento com hot reload (abre a janela do app)
SERVER_URL=http://localhost:8080 wails dev

# App com dados falsos, sem servidor (enquanto o back-end não existe); login: qualquer usuário, senha "senha1234"
VITE_MOCK=true wails dev

# Só a interface no navegador, também com dados falsos
cd frontend && npm run dev

# No mock, pelo console (devtools): simular queda e volta da conexão; mensagens com "/falha" são recusadas
mockChat.disconnect()
mockChat.reconnect()
# Mensagem recebida (conversa, texto, remetente) e presença de um contato
mockChat.receive(11, 'Oi', 3)
mockChat.presence(3, true)

# Testes da camada Go
go test -race -tags webkit2_41 ./...

# Regenerar os bindings JS após mudar os métodos de App
wails generate module -tags webkit2_41

# Binário final em build/bin/
wails build
```

## Teste com dois clientes

Três terminais. Use o binário gerado por `wails build`: dois `wails dev` ao mesmo tempo disputam as portas do
servidor de desenvolvimento.

```bash
# Terminal 1 — back-end
cd back-end
DEV_MODE=true DATABASE_PATH=./chat-teste.db go run ./cmd/server

# Terminal 2 — primeiro cliente
cd front-end
wails build
XDG_CONFIG_HOME=/tmp/chat-a SERVER_URL=http://localhost:8080 ./build/bin/chat-client

# Terminal 3 — segundo cliente
cd front-end
XDG_CONFIG_HOME=/tmp/chat-b SERVER_URL=http://localhost:8080 ./build/bin/chat-client
```

No Windows (PowerShell), troque `XDG_CONFIG_HOME=...` por `$env:APPDATA = 'C:\temp\chat-a'` (e `chat-b` na outra
janela) e rode `.\build\bin\chat-client.exe`. Confirme que `http://localhost:8080/api/health` responde antes de
abrir os clientes.

### Roteiro de teste manual

1. **Contas:** em cada janela, "Criar conta" com usuários diferentes (ex.: `ana` e `bruno`, senha de 8+ caracteres).
2. **Conversa individual:** na janela da Ana, **+** → buscar `bruno` → clicar. A conversa aparece nas duas janelas.
3. **Mensagens e status:** Ana envia; a mensagem vai de relógio a ✓ e ✓✓. Com a conversa aberta e a janela do Bruno
   em foco, vira ✓✓ azul. Se a janela do Bruno estiver em outra conversa, ela mostra o contador de não lidas e o
   total no título da janela.
4. **Presença:** Bruno sai (botão de sair); o ponto verde some para a Ana e o cabeçalho mostra "Offline".
5. **Envio offline:** pare o back-end (Ctrl+C no terminal 1). A faixa "Sem conexão" aparece; envie mensagens
   (ficam com relógio). Suba o back-end de novo com o mesmo comando: elas são entregues em ordem, sem duplicatas.
6. **Recebidas offline:** feche a janela do Bruno, envie mensagens pela Ana, reabra o Bruno (entra direto pela
   sessão salva): as mensagens aparecem em ordem e com o contador de não lidas.
7. **Grupo:** Ana cria um grupo com o Bruno (e uma terceira conta, se houver). Abra os detalhes (ⓘ): membros com
   presença, Ana como administradora. Envie mensagens: ✓✓ só quando todos receberem, azul só quando todos lerem.
8. **Membros:** Ana adiciona e remove alguém pelos detalhes; o grupo some da lista de quem foi removido. Bruno tenta
   remover alguém (não aparece o botão) e depois sai pelo "Sair do grupo".

### Teste automático de ponta a ponta

`e2e_test.go` faz o mesmo percurso (sem a janela) com três clientes contra um back-end real: busca, conversa
individual, ACK, entregue/lida, presença, mensagem recebida offline, grupo, permissões, remoção e saída.

```bash
# Terminal 1
cd back-end && DEV_MODE=true DATABASE_PATH=/tmp/e2e.db SERVER_ADDRESS=:18080 go run ./cmd/server
# Terminal 2
cd front-end && E2E_SERVER_URL=http://localhost:18080 go test -tags 'webkit2_41 e2e' -race -run E2E -v .
```

## Execução no Windows (PowerShell)

```powershell
# Janela 1, dentro de back-end/:
$env:DEV_MODE = 'true'
$env:DATABASE_PATH = '.\chat-demo.db'
go run ./cmd/server

# Janela 2, dentro de front-end/:
$env:SERVER_URL = 'http://localhost:8080'
cd frontend
npm ci
cd ..
wails dev
```

`go test ./...` roda os testes de cada módulo quando executado dentro de `front-end/` ou `back-end/`;
`npm run build` roda dentro de `front-end/frontend/`.
