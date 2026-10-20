# Servidor de Chat (back-end)

Servidor em Go do sistema de chat em tempo real. Ele cuida da autenticação, do gerenciamento de usuários e conversas,
do roteamento das mensagens em tempo real e da persistência do histórico.

- **API REST** com `net/http` da biblioteca padrão.
- **WebSocket** em `/ws` com [`coder/websocket`](https://github.com/coder/websocket).
- **Autenticação** com JWT ([`golang-jwt/jwt`](https://github.com/golang-jwt/jwt)) e senhas com bcrypt.
- **Banco de dados** SQLite com [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite), escrito em Go puro (não precisa de CGO).
- **Concorrência**: um hub central com `select` e `sync.RWMutex`, duas goroutines (leitura e escrita) por cliente,
  um worker pool para gravar as mensagens no banco, heartbeat com `time.Ticker` e graceful shutdown.

## Estrutura

```
cmd/servidor/          ponto de entrada (main)
internal/
  configuracao/        leitura das variáveis de ambiente
  autenticacao/        tokens JWT e hash de senhas
  banco/               conexão com o banco e migrações
  modelos/             usuários, conversas, membros e mensagens
  rotas/               handlers da API REST e upgrade do /ws
  middleware/          autenticação JWT, logging
  hub/                 hub concorrente que roteia as mensagens
  persistencia/        worker pool de gravação das mensagens
migracoes/             scripts SQL do banco
```

## Endpoints

| Método | Endpoint                                   | Função                                           |
|--------|--------------------------------------------|--------------------------------------------------|
| POST   | `/api/auth/register`                       | Cadastro de usuário                              |
| POST   | `/api/auth/login`                          | Login, retorna token JWT                         |
| GET    | `/api/users`                               | Busca de usuários para iniciar conversas         |
| GET    | `/api/conversations`                       | Lista as conversas do usuário com não lidas      |
| POST   | `/api/conversations`                       | Cria conversa individual ou em grupo             |
| POST   | `/api/conversations/{id}/members`          | Adiciona membros a um grupo                      |
| DELETE | `/api/conversations/{id}/members/{userId}` | Remove membro ou sai do grupo                    |
| GET    | `/api/conversations/{id}/messages`         | Histórico paginado de mensagens                  |
| GET    | `/ws`                                      | Abre a conexão WebSocket autenticada             |

## Configuração

As configurações são lidas de variáveis de ambiente. O arquivo `.env.example` lista as disponíveis:

| Variável               | Exemplo     | Descrição                                     |
|------------------------|-------------|-----------------------------------------------|
| `ENDERECO_SERVIDOR`    | `:8080`     | Endereço em que o servidor escuta             |
| `CAMINHO_BANCO`        | `./chat.db` | Caminho do arquivo SQLite                     |
| `SEGREDO_JWT`          | `altere-me` | Segredo usado para assinar os tokens JWT      |
| `WORKERS_PERSISTENCIA` | `4`         | Quantidade de workers que gravam as mensagens |

```bash
cp .env.example .env
```

## Requisitos

- Go 1.26 ou superior

## Comandos comuns

Todos os comandos são executados dentro de `back-end/`.

```bash
# Executar o servidor
go run ./cmd/servidor

# Executar carregando as variáveis do .env
set -a && source .env && set +a && go run ./cmd/servidor

# Gerar o binário em bin/servidor
go build -o bin/servidor ./cmd/servidor

# Executar os testes (com detector de condições de corrida)
go test -race ./...

# Análise estática
go vet ./...

# Formatar o código
gofmt -w .

# Adicionar uma dependência
go get github.com/usuario/pacote@latest

# Limpar e sincronizar o go.mod / go.sum
go mod tidy
```
