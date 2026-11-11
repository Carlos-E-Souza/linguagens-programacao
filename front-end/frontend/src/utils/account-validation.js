const USERNAME_PATTERN = /^[a-z0-9_]{3,32}$/;

export function normalizedUsername(value) {
  return value.trim().toLowerCase();
}

export function usernameError(value) {
  const username = normalizedUsername(value);
  if (!username) return 'Informe o nome de usuário.';
  if (!USERNAME_PATTERN.test(username)) return 'Use de 3 a 32 letras, números ou _.';
  return '';
}
