// Pacote middleware fornece os middlewares HTTP aplicados às rotas do
// servidor: autenticação JWT e log de requisições, além da recuperação que
// transforma um panic numa resposta 500 e numa entrada de log em vez de
// derrubar a conexão.
//
// Authenticate extrai o bearer token do header Authorization, o valida e
// guarda o ID do usuário no contexto da requisição sob um tipo de chave
// não exportado, o que evita colisões com chaves definidas por outros
// pacotes; os handlers o leem de volta com UserFromContext.
package middleware
