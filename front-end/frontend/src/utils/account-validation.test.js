import assert from 'node:assert/strict';
import test from 'node:test';
import { displayNameError, normalizedUsername, passwordError, usernameError } from './account-validation.js';

test('nome de usuário é normalizado como no servidor', () => {
  assert.equal(normalizedUsername('  Ana_12  '), 'ana_12');
  assert.equal(usernameError('  Ana_12  '), '');
});

test('nome de usuário fora do formato recebe erro antes do cadastro', () => {
  assert.equal(usernameError('a'.repeat(32)), '');
  assert.equal(usernameError('a'.repeat(33)), 'Use de 3 a 32 letras, números ou _.');
  assert.equal(usernameError('ab'), 'Use de 3 a 32 letras, números ou _.');
  assert.equal(usernameError('ana silva'), 'Use de 3 a 32 letras, números ou _.');
  assert.equal(usernameError('joão'), 'Use de 3 a 32 letras, números ou _.');
  assert.equal(usernameError(''), 'Informe o nome de usuário.');
});

test('nome de exibição respeita o limite de 64 bytes do servidor', () => {
  assert.equal(displayNameError('  Ana Silva  '), '');
  assert.equal(displayNameError('a'.repeat(64)), '');
  assert.equal(displayNameError('a'.repeat(65)), 'O nome de exibição é muito longo. Use no máximo 64 bytes.');
  assert.equal(displayNameError('á'.repeat(32)), '');
  assert.equal(displayNameError('á'.repeat(33)), 'O nome de exibição é muito longo. Use no máximo 64 bytes.');
  assert.equal(displayNameError('  '), 'Informe como você quer aparecer.');
});

test('senha exige entre 8 e 72 bytes como o servidor', () => {
  assert.equal(passwordError('senha123'), '');
  assert.equal(passwordError('a'.repeat(72)), '');
  assert.equal(passwordError('a'.repeat(73)), 'A senha deve ter no máximo 72 bytes.');
  assert.equal(passwordError('á'.repeat(8)), '');
  assert.equal(passwordError('á'.repeat(4)), '');
  assert.equal(passwordError('á'.repeat(3)), 'A senha precisa de pelo menos 8 bytes.');
  assert.equal(passwordError('short'), 'A senha precisa de pelo menos 8 bytes.');
  assert.equal(passwordError(''), 'Informe a senha.');
});
