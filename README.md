# linguagens-programacao
Repositorio para o desenvolvimento do projeto da disciplina de Linguagens de Programação da UnB. O projeto tem como objetivo desenvolver uma aplicação em GO utilizando seus recursos caracteristicos.

# Chat em Tempo Real com Go e WebSockets

## Definição

Sistema de mensagens instantâneas composto por duas aplicações independentes: um servidor em Go, que expõe uma API REST e um endpoint WebSocket, e um cliente desktop construído com Wails, cuja interface é feita em HTML, CSS e JavaScript puros (sem frameworks), empacotada com Vite. O sistema permite que muitos usuários conectados simultaneamente troquem um grande volume de mensagens em conversas individuais (1:1) e em grupo, com baixa latência, baixo consumo de memória e CPU, e garantias de entrega e ordenação mesmo diante de quedas de conexão.

## Domínio de Aplicação

Comunicação em tempo real e aplicações de alta concorrência.

## Usuários Característicos

Equipes, comunidades e plataformas que precisam trocar mensagens instantaneamente, de forma privada ou em grupos.

## Arquitetura

### Aplicação 1: Servidor (Go)

Responsável por autenticação, gerenciamento de usuários e conversas, roteamento de mensagens em tempo real e persistência do histórico. Utiliza `net/http` da biblioteca padrão para a API REST e a biblioteca [`coder/websocket`](https://github.com/coder/websocket) para o canal em tempo real. O histórico é armazenado em um banco de dados SQLite, por meio do driver [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite), escrito em Go puro.

| Método | Endpoint                                   | Função                                                  |
|--------|--------------------------------------------|---------------------------------------------------------|
| GET    | `/api/health`                              | Verificação de disponibilidade do servidor              |
| POST   | `/api/auth/register`                       | Cadastro de usuário                                     |
| POST   | `/api/auth/login`                          | Login, retorna token JWT                                |
| GET    | `/api/users`                               | Busca de usuários para iniciar conversas                |
| GET    | `/api/conversations`                       | Lista as conversas do usuário com contador de não lidas |
| POST   | `/api/conversations`                       | Cria conversa individual ou em grupo                    |
| POST   | `/api/conversations/{id}/members`          | Adiciona membros a um grupo                             |
| DELETE | `/api/conversations/{id}/members/{userId}` | Remove membro ou sai do grupo                           |
| GET    | `/api/conversations/{id}/messages`         | Histórico paginado de mensagens                         |
| GET    | `/ws`                                      | Abre a conexão WebSocket autenticada                    |

### Aplicação 2: Cliente Desktop (Wails + Vite + Vanilla JS)

A camada Go do Wails mantém a conexão WebSocket com o servidor, cuida da reconexão automática, da fila de mensagens pendentes e do cache local, e repassa os eventos para a interface por meio do sistema de eventos do Wails (`runtime.EventsEmit`). A interface, em HTML, CSS e JavaScript puros, exibe a lista de conversas, a janela de chat, a criação de grupos, os indicadores de presença (online/offline), “digitando...” e o status de cada mensagem (enviada, entregue, lida).

## Convenção de nomes

O projeto adota **um único idioma para identificadores: o inglês**. Vale para nomes de pacotes, arquivos, funções,
variáveis, variáveis de ambiente, tabelas e colunas do banco, endpoints, tipos do envelope WebSocket e chaves JSON.
Assim um mesmo dado mantém o mesmo nome desde a coluna do SQL até o objeto JavaScript, sem traduções pelo caminho.
A **prosa da documentação permanece em português** — os planos, este README e os comentários explicativos são
escritos em português, citando os identificadores em inglês.

| Camada                  | Exemplo                                                                        |
|-------------------------|--------------------------------------------------------------------------------|
| Pacotes e arquivos      | `cmd/server`, `internal/db`, `internal/httputil`, `migrations/`                |
| Funções e tipos         | `config.Load`, `auth.GenerateToken`, `hub.Hub`, `store.Pool`                   |
| Banco de dados          | `users`, `conversations`, `conversation_members`, `messages`, `last_read`       |
| Envelope WebSocket      | `{"type": "message.send", "data": {...}}`                                      |
| Tipos de evento WS      | `message.ack`, `message.new`, `message.status`, `presence`, `typing`, `sync`   |
| Variáveis de ambiente   | `SERVER_ADDRESS`, `DATABASE_PATH`, `JWT_SECRET`, `PERSISTENCE_WORKERS`         |

A tabela completa de renomeações (nome antigo em português → nome novo em inglês) está em
[`back-end/PLANO.md`](back-end/PLANO.md).

## Recursos do Go Explorados

- **Goroutines:** cada cliente conectado é atendido por duas goroutines dedicadas, uma de leitura e outra de escrita. Por ocuparem poucos kilobytes de pilha e serem escalonadas pelo runtime do Go, e não pelo sistema operacional, o servidor suporta milhares de conexões simultâneas com consumo mínimo de recursos.
- **Channels:** toda a comunicação entre goroutines ocorre por channels, seguindo o princípio do Go de compartilhar memória comunicando. Channels com buffer em cada cliente absorvem picos de mensagens e permitem desconectar clientes lentos sem travar os demais.
- **Select:** o laço central do servidor (Hub) usa `select` para tratar, de forma não bloqueante, eventos de registro, desconexão e envio de mensagens.
- **Hub concorrente:** uma estrutura centralizada mantém os usuários conectados e as conversas ativas, protegida por `sync.RWMutex`, permitindo muitas leituras simultâneas e escritas seguras.
- **Worker pool:** a gravação das mensagens no banco é feita por um conjunto fixo de goroutines que consomem uma fila, de modo que a persistência não atrasa a entrega em tempo real.
- **Context:** `context.Context` propaga cancelamento e timeouts entre requisições, conexões e workers.
- **Tickers e heartbeat:** `time.Ticker` envia pings periódicos para detectar conexões mortas e liberar seus recursos.
- **Graceful shutdown:** `sync.WaitGroup` e captura de sinais do sistema garantem que, ao desligar o servidor, as mensagens pendentes sejam gravadas e os clientes avisados.
- **Biblioteca padrão HTTP:** `net/http` fornece servidor HTTP robusto e concorrente sem necessidade de frameworks externos.

## Confiabilidade da Comunicação

O WebSocket opera sobre TCP, que já trata perda de pacotes, retransmissão, ordenação e verificação de integridade em nível de transporte. O projeto complementa isso na camada de aplicação: cada mensagem recebe um identificador único gerado no cliente e o servidor responde com uma confirmação (ACK); mensagens sem ACK permanecem na fila do cliente e são reenviadas após reconexão; o identificador único evita duplicatas no servidor; e números de sequência por conversa garantem a ordem de exibição. Ao reconectar, o cliente solicita as mensagens recebidas durante o período offline.

## Funcionamento do Programa

1. O usuário abre o cliente desktop e faz login; o servidor retorna um token JWT.
2. O cliente abre a conexão WebSocket enviando o token; o servidor valida, registra o usuário no Hub e cria suas goroutines de leitura e escrita.
3. O servidor notifica os contatos do usuário de que ele está online, e o cliente carrega a lista de conversas e seus históricos via API REST.
4. O usuário cria ou seleciona uma conversa, individual ou em grupo.
5. O usuário A digita “Olá, pessoal!” em um grupo e envia pelo WebSocket.
6. O servidor recebe a mensagem, adiciona remetente, horário e número de sequência, responde ao usuário A com um ACK e coloca a mensagem na fila de persistência.
7. O Hub entrega cópias da mensagem apenas aos membros daquela conversa que estão conectados; os membros offline a recebem ao se reconectar.
8. Os clientes destinatários confirmam o recebimento, e o status da mensagem é atualizado para “entregue” e, depois, “lida”.
9. Ao fechar o aplicativo ou perder a conexão, o servidor detecta a saída (pelo fechamento do socket ou pela falha no heartbeat), remove o usuário do Hub, encerra suas goroutines e avisa os contatos de que ele ficou offline.
