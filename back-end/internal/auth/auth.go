// Package auth issues and validates the authentication credentials of
// the server: it hashes and verifies user passwords with bcrypt and
// generates and validates the JWT access tokens (HS256).
package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLength is the shortest password we accept. It is enforced at
// registration only: raising it later must not lock out existing users.
const MinPasswordLength = 8

// Sentinel errors returned by this package.
var (
	// ErrInvalidCredentials covers both an unknown username and a wrong
	// password. The two are deliberately indistinguishable so the API
	// cannot be used to enumerate which accounts exist.
	ErrInvalidCredentials = errors.New("invalid credentials")

	// ErrInvalidToken means the token was malformed, expired or signed
	// with the wrong key.
	ErrInvalidToken = errors.New("invalid token")

	// ErrWeakPassword means the password failed the length rule.
	ErrWeakPassword = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
)

// Hasher hashes and verifies passwords. It carries the bcrypt cost so
// tests can use the cheapest setting instead of paying for the
// production cost on every run.
type Hasher struct {
	cost int
}

// NewHasher returns a Hasher with the given bcrypt cost. A cost below
// bcrypt.MinCost is clamped, since bcrypt itself rejects it.
func NewHasher(cost int) *Hasher {
	if cost < bcrypt.MinCost {
		cost = bcrypt.MinCost
	}
	if cost > bcrypt.MaxCost {
		cost = bcrypt.MaxCost
	}
	return &Hasher{cost: cost}
}

// DefaultHasher returns a Hasher at bcrypt.DefaultCost, which is the
// right trade-off between resistance to cracking and login latency.
func DefaultHasher() *Hasher {
	return NewHasher(bcrypt.DefaultCost)
}

// HashPassword validates and hashes a plaintext password.
//
// bcrypt truncates at 72 bytes, so a longer password would silently have
// its tail ignored. Rejecting it is safer than accepting a password whose
// effective length is shorter than it looks.
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

// CheckPassword reports whether the plaintext matches the stored hash.
//
// bcrypt.CompareHashAndPassword is constant-time with respect to the
// hash, so it does not leak information through timing.
func (h *Hasher) CheckPassword(hash, password string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return ErrInvalidCredentials
	}
	return nil
}

// TokenIssuer signs and verifies the access tokens.
type TokenIssuer struct {
	secret     []byte
	expiry     time.Duration
	signingAlg string
}

// NewTokenIssuer returns an issuer using HS256. An empty secret is
// rejected here rather than at first use, so a misconfigured server
// cannot start and then accept unverifiable tokens.
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

// GenerateToken returns a signed token for the user.
//
// The subject carries the user ID as a string because the JWT spec
// defines sub as a string; it is converted back on validation.
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

// ValidateToken verifies the signature and expiry and returns the user ID.
//
// The signing method is pinned to HMAC. Without that check a token could
// declare "alg": "none", or name an asymmetric algorithm and have the
// public key used as the HMAC secret — the classic JWT confusion attack.
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

// BearerToken extracts the token from an Authorization header value.
// The scheme is matched case-insensitively because HTTP treats it that
// way, even though clients usually send "Bearer".
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
