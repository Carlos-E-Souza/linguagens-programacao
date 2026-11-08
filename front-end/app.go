package main

import (
	"context"
	"fmt"
)

// Aplicacao é a estrutura principal exposta para a interface pelo Wails.
type Aplicacao struct {
	ctx context.Context
}

// NovaAplicacao cria uma nova estrutura Aplicacao.
func NovaAplicacao() *Aplicacao {
	return &Aplicacao{}
}

// iniciar é chamado quando a aplicação inicia. O contexto é guardado
// para que os métodos do runtime possam ser chamados.
func (a *Aplicacao) iniciar(ctx context.Context) {
	a.ctx = ctx
}

// Saudar retorna uma saudação para o nome informado.
func (a *Aplicacao) Saudar(nome string) string {
	return fmt.Sprintf("Olá %s, é hora do show!", nome)
}
