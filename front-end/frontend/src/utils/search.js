// Utilitários da busca de mensagens.

/** Escapa os caracteres que têm significado especial em uma expressão regular. */
function escapeRegExp(text) {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/**
 * Divide text em trechos para destacar as ocorrências de term (sem diferenciar maiúsculas, como a busca do
 * servidor). Devolve [{ text, match }]; juntar os `text` na ordem reproduz o original. Sem termo, um trecho só.
 * Quem renderiza deve usar textContent (nunca innerHTML): o texto da mensagem não é confiável.
 */
export function highlightParts(text, term) {
  const source = String(text ?? '');
  if (source === '') return [];
  const needle = String(term ?? '').trim().split(/\s+/).filter(Boolean).join(' ');
  if (needle === '') return [{ text: source, match: false }];

  const parts = [];
  let last = 0;
  for (const found of source.matchAll(new RegExp(escapeRegExp(needle), 'gi'))) {
    if (found.index > last) parts.push({ text: source.slice(last, found.index), match: false });
    parts.push({ text: found[0], match: true });
    last = found.index + found[0].length;
  }
  if (last < source.length) parts.push({ text: source.slice(last), match: false });
  return parts;
}
