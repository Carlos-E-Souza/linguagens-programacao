// Pacote ws define o contrato WebSocket entre o servidor e o cliente
// desktop: o envelope que envolve todo frame e os tipos de payload que
// ele carrega.
//
// Os nomes aqui são um contrato público, compartilhado com o cliente, e
// coincidem com as tags JSON, os campos das requisições REST e as colunas
// do banco. Assim, um valor mantém o mesmo nome da linha SQL até o objeto
// JavaScript, que é o sentido da convenção de nomes do projeto: existe
// exatamente uma grafia para cada conceito.
//
// Vale conhecer duas decisões de projeto antes de mudar este pacote:
//
//   - O envelope carrega o payload como json.RawMessage. Um frame não pode
//     ser decodificado num tipo de payload concreto antes de se conhecer
//     seu Type, então o payload é decodificado num segundo passo pelo hub.
//   - As constantes cliente-servidor e servidor-cliente são declaradas
//     separadamente, para que cada direção liste exatamente os tipos que
//     aceita.
//
// Cliente para servidor: message.send, message.delivered, message.read,
// sync.
//
// Servidor para cliente: message.ack, message.new, message.status,
// presence, conversation.updated, sync.end, error, server.shutdown.
package ws
