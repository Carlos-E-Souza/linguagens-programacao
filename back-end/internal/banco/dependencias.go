package banco

// Imports provisórios mantêm as dependências escolhidas no go.mod até que sejam usadas.
import (
	_ "github.com/coder/websocket"
	_ "github.com/golang-jwt/jwt/v5"
	_ "github.com/google/uuid"
	_ "golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)
