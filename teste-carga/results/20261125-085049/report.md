# Relatório de teste de carga — 20261125-085049

- Início: 2026-11-25 08:50:49 -03
- Servidor: http://localhost:18090
- Usuários: 100 (prefixo `carga`) · conversas diretas: 300 · grupos: 6 [5 5 10 10 20 50]
- Usuários e grupos: reaproveitados
- Taxa: 1.00 msg/s por usuário · duração 1m0s · rampa 10s · drenagem 5s · recibos true · semente 1

## Resumo

| Métrica | Valor |
|---|---|
| Mensagens enviadas | 6000 |
| Confirmadas (ACK) | 6000 (100.0%) |
| Sem ACK | 0 |
| Vazão média de envio | 100.0 msg/s |
| Entregas esperadas | 30941 |
| Entregas realizadas | 30941 |
| Taxa de entrega | 100.0% |
| Entregas perdidas | 0 |
| Duplicatas | 0 |
| Violações de ordem | 0 |
| Desconexões | 0 |

## Latência

Valores em ms. ACK: envio → `message.ack`. Entrega: envio → `message.new` no destinatário.

| Medida | n | p50 | p90 | p95 | p99 | máx |
|---|---|---|---|---|---|---|
| ACK | 6000 | 107.8 | 188.5 | 199.4 | 213.9 | 236.8 |
| Entrega (direta) | 5183 | 1.1 | 2.0 | 5.0 | 13.9 | 25.2 |
| Entrega (grupo) | 25758 | 2.0 | 3.2 | 5.4 | 14.7 | 169.1 |

## Erros e status

**Erros:** nenhum

**`message.status` recebidos:** `delivered` 863857 · `read` 863857

## Servidor (pprof)

| Métrica | Valor |
|---|---|
| Amostras | 65 |
| Goroutines (mín / máx) | 414 / 442 |
| Heap alocado máx | 12.0 MiB |
| Heap em uso máx | 16.6 MiB |

- `cpu.pprof` — `go tool pprof -top cpu.pprof`
- `heap.pprof` — `go tool pprof -top heap.pprof`

## Linha do tempo

| Janela | Enviadas | Confirmadas | Entregues |
|---|---|---|---|
| 0–5 s | 506 | 499 | 2491 |
| 5–10 s | 500 | 500 | 2587 |
| 10–15 s | 500 | 500 | 2177 |
| 15–20 s | 500 | 500 | 2703 |
| 20–25 s | 500 | 500 | 2798 |
| 25–30 s | 500 | 500 | 2692 |
| 30–35 s | 500 | 499 | 2924 |
| 35–40 s | 500 | 500 | 2565 |
| 40–45 s | 500 | 501 | 2683 |
| 45–50 s | 500 | 500 | 2651 |
| 50–55 s | 500 | 500 | 2449 |
| 55–60 s | 494 | 500 | 2221 |
| 60–65 s | 0 | 1 | 0 |
