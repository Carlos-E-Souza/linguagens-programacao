package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// testHasher usa o custo de bcrypt mais barato: gerar hashes com o custo
// de produção deixaria a suíte lenta sem necessidade.
func testHasher() *Hasher {
	return NewHasher(bcrypt.MinCost)
}

func testIssuer(t *testing.T) *TokenIssuer {
	t.Helper()

	issuer, err := NewTokenIssuer("test-secret-that-is-long-enough", time.Hour)
	if err != nil {
		t.Fatalf("NewTokenIssuer() failed: %v", err)
	}
	return issuer
}

func TestHashPasswordAndCheck(t *testing.T) {
	h := testHasher()

	hash, err := h.HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("HashPassword() failed: %v", err)
	}

	if strings.Contains(hash, "correct horse battery") {
		t.Fatal("the hash contains the plaintext password")
	}
	if err := h.CheckPassword(hash, "correct horse battery"); err != nil {
		t.Errorf("CheckPassword(correct) failed: %v", err)
	}
	if err := h.CheckPassword(hash, "wrong password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("CheckPassword(wrong) error = %v, want ErrInvalidCredentials", err)
	}
}

// TestHashPasswordIsSalted prova que duas senhas idênticas geram hashes
// diferentes, o que torna inútil uma rainbow table.
func TestHashPasswordIsSalted(t *testing.T) {
	h := testHasher()

	first, err := h.HashPassword("same password")
	if err != nil {
		t.Fatalf("HashPassword() failed: %v", err)
	}
	second, err := h.HashPassword("same password")
	if err != nil {
		t.Fatalf("HashPassword() failed: %v", err)
	}

	if first == second {
		t.Error("identical passwords produced identical hashes (no salt)")
	}
}

func TestHashPasswordRejectsShortPassword(t *testing.T) {
	h := testHasher()

	if _, err := h.HashPassword("short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("short password error = %v, want ErrWeakPassword", err)
	}
}

// TestHashPasswordRejectsOverlongPassword protege o limite de 72 bytes do
// bcrypt: além dele o final é ignorado sem aviso, e a senha guardada não
// seria a que o usuário digitou.
func TestHashPasswordRejectsOverlongPassword(t *testing.T) {
	h := testHasher()

	if _, err := h.HashPassword(strings.Repeat("a", 73)); err == nil {
		t.Error("expected an error for a password longer than 72 bytes")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	issuer := testIssuer(t)

	token, err := issuer.GenerateToken(42)
	if err != nil {
		t.Fatalf("GenerateToken() failed: %v", err)
	}

	userID, err := issuer.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken() failed: %v", err)
	}
	if got, want := userID, int64(42); got != want {
		t.Errorf("user id: got %d, want %d", got, want)
	}
}

func TestValidateTokenRejectsExpired(t *testing.T) {
	// Uma expiração negativa não pode ser configurada, então o token é
	// gerado diretamente com um horário no passado.
	issuer, err := NewTokenIssuer("test-secret-that-is-long-enough", time.Hour)
	if err != nil {
		t.Fatalf("NewTokenIssuer() failed: %v", err)
	}

	claims := jwt.RegisteredClaims{
		Subject:   "1",
		IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("test-secret-that-is-long-enough"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	if _, err := issuer.ValidateToken(signed); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expired token error = %v, want ErrInvalidToken", err)
	}
}

func TestValidateTokenRejectsWrongSecret(t *testing.T) {
	issuer := testIssuer(t)

	other, err := NewTokenIssuer("a-completely-different-secret", time.Hour)
	if err != nil {
		t.Fatalf("NewTokenIssuer() failed: %v", err)
	}

	token, err := other.GenerateToken(1)
	if err != nil {
		t.Fatalf("GenerateToken() failed: %v", err)
	}

	if _, err := issuer.ValidateToken(token); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("foreign token error = %v, want ErrInvalidToken", err)
	}
}

// TestValidateTokenRejectsAlgNone é o clássico ataque ao JWT: um token
// que declara "alg": "none" não tem assinatura nenhuma. Fixar o método é
// o que o faz falhar.
func TestValidateTokenRejectsAlgNone(t *testing.T) {
	issuer := testIssuer(t)

	claims := jwt.RegisteredClaims{
		Subject:   "1",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)

	unsigned, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none token: %v", err)
	}

	if _, err := issuer.ValidateToken(unsigned); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("alg=none token error = %v, want ErrInvalidToken", err)
	}
}

// TestValidateTokenRejectsGarbage cobre entrada malformada vinda de um cliente.
func TestValidateTokenRejectsGarbage(t *testing.T) {
	issuer := testIssuer(t)

	for _, raw := range []string{"", "not-a-token", "a.b.c", "Bearer x"} {
		if _, err := issuer.ValidateToken(raw); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("ValidateToken(%q) error = %v, want ErrInvalidToken", raw, err)
		}
	}
}

// TestNewTokenIssuerRejectsEmptySecret protege a regra de falhar cedo do
// plano: um servidor sem segredo não pode subir.
func TestNewTokenIssuerRejectsEmptySecret(t *testing.T) {
	if _, err := NewTokenIssuer("", time.Hour); err == nil {
		t.Error("expected an error for an empty secret")
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		name    string
		header  string
		want    string
		wantErr bool
	}{
		{"standard", "Bearer abc.def.ghi", "abc.def.ghi", false},
		{"lowercase scheme", "bearer abc.def.ghi", "abc.def.ghi", false},
		{"mixed case scheme", "BeArEr abc.def.ghi", "abc.def.ghi", false},
		{"empty header", "", "", true},
		{"scheme only", "Bearer ", "", true},
		{"wrong scheme", "Basic abc.def.ghi", "", true},
		{"no scheme", "abc.def.ghi", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BearerToken(tc.header)
			if (err != nil) != tc.wantErr {
				t.Fatalf("BearerToken() error = %v, wantErr = %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("token: got %q, want %q", got, tc.want)
			}
		})
	}
}
