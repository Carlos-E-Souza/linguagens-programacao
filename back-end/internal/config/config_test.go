package config

import (
	"testing"
	"time"
)

// TestLoadDefaults confere que todo campo cai no padrão documentado
// quando o ambiente está completamente vazio.
// t.Setenv define temporariamente uma variável de ambiente e restaura o
// valor anterior automaticamente ao fim do teste; ele também marca o
// teste como não paralelizável, o que não é problema aqui porque só uma
// goroutine mexe no ambiente por vez.
func TestLoadDefaults(t *testing.T) {
	// Aponta o carregador para um ambiente vazio e controlado.
	t.Setenv("SERVER_ADDRESS", "")
	t.Setenv("DATABASE_PATH", "")
	t.Setenv("JWT_SECRET", "")
	t.Setenv("PERSISTENCE_WORKERS", "")
	t.Setenv("DEV_MODE", "true") // tolera o segredo vazio neste teste

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	// Pares got/want: o relatório de divergência idiomático, para a linha
	// de falha mostrar os dois valores de uma vez.
	if got, want := cfg.ServerAddress, ":8080"; got != want {
		t.Errorf("ServerAddress: got %q, want %q", got, want)
	}
	if got, want := cfg.DatabasePath, "./chat.db"; got != want {
		t.Errorf("DatabasePath: got %q, want %q", got, want)
	}
	if got, want := cfg.PersistenceWorkers, 4; got != want {
		t.Errorf("PersistenceWorkers: got %d, want %d", got, want)
	}
	if got, want := cfg.PingInterval, 30*time.Second; got != want {
		t.Errorf("PingInterval: got %v, want %v", got, want)
	}
	if got, want := cfg.PongTimeout, 60*time.Second; got != want {
		t.Errorf("PongTimeout: got %v, want %v", got, want)
	}
	if got, want := cfg.TokenExpiry, 24*time.Hour; got != want {
		t.Errorf("TokenExpiry: got %v, want %v", got, want)
	}
	if got, want := cfg.ClientQueueSize, 256; got != want {
		t.Errorf("ClientQueueSize: got %d, want %d", got, want)
	}
	if got, want := cfg.PersistenceQueueSize, 4096; got != want {
		t.Errorf("PersistenceQueueSize: got %d, want %d", got, want)
	}
}

// TestLoadDevModeSuppliesSecret cobre a lacuna entre "DEV_MODE tolera um
// segredo ausente" e "o emissor de tokens recusa um segredo vazio": sem
// uma chave provisória o servidor subiria e depois falharia.
func TestLoadDevModeSuppliesSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("DEV_MODE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() in dev mode returned an error: %v", err)
	}
	if cfg.JWTSecret == "" {
		t.Error("dev mode must supply a non-empty placeholder secret")
	}
}

// TestPprofRequiresDevMode protege os endpoints do profiler: eles expõem
// detalhes internos e não têm autenticação, então ativá-los numa
// configuração de produção precisa ser recusado em vez de obedecido em
// silêncio.
func TestPprofRequiresDevMode(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("DEV_MODE", "false")
	t.Setenv("PPROF_ADDRESS", ":6060")

	if _, err := Load(); err == nil {
		t.Error("expected an error when PPROF_ADDRESS is set outside dev mode")
	}
}

// TestPprofAllowedInDevMode cobre o caminho de desenvolvimento.
func TestPprofAllowedInDevMode(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("DEV_MODE", "true")
	t.Setenv("PPROF_ADDRESS", "127.0.0.1:6060")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if got, want := cfg.PprofAddress, "127.0.0.1:6060"; got != want {
		t.Errorf("PprofAddress: got %q, want %q", got, want)
	}
	if !cfg.DevMode {
		t.Error("DevMode should be reported as true")
	}
}

// TestPprofDisabledByDefault documenta que um endereço não definido
// significa que nenhum servidor de depuração é iniciado.
func TestPprofDisabledByDefault(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("DEV_MODE", "false")
	t.Setenv("PPROF_ADDRESS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.PprofAddress != "" {
		t.Errorf("PprofAddress should be empty by default, got %q", cfg.PprofAddress)
	}
}

// TestLoadRejectsEmptySecretOutsideDevMode protege a regra de falhar cedo:
// sem DEV_MODE, um JWT_SECRET vazio precisa impedir o servidor de subir
// com uma configuração inutilizável.
func TestLoadRejectsEmptySecretOutsideDevMode(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("DEV_MODE", "false")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with empty JWT_SECRET and DEV_MODE=false must return an error")
	}
}

// TestLoadInvalidIntervals exercita o ramo de validação de ping/pong com
// uma tabela de combinações inválidas. O teste orientado a tabela mantém
// os casos como dados: acrescentar um é uma linha, e não mais uma cópia
// do corpo do teste.
func TestLoadInvalidIntervals(t *testing.T) {
	cases := []struct {
		name         string
		pingInterval string
		pongTimeout  string
		expectError  bool
	}{
		{"zero ping", "0s", "60s", true},
		{"pong before ping", "30s", "20s", true},
		{"unparseable ping", "not-a-duration", "60s", true},
		{"valid pair", "10s", "20s", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PING_INTERVAL", tc.pingInterval)
			t.Setenv("PONG_TIMEOUT", tc.pongTimeout)
			t.Setenv("JWT_SECRET", "test-secret") // evita o caminho do modo dev
			t.Setenv("DEV_MODE", "false")

			_, err := Load()
			if (err != nil) != tc.expectError {
				t.Errorf("Load() error = %v, want error: %v", err, tc.expectError)
			}
		})
	}
}
