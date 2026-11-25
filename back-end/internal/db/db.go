package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite" // registra o driver "sqlite"
)

// Open abre o banco SQLite em path e o configura para o padrão de acesso
// deste servidor: muitos leitores simultâneos e poucos escritores.
//
// Os pragmas importam mais do que parecem. Por padrão o SQLite usa
// journal de rollback e deixa as chaves estrangeiras desligadas, o que
// surpreenderia quem vem de outro banco: o WAL deixa os leitores seguirem
// enquanto um escritor segura a trava, e foreign_keys=ON é o que de fato
// faz valer as cláusulas REFERENCES do schema.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	dsn := buildDSN(path)

	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	// O SQLite serializa as escritas, então um pool grande só gera
	// disputa por trava e erros de banco ocupado. Poucas conexões mantêm as
	// leituras simultâneas sem deixar os escritores se acumularem.
	handle.SetMaxOpenConns(4)
	handle.SetMaxIdleConns(4)

	// Ping confere que o arquivo está acessível agora, em vez de deixar a
	// primeira consulta falhar depois num ponto sem relação.
	if err := handle.PingContext(ctx); err != nil {
		handle.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}

	return handle, nil
}

// buildDSN monta a string de conexão com os pragmas aplicados a cada nova
// conexão do pool. Defini-los no DSN, e não com comandos PRAGMA depois de
// abrir, é proposital: pragmas valem por conexão, então um comando só
// configuraria a conexão que estivesse em uso naquele momento.
func buildDSN(path string) string {
	pragmas := url.Values{}
	pragmas.Add("_pragma", "journal_mode(WAL)")
	pragmas.Add("_pragma", "foreign_keys(ON)")
	pragmas.Add("_pragma", "busy_timeout(5000)")
	pragmas.Add("_pragma", "synchronous(NORMAL)")

	return "file:" + path + "?" + pragmas.Encode()
}
