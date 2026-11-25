// Pacote auth emite e valida as credenciais de autenticação do servidor:
// gera e confere o hash das senhas com bcrypt e gera e valida os tokens
// de acesso JWT (HS256).
package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLength é o menor tamanho de senha aceito. Só é exigido no
// cadastro: aumentá-lo depois não pode trancar usuários já existentes.
const MinPasswordLength = 8

// Erros sentinela devolvidos por este pacote.
var (
	// ErrInvalidCredentials cobre tanto um username desconhecido quanto uma
	// senha errada. Os dois são indistinguíveis de propósito, para que a
	// API não sirva para descobrir quais contas existem.
	ErrInvalidCredentials = errors.New("invalid credentials")

	// ErrInvalidToken indica token malformado, expirado ou assinado com a
	// chave errada.
	ErrInvalidToken = errors.New("invalid token")

	// ErrWeakPassword indica que a senha não passou na regra de tamanho.
	ErrWeakPassword = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
)

// Hasher gera e confere hashes de senha. Ele guarda o custo do bcrypt
// para que os testes usem o valor mais barato em vez de pagar o custo de
// produção a cada execução.
type Hasher struct {
	cost int
}

// NewHasher devolve um Hasher com o custo de bcrypt informado. Um custo
// abaixo de bcrypt.MinCost é ajustado para o mínimo, já que o bcrypt o
// rejeita.
func NewHasher(cost int) *Hasher {
	if cost < bcrypt.MinCost {
		cost = bcrypt.MinCost
	}
	if cost > bcrypt.MaxCost {
		cost = bcrypt.MaxCost
	}
	return &Hasher{cost: cost}
}

// DefaultHasher devolve um Hasher com bcrypt.DefaultCost, o equilíbrio
// certo entre resistência a quebra e latência do login.
func DefaultHasher() *Hasher {
	return NewHasher(bcrypt.DefaultCost)
}

// HashPassword valida uma senha em texto puro e gera o seu hash.
//
// O bcrypt trunca em 72 bytes, então o final de uma senha mais longa seria
// ignorado sem aviso. Rejeitá-la é mais seguro do que aceitar uma senha
// cujo tamanho efetivo é menor do que parece.
func (h *Hasher) HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLength {
		return "", ErrWeakPassword
	}
	if len(password) > 72 {
		return "", errors.New("password must be at most 72 bytes")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), h.cost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}

	return string(hash), nil
}

// CheckPassword informa se o texto puro corresponde ao hash guardado.
//
// bcrypt.CompareHashAndPassword tem tempo constante em relação ao hash,
// então não vaza informação pelo tempo de resposta.
func (h *Hasher) CheckPassword(hash, password string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return ErrInvalidCredentials
	}
	return nil
}

// TokenIssuer assina e verifica os tokens de acesso.
type TokenIssuer struct {
	secret     []byte
	expiry     time.Duration
	signingAlg string
}

// NewTokenIssuer devolve um emissor que usa HS256. Um segredo vazio é
// rejeitado aqui, e não no primeiro uso, para que um servidor mal
// configurado não consiga subir e depois aceitar tokens inverificáveis.
func NewTokenIssuer(secret string, expiry time.Duration) (*TokenIssuer, error) {
	if secret == "" {
		return nil, errors.New("jwt secret must not be empty")
	}
	if expiry <= 0 {
		return nil, errors.New("token expiry must be positive")
	}

	return &TokenIssuer{
		secret:     []byte(secret),
		expiry:     expiry,
		signingAlg: "HS256",
	}, nil
}

// GenerateToken devolve um token assinado para o usuário.
//
// O subject leva o ID do usuário como string porque a especificação JWT
// define sub como string; ele é convertido de volta na validação.
func (t *TokenIssuer) GenerateToken(userID int64) (string, error) {
	now := time.Now()

	claims := jwt.RegisteredClaims{
		Subject:   fmt.Sprintf("%d", userID),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(t.expiry)),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	signed, err := token.SignedString(t.secret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}

	return signed, nil
}

// ValidateToken confere a assinatura e a expiração e devolve o ID do
// usuário.
//
// O método de assinatura é fixado em HMAC. Sem essa checagem, um token
// poderia declarar "alg": "none", ou indicar um algoritmo assimétrico e
// fazer a chave pública ser usada como segredo HMAC — o clássico ataque
// de confusão de algoritmo do JWT.
func (t *TokenIssuer) ValidateToken(raw string) (int64, error) {
	token, err := jwt.ParseWithClaims(raw, &jwt.RegisteredClaims{},
		func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("%w: unexpected signing method %q", ErrInvalidToken, token.Header["alg"])
			}
			return t.secret, nil
		},
		jwt.WithValidMethods([]string{t.signingAlg}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	claims, ok := token.Claims.(*jwt.RegisteredClaims)
	if !ok || !token.Valid {
		return 0, ErrInvalidToken
	}

	var userID int64
	if _, err := fmt.Sscanf(claims.Subject, "%d", &userID); err != nil {
		return 0, fmt.Errorf("%w: subject %q is not a user id", ErrInvalidToken, claims.Subject)
	}
	if userID <= 0 {
		return 0, fmt.Errorf("%w: subject %q is not a positive user id", ErrInvalidToken, claims.Subject)
	}

	return userID, nil
}

// BearerToken extrai o token do valor de um header Authorization.
// O esquema é comparado sem diferenciar maiúsculas porque o HTTP o trata
// assim, embora os clientes normalmente enviem "Bearer".
func BearerToken(header string) (string, error) {
	const prefix = "bearer "

	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", ErrInvalidToken
	}

	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", ErrInvalidToken
	}

	return token, nil
}
