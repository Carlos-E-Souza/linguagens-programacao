// Pacote hub é o hub concorrente central do servidor. Ele acompanha todo
// cliente conectado e roteia cada mensagem aos membros da conversa a que
// ela pertence.
//
// Uma única goroutine é dona do estado do hub e conduz o laço de eventos
// em Run, fazendo select nos canais register, unregister, incoming e
// notify. Como só essa goroutine altera os mapas de participação, o
// caminho de escrita não precisa de trava; um sync.RWMutex protege as
// estruturas que outras goroutines leem (como IsOnline, chamado pela API
// REST), e as sequences das mensagens são atribuídas ali, o que elimina
// qualquer corrida na ordenação.
//
// Cada usuário conectado é atendido por duas goroutines, uma de leitura e
// uma de escrita, ligadas por um canal com buffer. Um cliente que não
// acompanha é desconectado em vez de bloquear o hub, então um consumidor
// lento nunca trava a entrega para todos os outros.
package hub
