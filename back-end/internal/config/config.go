// Pacote config carrega a configuração do servidor a partir do ambiente,
// com padrões seguros, e valida o que é obrigatório para rodar.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// devJWTSecret é a chave provisória usada quando DEV_MODE está ligado e
// nenhum JWT_SECRET foi informado. É óbvia de propósito, para nunca ser
// confundida com um segredo real.
const devJWTSecret = "insecure-dev-secret-do-not-use-in-production"

// Config reúne tudo o que o servidor precisa para subir.
// Os campos são exportados (maiúsculos) para que o pacote main os leia.
type Config struct {
	ServerAddress        string        // endereço de escuta HTTP, ex.: ":8080"
	DatabasePath         string        // caminho do arquivo do banco SQLite
	JWTSecret            string        // chave HS256 usada para assinar tokens
	PersistenceWorkers   int           // número de goroutines que gravam mensagens
	PingInterval         time.Duration // cadência do heartbeat do WebSocket
	PongTimeout          time.Duration // tempo máximo de espera pela resposta ao ping
	TokenExpiry          time.Duration // validade do JWT
	ClientQueueSize      int           // buffer do canal de saída de cada conexão
	PersistenceQueueSize int           // buffer da fila de persistência
	MessageLimitChars    int           // tamanho máximo do corpo de uma mensagem
	PprofAddress         string        // endereço de escuta da depuração; vazio desativa
	DevMode              bool          // se DEV_MODE está ligado
}

// isDevMode informa se DEV_MODE está ligado, o que marca uma execução
// local sem arquivo .env, em que um segredo vazio é tolerado. A decisão
// vem de uma variável dedicada em vez de ser deduzida de outros sinais.
func isDevMode() bool {
	return os.Getenv("DEV_MODE") == "true"
}

// Load lê o ambiente e devolve uma Config pronta para uso.
// Um segredo vazio é rejeitado fora do modo dev: falhar cedo é melhor do
// que subir um servidor em que ninguém consegue se autenticar.
func Load() (Config, error) {
	cfg := Config{
		ServerAddress:        envOr("SERVER_ADDRESS", ":8080"),
		DatabasePath:         envOr("DATABASE_PATH", "./chat.db"),
		JWTSecret:            envOr("JWT_SECRET", ""),
		PersistenceWorkers:   envIntOr("PERSISTENCE_WORKERS", 4),
		ClientQueueSize:      envIntOr("CLIENT_QUEUE_SIZE", 256),
		PersistenceQueueSize: envIntOr("PERSISTENCE_QUEUE_SIZE", 4096),
		MessageLimitChars:    envIntOr("MESSAGE_LIMIT_CHARS", 4000),
		PprofAddress:         envOr("PPROF_ADDRESS", ""),
		DevMode:              isDevMode(),
	}

	// As durações são lidas de forma estrita: um erro de digitação como
	// PING_INTERVAL=30 (sem unidade) precisa impedir a inicialização em vez
	// de rodar em silêncio com um valor que o operador nunca escolheu.
	var err error
	if cfg.PingInterval, err = envDurationOr("PING_INTERVAL", 30*time.Second); err != nil {
		return cfg, err
	}
	if cfg.PongTimeout, err = envDurationOr("PONG_TIMEOUT", 60*time.Second); err != nil {
		return cfg, err
	}
	if cfg.TokenExpiry, err = envDurationOr("TOKEN_EXPIRY", 24*time.Hour); err != nil {
		return cfg, err
	}

	if cfg.JWTSecret == "" {
		if !isDevMode() {
			return cfg, fmt.Errorf("JWT_SECRET is required (or set DEV_MODE=true for development)")
		}
		// O modo dev ainda precisa de uma chave não vazia, porque o emissor
		// de tokens se recusa a assinar com segredo vazio. Uma chave fixa
		// provisória mantém as execuções locais funcionando sem fingir
		// segurança.
		cfg.JWTSecret = devJWTSecret
		slog.Warn("DEV_MODE is enabled: using an insecure development JWT secret; tokens are not verifiable across restarts")
	}
	if cfg.PersistenceWorkers < 1 {
		return cfg, fmt.Errorf("PERSISTENCE_WORKERS must be at least 1")
	}
	if cfg.PingInterval <= 0 || cfg.PongTimeout < cfg.PingInterval {
		return cfg, fmt.Errorf("invalid ping/pong intervals")
	}
	if cfg.MessageLimitChars < 1 {
		return cfg, fmt.Errorf("MESSAGE_LIMIT_CHARS must be at least 1")
	}
	// Os endpoints do profiler expõem detalhes internos e não têm
	// autenticação, então são recusados fora do modo dev em vez de serem
	// ativados em silêncio.
	if cfg.PprofAddress != "" && !cfg.DevMode {
		return cfg, fmt.Errorf("PPROF_ADDRESS requires DEV_MODE=true")
	}

	return cfg, nil
}

// envOr devolve o valor da variável indicada ou um valor padrão quando ela
// não está definida (ou está vazia). Vazio conta como não definido porque
// uma string vazia nunca é um valor útil para nenhuma configuração aqui.
func envOr(name string, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// envIntOr lê uma variável inteira. Um valor malformado cai no padrão em
// vez de derrubar a inicialização, mas o problema é registrado para que a
// configuração errada continue visível nos logs do servidor.
func envIntOr(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s=%q is not a valid integer, using %d\n", name, value, fallback)
		return fallback
	}
	return parsed
}

// envDurationOr lê uma variável de duração como "30s" ou "24h", que é
// exatamente o que time.ParseDuration entende. Um número sem unidade
// (como "30") é rejeitado com erro, para que sempre se informe a unidade.
func envDurationOr(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback, fmt.Errorf("%s=%q is not a valid duration (use a unit, e.g. 30s or 24h)", name, value)
	}
	return parsed, nil
}
