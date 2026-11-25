package db

import (
	"context"
	"database/sql"
	"strings"
)

// Store expõe todas as operações de banco do servidor.
//
// É um invólucro fino sobre *sql.DB em vez de um conjunto de funções
// soltas, para que os handlers dependam de um único valor. Não guarda
// estado mutável, então uma única instância pode ser compartilhada entre
// os handlers REST, o hub e os workers de persistência.
type Store struct {
	db *sql.DB
}

// NewStore envolve uma conexão de banco já aberta.
func NewStore(handle *sql.DB) *Store {
	return &Store{db: handle}
}

// DB devolve a conexão subjacente, para os poucos chamadores que precisam
// rodar a própria transação envolvendo vários repositórios.
func (s *Store) DB() *sql.DB {
	return s.db
}

// isUniqueViolation informa se err é uma falha de restrição UNIQUE.
//
// modernc.org/sqlite não exporta um erro tipado para isso, então a
// checagem é pela mensagem. Ela fica restrita a esta função para que uma
// troca de driver só precise de ajuste aqui.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED")
}

// isForeignKeyViolation informa se err é uma falha de FOREIGN KEY.
func isForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToUpper(err.Error()), "FOREIGN KEY CONSTRAINT FAILED")
}

// txFunc é o corpo de uma transação. Devolver erro desfaz; devolver nil
// confirma.
type txFunc func(tx *sql.Tx) error

// withTx roda fn dentro de uma transação, confirmando em caso de sucesso e
// desfazendo em caso de erro ou panic.
//
// O rollback é adiado (defer) incondicionalmente porque Rollback depois de
// um Commit bem-sucedido devolve sql.ErrTxDone e não faz nada, então o
// caminho de sucesso não é afetado.
func (s *Store) withTx(ctx context.Context, fn txFunc) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit()
}
