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
  components/            componentes (avatar, lista de conversas, janela, bolha, campo de mensagem, indicador de conexão)
  utils/                 dom, datas, ícones e regras de apresentação de conversas
  styles/                tokens.css, base.css, login.css, chat.css
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

O cache local fica em `os.UserConfigDir()/chat-client/<username>.db` (no Linux, `~/.config/chat-client/`).

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

# Testes da camada Go
go test -race -tags webkit2_41 ./...

# Regenerar os bindings JS após mudar os métodos de App
wails generate module -tags webkit2_41

# Binário final em build/bin/
wails build
```

## Execução no Windows (PowerShell)

Use duas janelas. O cliente precisa da branch do back-end `feat/phase-7-11-polish` em outra pasta de trabalho,
pois `main` ainda não contém o servidor completo.

```powershell
# Janela 1, dentro do back-end da branch acima:
$env:DEV_MODE = 'true'
$env:DATABASE_PATH = '.\chat-demo.db'
go run ./cmd/server

# Janela 2, dentro de front-end/ nesta branch:
$env:SERVER_URL = 'http://localhost:8080'
cd frontend
npm ci
cd ..
wails dev
```

Confirme que `http://localhost:8080/api/health` responde antes de abrir o cliente. `go test ./...` roda os testes
de cada módulo quando executado dentro de `front-end/` ou `back-end/`; `npm run build` roda dentro de
`front-end/frontend/`.

A Fase 8 desta branch recupera mensagens após reconexão. O tratamento de mensagens ao vivo, presença e digitação
pertence à Fase 7, que será integrada depois. A Fase 9 ainda precisa acrescentar criação de conversas na interface.
Até lá, uma conversa para testes reais deve ser criada pela API. O roteiro completo com duas instâncias depende da
integração dessas fases.
