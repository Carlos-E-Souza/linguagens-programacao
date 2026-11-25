// Comando server é o ponto de entrada do back-end do chat.
//
// Ele carrega a configuração do ambiente, conecta os componentes
// (banco, repositórios, API HTTP, hub WebSocket e o worker pool de
// persistência) e roda o servidor HTTP até receber SIGINT ou SIGTERM.
// No encerramento, para de aceitar conexões, avisa os clientes
// conectados, drena a fila de persistência e fecha o banco, para que
// nenhuma mensagem confirmada seja perdida.
//
// Execute com:
//
//	go run ./cmd/server
package main
