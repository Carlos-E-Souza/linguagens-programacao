# Validação da fase 10

Registro de 11/11/2026, no Windows, com o código do commit `02d2799` da `main`.

## Verificações concluídas

- `go test -race ./...` em `front-end/`: passou.
- `npm ci` e `npm run build` em `front-end/frontend/`: passaram.
- `wails build` em `front-end/`: gerou `build/bin/chat-client.exe`.
- Teste `E2E` em `front-end/`, com três clientes sem janela e um servidor real:
  `E2E_SERVER_URL=http://127.0.0.1:18080 go test -tags 'webkit2_41 e2e' -race -run E2E -v .` passou.
  Ele cobre mensagens, estados de entrega e leitura, presença, reconexão e grupos.

## Pendente

- Executar o roteiro com duas janelas de verdade, descrito em `README.md`.
  O teste automático não verifica renderização, foco, interação por mouse/teclado e comportamento visual ao
  desconectar. A fase 10 permanece aberta até esse roteiro ser observado.

## Observação para revisão posterior

`go test -race ./...` em `back-end/` falhou duas vezes em `TestUnresponsiveClientIsRemoved`
(`internal/httputil/ws_integration_test.go`). A falha foi registrada para análise em uma branch de correções;
ela não foi alterada nesta branch de validação do front.
