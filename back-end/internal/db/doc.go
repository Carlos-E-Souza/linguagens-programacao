// Pacote db abre a conexão SQLite, aplica as migrações SQL embutidas e
// expõe os repositórios que leem e gravam os dados do chat.
//
// Open configura a conexão para uso simultâneo (journal WAL, chaves
// estrangeiras ativas e busy timeout) e define um limite de conexões
// adequado ao SQLite. Migrate embute migrations/*.sql e aplica só as
// pendentes dentro de uma transação, registrando-as na tabela
// schema_version.
//
// Os repositórios (usuários, conversas, membros e mensagens) são o único
// código autorizado a usar SQL. Todos recebem um context.Context como
// primeiro argumento e não guardam estado além da conexão, o que os torna
// seguros para compartilhar entre os handlers REST e os workers de
// persistência.
package db
