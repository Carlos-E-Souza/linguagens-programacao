# Teste de carga

Simula vários usuários trocando mensagens em conversas diretas e grupos
contra o servidor de chat real. Mede latência e confiabilidade do lado do
cliente, coleta métricas do servidor via pprof e grava um relatório.

## Por que um módulo separado

O teste fica num módulo Go próprio (`go.mod` nesta pasta) para não misturar
código de teste com o projeto. Isso tem uma consequência: o Go não permite
importar pacotes `internal/` nem `package main` de outro módulo, então o
teste **não** reaproveita `back-end/internal/ws` nem o `App` do front-end.
Ele tem um cliente leve próprio, e `internal/protocol` é uma **cópia** do
contrato WebSocket. Toda mudança no protocolo precisa ser repetida lá; os
testes de `internal/protocol` usam frames literais para acusar divergência.

O cliente leve também mede o servidor sem o custo do cache SQLite que o
cliente desktop mantém por usuário.

## Como rodar

```bash
make load-server   # terminal 1: servidor com DEV_MODE, pprof em localhost:6060, banco em /tmp/chat-load.db
make load-test     # terminal 2: 100 usuários por 60 s; relatório em teste-carga/results/<data-hora>/
```

Para passar flags, rode direto: `cd teste-carga && go run . -rate 5 -duration 30s`.

Ao terminar, o terminal mostra o caminho do relatório e uma linha de resumo.
`Ctrl+C` interrompe a carga e ainda grava um relatório parcial.

## Flags

| Flag | Padrão | Descrição |
|---|---|---|
| `-server` | `http://localhost:8080` | URL HTTP do servidor |
| `-pprof` | `http://localhost:6060` | URL do pprof do servidor; vazio desativa |
| `-prefix` | `carga` | prefixo dos usuários (`carga_u000`…) e grupos (`carga_g00`…) |
| `-users` | `100` | usuários simultâneos |
| `-direct-per-user` | `3` | cada usuário `i` conversa com `i+1..i+3` (em anel): 300 conversas diretas |
| `-group-sizes` | `5,5,10,10,20,50` | um grupo por tamanho, membros sorteados pela semente |
| `-rate` | `1` | mensagens por segundo por usuário |
| `-duration` | `60s` | duração da fase de carga |
| `-ramp` | `10s` | tempo para conectar todos os usuários |
| `-drain` | `5s` | espera por ACKs e entregas depois de parar de enviar |
| `-receipts` | `true` | destinatários respondem `message.delivered` e `message.read` (agrupados a cada 50 ms, só o maior `sequence` por conversa) |
| `-seed` | `1` | semente dos grupos e da escolha de conversas |
| `-sample` | `1s` | intervalo de amostragem do pprof |
| `-out` | `results` | pasta dos relatórios |

## Usuários e grupos reaproveitados

Usuários e grupos têm nomes fixos e senha fixa. A primeira execução cria
tudo; as seguintes encontram os usuários (`409 username_taken` não é erro),
reaproveitam as conversas diretas (o servidor já devolve a existente) e
procuram os grupos pelo nome. Cada execução só reenvia mensagens. O
relatório informa quantos usuários e grupos foram criados.

Se um grupo existir com membros diferentes do plano (outra `-seed` ou
outros `-group-sizes`), o teste para com erro. Rode `make load-reset`.

O banco cresce a cada execução, o que pode piorar a latência de gravação.
Para comparar execuções de forma justa, rode `make load-reset` antes de
cada uma (com o servidor parado).

## O que o relatório mede

Cada mensagem é rastreada pelo `client_id`, que volta no ACK e no
`message.new`.

- **ACK:** tempo do envio até o `message.ack`.
- **Entrega:** tempo do envio até o `message.new` chegar a cada
  destinatário, separado em conversas diretas e grupos. O eco que o
  remetente recebe não conta.
- **Perdas:** entregas esperadas (todos os membros da conversa menos o remetente) que
  não chegaram até o fim da drenagem; mensagens sem ACK.
- **Duplicatas:** a mesma mensagem recebida duas vezes pelo mesmo usuário.
- **Violações de ordem:** `sequence` menor ou igual ao último recebido
  naquela conversa.
- **Erros:** frames `error` por código, mais falhas locais
  (`send_failed`, `connect_failed`, `receipt_failed`).
- **Servidor:** goroutines e heap amostrados, mais `cpu.pprof` e
  `heap.pprof` na pasta da execução. Analise com
  `go tool pprof -top cpu.pprof` ou `go tool pprof -http=:0 cpu.pprof`.

Servidor e gerador de carga na mesma máquina disputam a CPU; leve isso em
conta ao ler as latências.

## Testes

```bash
make test-load    # unitários, com -race
# integração, com o servidor de make load-server rodando:
LOAD_TEST_SERVER_URL=http://localhost:8080 LOAD_TEST_PPROF_URL=http://localhost:6060 \
  go test -race -tags loadtest -run TestRunAgainstServer -v .
```
