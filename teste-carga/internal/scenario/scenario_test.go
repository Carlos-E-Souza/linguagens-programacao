package scenario

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var defaultConfig = Config{Prefix: "carga", Users: 100, DirectPerUser: 3, GroupSizes: []int{5, 5, 10, 10, 20, 50}, Seed: 1}

func TestBuildDefault(t *testing.T) {
	p, err := Build(defaultConfig)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(p.Conversations) != 306 {
		t.Fatalf("conversas = %d, esperado 306", len(p.Conversations))
	}
	if p.Usernames[0] != "carga_u000" || p.Usernames[99] != "carga_u099" {
		t.Fatalf("usernames inesperados: %q, %q", p.Usernames[0], p.Usernames[99])
	}

	pairs := map[[2]int]bool{}
	var groupSizes []int
	groupIndex := 0
	for _, c := range p.Conversations {
		switch c.Kind {
		case "direct":
			if len(c.Members) != 2 || c.Members[0] == c.Members[1] {
				t.Fatalf("direta inválida: %v", c.Members)
			}
			a, b := c.Members[0], c.Members[1]
			if a > b {
				a, b = b, a
			}
			if pairs[[2]int{a, b}] {
				t.Fatalf("par repetido: %d-%d", a, b)
			}
			pairs[[2]int{a, b}] = true
			if c.Name != "" {
				t.Fatalf("direta com nome %q", c.Name)
			}
		case "group":
			seen := map[int]bool{}
			for _, m := range c.Members {
				if m < 0 || m >= 100 || seen[m] {
					t.Fatalf("membro inválido ou repetido %d em %v", m, c.Members)
				}
				seen[m] = true
			}
			if c.Name != GroupName("carga", groupIndex) {
				t.Fatalf("nome = %q, esperado %q", c.Name, GroupName("carga", groupIndex))
			}
			groupIndex++
			groupSizes = append(groupSizes, len(c.Members))
		default:
			t.Fatalf("tipo inesperado %q", c.Kind)
		}
	}
	if !reflect.DeepEqual(groupSizes, []int{5, 5, 10, 10, 20, 50}) {
		t.Fatalf("tamanhos dos grupos = %v", groupSizes)
	}

	for user, convs := range p.ByUser {
		want := []int{}
		for i, c := range p.Conversations {
			for _, m := range c.Members {
				if m == user {
					want = append(want, i)
				}
			}
		}
		got := append([]int{}, convs...)
		sort.Ints(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ByUser[%d] = %v, esperado %v", user, got, want)
		}
	}
}

func TestBuildDeterministic(t *testing.T) {
	a, _ := Build(defaultConfig)
	b, _ := Build(defaultConfig)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("mesma semente gerou planos diferentes")
	}
	other := defaultConfig
	other.Seed = 2
	c, _ := Build(other)
	if reflect.DeepEqual(a.Conversations, c.Conversations) {
		t.Fatal("sementes diferentes geraram os mesmos grupos")
	}
}

func TestBuildRejectsInvalid(t *testing.T) {
	cases := map[string]func(*Config){
		"um usuário":          func(c *Config) { c.Users = 1 },
		"diretas demais":      func(c *Config) { c.DirectPerUser = 100 },
		"grupo pequeno":       func(c *Config) { c.GroupSizes = []int{2} },
		"grupo grande":        func(c *Config) { c.GroupSizes = []int{101} },
		"prefixo maiúsculo":   func(c *Config) { c.Prefix = "Carga" },
		"prefixo muito longo": func(c *Config) { c.Prefix = strings.Repeat("a", 30) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := defaultConfig
			cfg.GroupSizes = append([]int{}, defaultConfig.GroupSizes...)
			mutate(&cfg)
			if _, err := Build(cfg); err == nil {
				t.Fatal("esperava erro")
			}
		})
	}
}

func TestUsernameFormat(t *testing.T) {
	re := regexp.MustCompile(`^[a-z0-9_]{3,32}$`)
	if got := Username("carga", 7); got != "carga_u007" || !re.MatchString(got) {
		t.Fatalf("Username = %q", got)
	}
	if got := GroupName("carga", 3); got != "carga_g03" {
		t.Fatalf("GroupName = %q", got)
	}
}
