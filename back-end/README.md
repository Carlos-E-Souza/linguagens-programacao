# Servidor de Chat (back-end)

Servidor em Go do sistema de chat em tempo real. Ele cuida da autenticação, do gerenciamento de usuários e conversas,
do roteamento das mensagens em tempo real e da persistência do histórico.

- **API REST** com `net/http` da biblioteca padrão.
- **WebSocket** em `/ws` com [`coder/websocket`](https://github.com/coder/websocket).
- **Autenticação** com JWT ([`golang-jwt/jwt`](https://github.com/golang-jwt/jwt)) e senhas com bcrypt.
- **Banco de dados** SQLite com [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite), escrito em Go puro (não precisa de CGO).
- **Concorrência**: um hub central com `select` e `sync.RWMutex`, duas goroutines (leitura e escrita) por cliente,
  um worker pool para gravar as mensagens no banco, heartbeat com `time.Ticker` e graceful shutdown.

> **Convenção de nomes:** todo o código e todo o contrato de dados usam inglês — pacotes, arquivos, funções,
> variáveis, variáveis de ambiente, tabelas e colunas do banco, endpoints, tipos do envelope WebSocket e chaves JSON.
> A prosa da documentação permanece em português. A tabela de renomeações está em [`PLANO.md`](PLANO.md).

## Estrutura

```
cmd/server/            ponto de entrada (main)
internal/
  config/              leitura das variáveis de ambiente
  auth/                tokens JWT e hash de senhas
  db/                  conexão com o banco, migrações e repositórios
  models/              usuários, conversas, membros e mensagens
  httputil/            handlers da API REST, upgrade do /ws e helpers de JSON
  middleware/          autenticação JWT, logging
  hub/                 hub concorrente que roteia as mensagens
  store/               worker pool de gravação das mensagens
migrations/            scripts SQL do banco
```

Cada pasta de pacote tem um `doc.go` descrevendo sua responsabilidade.

## Endpoints

| Método | Endpoint                                   | Função                                           |
|--------|--------------------------------------------|--------------------------------------------------|
| GET    | `/api/health`                              | Verificação de disponibilidade do servidor        |
| POST   | `/api/auth/register`                       | Cadastro de usuário                              |
| POST   | `/api/auth/login`                          | Login, retorna token JWT                         |
| GET    | `/api/users`                               | Busca de usuários para iniciar conversas         |
| GET    | `/api/conversations`                       | Lista as conversas do usuário com não lidas      |
| POST   | `/api/conversations`                       | Cria conversa individual ou em grupo             |
| POST   | `/api/conversations/{id}/members`          | Adiciona membros a um grupo                      |
| DELETE | `/api/conversations/{id}/members/{userId}` | Remove membro ou sai do grupo                    |
| GET    | `/api/conversations/{id}/messages`         | Histórico paginado de mensagens                  |
| GET    | `/ws`                                      | Abre a conexão WebSocket autenticada             |

Os parâmetros de query e os campos JSON de cada endpoint estão detalhados em [`PLANO.md`](PLANO.md).

## Configuração

As configurações são lidas de variáveis de ambiente. O arquivo `.env.example` lista as disponíveis:

| Variável                 | Padrão      | Descrição                                       |
|--------------------------|-------------|-------------------------------------------------|
| `SERVER_ADDRESS`         | `:8080`     | Endereço em que o servidor escuta               |
| `DATABASE_PATH`          | `./chat.db` | Caminho do arquivo SQLite                       |
| `JWT_SECRET`             | —           | Segredo usado para assinar os tokens JWT        |
| `DEV_MODE`               | `false`     | Tolera `JWT_SECRET` vazio em desenvolvimento    |
| `PERSISTENCE_WORKERS`    | `4`         | Quantidade de workers que gravam as mensagens   |
| `PING_INTERVAL`          | `30s`       | Intervalo do heartbeat do WebSocket             |
| `PONG_TIMEOUT`           | `60s`       | Tempo máximo de espera pelo ping                |
| `TOKEN_EXPIRY`           | `24h`       | Validade do token JWT                           |
| `CLIENT_QUEUE_SIZE`      | `256`       | Buffer do canal de saída por conexão            |
| `PERSISTENCE_QUEUE_SIZE` | `4096`      | Buffer da fila de persistência                  |

As durações (`PING_INTERVAL`, `PONG_TIMEOUT`, `TOKEN_EXPIRY`) exigem unidade explícita: `30s`, `24h`. Um valor como
`30` é rejeitado na inicialização. Fora do modo dev, `JWT_SECRET` vazio também impede a subida do servidor.

```bash
cp .env.example .env
```

## Requisitos

- Go 1.26 ou superior

## Comandos comuns

Todos os comandos são executados dentro de `back-end/`.

```bash
# Executar o servidor
go run ./cmd/server

# Executar carregando as variáveis do .env
set -a && source .env && set +a && go run ./cmd/server

# Gerar o binário em bin/server
go build -o bin/server ./cmd/server

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

## Identificadores do projeto

Referência rápida dos nomes usados neste pacote. A coluna "Onde" indica o arquivo ou contexto em que o
identificador aparece. Para a tabela completa de renomeações (português → inglês), ver [`PLANO.md`](PLANO.md).

| Identificador            | Tipo              | Onde                          | Descrição                                            |
|--------------------------|-------------------|-------------------------------|------------------------------------------------------|
| `SERVER_ADDRESS`         | variável de amb.  | `internal/config`             | Endereço de escuta do `http.Server`                   |
| `DATABASE_PATH`          | variável de amb.  | `internal/config`             | Caminho do arquivo SQLite                             |
| `JWT_SECRET`             | variável de amb.  | `internal/config`             | Chave HS256 que assina os tokens                      |
| `DEV_MODE`               | variável de amb.  | `internal/config`             | Tolera segredo vazio fora de produção                 |
| `PERSISTENCE_WORKERS`    | variável de amb.  | `internal/config`             | Número de goroutines de gravação                      |
| `PING_INTERVAL`          | variável de amb.  | `internal/config`             | Cadência do heartbeat                                 |
| `PONG_TIMEOUT`           | variável de amb.  | `internal/config`             | Prazo do ping                                         |
| `TOKEN_EXPIRY`           | variável de amb.  | `internal/config`             | Validade do JWT                                       |
| `CLIENT_QUEUE_SIZE`      | variável de amb.  | `internal/config`             | Buffer do canal por conexão                           |
| `PERSISTENCE_QUEUE_SIZE` | variável de amb.  | `internal/config`             | Buffer da fila de persistência                        |
| `Config`                 | struct            | `internal/config/config.go`   | Agrupa toda a configuração do servidor                |
| `Load`                   | função            | `internal/config/config.go`   | Lê o ambiente e devolve um `Config` validado          |
| `isDevMode`              | função            | `internal/config/config.go`   | Avalia `DEV_MODE` a cada chamada (testável)           |
| `envOr`                  | função            | `internal/config/config.go`   | Lê string com valor padrão                            |
| `envIntOr`               | função            | `internal/config/config.go`   | Lê inteiro com valor padrão                           |
| `envDurationOr`          | função            | `internal/config/config.go`   | Lê duração e devolve erro se inválida                 |
| `users`                  | tabela            | `migrations/0001_initial.sql` | Contas cadastradas                                    |
| `conversations`          | tabela            | `migrations/0001_initial.sql` | Conversas individuais e em grupo                      |
| `conversation_members`   | tabela            | `migrations/0001_initial.sql` | Vínculo entre usuário e conversa                      |
| `messages`               | tabela            | `migrations/0001_initial.sql` | Histórico de mensagens                                |
| `last_sequence`          | coluna            | `conversations`               | Maior sequência atribuída na conversa                 |
| `last_delivered`         | coluna            | `conversation_members`        | Maior sequência entregue ao membro                    |
| `last_read`              | coluna            | `conversation_members`        | Maior sequência lida pelo membro                      |
| `client_id`              | coluna / payload  | `messages` / `message.send`   | UUID gerado no cliente; evita duplicatas              |
| `sequence`               | coluna / payload  | `messages` / `message.ack`    | Ordem da mensagem dentro da conversa                  |
| `body`                   | coluna / payload  | `messages` / `message.send`   | Conteúdo textual da mensagem                          |
