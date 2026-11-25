// Comando loadgen mede a vazão e a latência do servidor de chat com
// muitas conexões WebSocket simultâneas.
//
// Ele cadastra os próprios usuários em um servidor em execução, abre uma
// conexão por usuário, faz todos enviarem para um grupo compartilhado e
// informa a distribuição dos tempos de ida e volta dos ACKs, junto com o
// número de goroutines e o heap em uso.
//
// É uma ferramenta de desenvolvimento, fora do binário do servidor: existe
// para que as afirmações do plano sobre concorrência possam ser conferidas
// com números reais em vez de apenas afirmadas.
//
// Uso:
//
//	go run ./cmd/loadgen -server http://localhost:8080 -clients 200 -messages 20
//
// Os valores padrão das flags fazem uma execução pequena, então é seguro
// apontar a ferramenta para um servidor local sem pensar muito. Um cliente
// que não acompanha a entrega é desconectado pelo servidor como
// backpressure, então algumas falhas numa execução longa são esperadas, e
// não um bug.
package main
