import test from 'node:test';
import assert from 'node:assert/strict';
import { highlightParts } from './search.js';

const join = (parts) => parts.map((p) => p.text).join('');

test('sem termo devolve o texto inteiro, sem destaque', () => {
  assert.deepEqual(highlightParts('Bom dia', ''), [{ text: 'Bom dia', match: false }]);
  assert.deepEqual(highlightParts('Bom dia', '   '), [{ text: 'Bom dia', match: false }]);
});

test('texto vazio devolve lista vazia', () => {
  assert.deepEqual(highlightParts('', 'oi'), []);
});

test('marca a ocorrência e preserva o resto', () => {
  assert.deepEqual(highlightParts('Bom dia, tudo bem?', 'dia'), [
    { text: 'Bom ', match: false },
    { text: 'dia', match: true },
    { text: ', tudo bem?', match: false },
  ]);
});

test('ignora maiúsculas e preserva a grafia original', () => {
  assert.deepEqual(highlightParts('REUNIÃO adiada', 'reunião'), [
    { text: 'REUNIÃO', match: true },
    { text: ' adiada', match: false },
  ]);
});

test('marca todas as ocorrências, inclusive coladas', () => {
  const parts = highlightParts('ababab', 'ab');
  assert.deepEqual(parts.filter((p) => p.match).length, 3);
  assert.equal(join(parts), 'ababab');
});

test('caracteres especiais de regex são literais', () => {
  assert.deepEqual(highlightParts('custa 3.5 (ou 3x5)', '3.5'), [
    { text: 'custa ', match: false },
    { text: '3.5', match: true },
    { text: ' (ou 3x5)', match: false },
  ]);
  assert.deepEqual(highlightParts('a+b=c', 'a+b').filter((p) => p.match).map((p) => p.text), ['a+b']);
  assert.doesNotThrow(() => highlightParts('qualquer', '(['));
  assert.doesNotThrow(() => highlightParts('qualquer', '\\'));
});

test('espaços do termo são normalizados como no servidor', () => {
  assert.deepEqual(highlightParts('oi mundo', '  oi   mundo '), [{ text: 'oi mundo', match: true }]);
});

test('concatenar as partes sempre reproduz o texto original', () => {
  for (const [text, term] of [['İstanbul i̇', 'i'], ['a\nb', 'b'], ['😀 oi 😀', '😀'], ['xx', 'x']]) {
    assert.equal(join(highlightParts(text, term)), text);
  }
});
