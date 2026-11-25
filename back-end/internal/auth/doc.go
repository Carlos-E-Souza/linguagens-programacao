// Pacote auth emite e valida as credenciais de autenticação do servidor:
// gera e confere o hash das senhas com bcrypt e gera e valida os tokens
// de acesso JWT (HS256).
//
// O pacote não lida com HTTP nem com banco, de propósito. Os handlers
// REST decidem quando chamá-lo e o middleware decide como o token
// resultante trafega no header Authorization, o que mantém as regras de
// segurança num só lugar e fáceis de testar.
package auth
