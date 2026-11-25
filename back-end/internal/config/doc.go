// Pacote config carrega e valida a configuração do servidor a partir das
// variáveis de ambiente.
//
// Load lê cada configuração uma vez e devolve uma Config imutável, para
// que o resto do programa nunca chame os.Getenv e exista um único lugar a
// olhar quando um valor estiver errado. Ele falha cedo em configurações
// que seriam inseguras ou sem sentido em execução: JWT_SECRET vazio fora
// do modo dev, menos de um worker de persistência ou um par ping/pong
// inválido. Durações exigem unidade explícita (30s, 24h).
package config
