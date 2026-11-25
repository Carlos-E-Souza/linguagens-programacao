// Pacote scenario monta a topologia do teste de carga: quais usuários
// existem e quais conversas diretas e grupos os ligam.
//
// A topologia é determinística (mesma Config gera o mesmo Plan), o que
// permite reaproveitar usuários e grupos entre execuções.
package scenario

import (
	"errors"
	"fmt"
	"math/rand"
	"regexp"
)

// Tipos de conversa, iguais aos do servidor.
const (
	KindDirect = "direct"
	KindGroup  = "group"
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9_]{3,32}$`)

// Config descreve a topologia desejada.
type Config struct {
	Prefix        string
	Users         int
	DirectPerUser int   // cada usuário i conversa com i+1..i+DirectPerUser (em anel)
	GroupSizes    []int // um grupo por item
	Seed          int64 // sorteio dos membros dos grupos
}

// Conversation é uma conversa planejada; Members são índices de usuário.
type Conversation struct {
	Kind    string
	Name    string // só para grupos
	Members []int
}

// Plan é a topologia completa. ByUser[i] lista os índices em Conversations
// das quais o usuário i participa.
type Plan struct {
	Usernames     []string
	Conversations []Conversation
	ByUser        [][]int
}

// Username devolve o nome fixo do usuário i.
func Username(prefix string, i int) string {
	return fmt.Sprintf("%s_u%03d", prefix, i)
}

// GroupName devolve o nome fixo do grupo i.
func GroupName(prefix string, i int) string {
	return fmt.Sprintf("%s_g%02d", prefix, i)
}

// Build valida a configuração e monta o plano.
func Build(cfg Config) (Plan, error) {
	if cfg.Users < 2 {
		return Plan{}, errors.New("são necessários pelo menos 2 usuários")
	}
	// Com 2*DirectPerUser >= Users, os pares (i, i+k) do anel começam a se repetir.
	if cfg.DirectPerUser < 0 || 2*cfg.DirectPerUser >= cfg.Users {
		return Plan{}, fmt.Errorf("direct-per-user deve ficar entre 0 e %d", (cfg.Users-1)/2)
	}
	for _, size := range cfg.GroupSizes {
		if size < 3 || size > cfg.Users {
			return Plan{}, fmt.Errorf("tamanho de grupo %d fora do intervalo 3..%d", size, cfg.Users)
		}
	}
	if !usernamePattern.MatchString(Username(cfg.Prefix, cfg.Users-1)) {
		return Plan{}, fmt.Errorf("prefixo %q gera usernames inválidos (use [a-z0-9_], até 32 caracteres no total)", cfg.Prefix)
	}

	p := Plan{
		Usernames: make([]string, cfg.Users),
		ByUser:    make([][]int, cfg.Users),
	}
	for i := range p.Usernames {
		p.Usernames[i] = Username(cfg.Prefix, i)
	}
	add := func(c Conversation) {
		idx := len(p.Conversations)
		p.Conversations = append(p.Conversations, c)
		for _, m := range c.Members {
			p.ByUser[m] = append(p.ByUser[m], idx)
		}
	}

	for i := 0; i < cfg.Users; i++ {
		for k := 1; k <= cfg.DirectPerUser; k++ {
			add(Conversation{Kind: KindDirect, Members: []int{i, (i + k) % cfg.Users}})
		}
	}

	rng := rand.New(rand.NewSource(cfg.Seed))
	for g, size := range cfg.GroupSizes {
		members := rng.Perm(cfg.Users)[:size]
		add(Conversation{Kind: KindGroup, Name: GroupName(cfg.Prefix, g), Members: members})
	}
	return p, nil
}
