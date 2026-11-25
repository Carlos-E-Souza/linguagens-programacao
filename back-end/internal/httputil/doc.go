// Pacote httputil implementa a superfície HTTP do servidor: os handlers
// REST de autenticação, usuários, conversas e mensagens, o endpoint de
// upgrade /ws que entrega a conexão ao hub e os pequenos helpers de JSON
// usados por todos eles.
//
// O roteamento usa http.ServeMux com os padrões de método e curinga do Go
// 1.22+, então uma rota como "GET /api/conversations/{id}/messages" é
// casada e suas variáveis lidas com r.PathValue. WriteJSON, WriteError e
// ReadJSON centralizam o formato das respostas e os limites das
// requisições (MaxBytesReader mais DisallowUnknownFields), o que mantém
// todo endpoint respondendo com o mesmo formato de corpo.
package httputil
