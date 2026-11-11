# Validação da fase 10

Registro de 11/11/2026, no Windows, com o código do commit `02d2799` da `main`.
Na branch `test/frontend-phase-10`, `npm run build`, `go test -race ./...` e `wails build` foram repetidos
com sucesso após `npm ci` na nova worktree. O teste E2E abaixo foi executado na `main`, cujo código do app é o mesmo.

## Verificações concluídas

- `go test -race ./...` em `front-end/`: passou.
- `npm ci` e `npm run build` em `front-end/frontend/`: passaram.
- `wails build` em `front-end/`: gerou `build/bin/chat-client.exe`.
- Teste `E2E` em `front-end/`, com três clientes sem janela e um servidor real:
  `E2E_SERVER_URL=http://127.0.0.1:18080 go test -tags 'webkit2_41 e2e' -race -run E2E -v .` passou.
  Ele cobre mensagens, estados de entrega e leitura, presença, reconexão e grupos.

## Pendente

- Conferir login e cadastro nas janelas. As duas contas usadas neste teste já tinham sessões locais salvas;
  por isso, abriram diretamente na lista de conversas.
- Conferir recebimento após fechar e reabrir uma das janelas, e remoção de membro do grupo.
  A fase 10 permanece aberta até completar o roteiro manual do `README.md`.

## Teste visual com duas janelas

Em 11/11/2026, foram abertas duas instâncias do executável com pastas `APPDATA` separadas, contas
`codexa1111` e `codexb1111`, e um servidor local de teste:

- A busca encontrou a outra conta, a conversa individual foi criada e apareceu nas duas janelas.
- Uma mensagem recebida mostrou contador de não lidas; ao abrir a conversa, o status no remetente passou de
  entregue para lida.
- Um grupo de duas pessoas apareceu nas duas janelas. Mensagem, contador de não lidas e status lida funcionaram.
  Os detalhes mostraram o administrador e as ações diferentes para administrador e membro.
- Ao parar o servidor, apareceu o aviso de falta de conexão. Uma mensagem enviada nesse intervalo ficou como
  enviando; após reiniciar o servidor, foi entregue e apareceu uma única vez na outra janela.
- Uma terceira conta de teste foi adicionada pelos detalhes do grupo. O total passou para três membros nas duas
  janelas e a conta apareceu como offline. Não foi executada a remoção do membro.
- Observação para investigação: antes da reinicialização, `codexb1111` mostrou `codexa1111` como offline embora
  a janela dele estivesse aberta. Após reconectar, passou a mostrar online. Ainda não foi isolada a causa.

## Observação para revisão posterior

`go test -race ./...` em `back-end/` falhou duas vezes em `TestUnresponsiveClientIsRemoved`
(`internal/httputil/ws_integration_test.go`). A falha foi registrada para análise em uma branch de correções;
ela não foi alterada nesta branch de validação do front.
