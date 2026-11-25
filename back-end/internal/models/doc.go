// Pacote models define os tipos de domínio compartilhados por todo o
// servidor: User, Conversation, Member e Message.
//
// As tags das structs fazem parte do contrato público. Elas são em inglês
// e idênticas em todas as camadas — o mesmo campo se chama
// conversation_id na coluna SQL, no payload REST e no envelope WebSocket
// —, então um valor nunca precisa ser renomeado enquanto atravessa o
// sistema.
package models
