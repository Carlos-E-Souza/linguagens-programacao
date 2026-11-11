import assert from 'node:assert/strict';
import test from 'node:test';
import { normalizedUsername, usernameError } from './account-validation.js';

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
