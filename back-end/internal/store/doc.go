// Pacote store persiste mensagens de forma assíncrona por meio de um pool
// de goroutines workers.
//
// Gravar no banco dentro do laço do hub faria a entrega esperar o disco,
// então o hub só enfileira a mensagem e retorna. Um número fixo de
// workers (PERSISTENCE_WORKERS) consome essa fila, agrupa as mensagens em
// lotes e grava cada lote numa única transação, o que dilui o custo do
// fsync entre muitas inserções.
//
// Enqueue aplica backpressure em vez de descartar dados: quando a fila
// está cheia, ele bloqueia com timeout. Shutdown fecha a fila e espera os
// workers a drenarem dentro do prazo do contexto, e é assim que o
// desligamento gracioso garante que toda mensagem confirmada está no
// disco antes de o processo sair.
//
// A ordem na tabela não importa, já que a ordem de exibição vem da coluna
// sequence, então os workers podem gravar em paralelo sem coordenação.
package store
