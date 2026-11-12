const USERNAME_PATTERN = /^[a-z0-9_]{3,32}$/;
const MAX_DISPLAY_NAME_BYTES = 64;
const MIN_PASSWORD_BYTES = 8;
const MAX_PASSWORD_BYTES = 72;

export function normalizedUsername(value) {
  return value.trim().toLowerCase();
}

export function usernameError(value) {
  const username = normalizedUsername(value);
  if (!username) return 'Informe o nome de usuário.';
  if (!USERNAME_PATTERN.test(username)) return 'Use de 3 a 32 letras sem acento, números ou _.';
  return '';
}

export function displayNameError(value) {
  const name = value.trim();
  if (!name) return 'Informe como você quer aparecer.';
  if (new TextEncoder().encode(name).length > MAX_DISPLAY_NAME_BYTES) {
    return 'O nome de exibição é muito longo. Use no máximo 64 bytes.';
  }
  return '';
}

export function passwordError(value) {
  if (!value) return 'Informe a senha.';
  const size = new TextEncoder().encode(value).length;
  if (size < MIN_PASSWORD_BYTES) return 'A senha precisa de pelo menos 8 bytes.';
  if (size > MAX_PASSWORD_BYTES) return 'A senha deve ter no máximo 72 bytes.';
  return '';
}
