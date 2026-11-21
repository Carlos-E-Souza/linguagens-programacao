import { h, reconcileChildren, replaceChildren, setText, toggleClass } from '../utils/dom.js';
import { formatListTime } from '../utils/dates.js';
import { icon } from '../utils/icons.js';
import { highlightParts } from '../utils/search.js';

// Espera depois da última tecla antes de buscar no servidor.
const DEBOUNCE_MS = 300;
// Os mesmos limites do servidor (normalizeSearchTerm): abaixo de 2 caracteres nem vale a requisição.
const MIN_CHARS = 2;
const MAX_CHARS = 100;
// Tamanho da página do servidor (searchPageSize em app.go). Uma página cheia significa que pode haver mais.
const PAGE_SIZE = 20;

let nextId = 0;

/**
 * Painel de busca dentro de uma conversa: campo, lista de resultados (do mais novo ao mais antigo) com o trecho
 * encontrado destacado, e "Carregar mais".
 * props: { search(term, before) → Promise<Message[]>, senderName(senderId) → string, onClose() }
 */
export function createMessageSearch(props) {
  const id = `message-search-${++nextId}`;
  const input = h('input', {
    class: 'field__input message-search__input',
    id,
    type: 'search',
    autocomplete: 'off',
    spellcheck: 'false',
    placeholder: 'Buscar nesta conversa',
    'aria-label': 'Buscar mensagens nesta conversa',
    'aria-controls': `${id}-results`,
  });
  const closeButton = h(
    'button',
    {
      class: 'button button--icon',
      type: 'button',
      'aria-label': 'Fechar busca',
      title: 'Fechar busca',
      on: { click: () => props.onClose?.() },
    },
    icon('close'),
  );
  const status = h('p', { class: 'message-search__status', role: 'status' });
  const list = h('ul', { class: 'message-search__results', id: `${id}-results`, 'aria-label': 'Resultados da busca' });
  const moreButton = h(
    'button',
    { class: 'button button--link message-search__more', type: 'button', on: { click: () => loadMore() } },
    'Carregar mais',
  );
  const el = h(
    'div',
    { class: 'message-search' },
    h('div', { class: 'message-search__bar' }, icon('search'), input, closeButton),
    status,
    list,
    moreButton,
  );

  const items = new Map();
  let term = '';
  let results = [];
  let hasMore = false;
  let loading = false;
  let error = '';
  let timer = null;
  // Cada busca nova ganha um número; respostas de buscas antigas (digitou de novo) são descartadas.
  let request = 0;

  input.addEventListener('input', () => {
    clearTimeout(timer);
    request++;
    term = input.value.trim().split(/\s+/).filter(Boolean).join(' ');
    results = [];
    hasMore = false;
    error = '';
    if ([...term].length < MIN_CHARS) {
      loading = false;
      render();
      return;
    }
    loading = true;
    render();
    timer = setTimeout(run, DEBOUNCE_MS);
  });
  input.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') {
      event.preventDefault();
      props.onClose?.();
    }
  });

  async function run() {
    const current = request;
    try {
      const found = await props.search(term, 0);
      if (current !== request) return;
      results = found;
      hasMore = found.length >= PAGE_SIZE;
      error = '';
    } catch (err) {
      if (current !== request) return;
      results = [];
      hasMore = false;
      error = err?.message || 'Não foi possível buscar agora.';
    }
    loading = false;
    render();
  }

  async function loadMore() {
    if (loading || !hasMore || results.length === 0) return;
    const current = request;
    // Os resultados vêm do mais novo para o mais antigo: a última sequência é a menor, e é o cursor da próxima página.
    const before = results[results.length - 1].sequence;
    loading = true;
    error = '';
    render();
    try {
      const found = await props.search(term, before);
      if (current !== request) return;
      results = results.concat(found);
      hasMore = found.length >= PAGE_SIZE;
    } catch (err) {
      if (current !== request) return;
      error = err?.message || 'Não foi possível buscar agora.';
    }
    loading = false;
    render();
  }

  function createItem(message) {
    const sender = h('span', { class: 'message-search__sender' });
    const time = h('span', { class: 'message-search__time' });
    const body = h('p', { class: 'message-search__body' });
    const li = h('li', { class: 'message-search__item' }, h('div', { class: 'message-search__meta' }, sender, time), body);
    return { li, sender, time, body, message, shownTerm: null };
  }

  function render() {
    const seen = new Set();
    const nodes = results.map((message) => {
      seen.add(message.id);
      let item = items.get(message.id);
      if (!item) {
        item = createItem(message);
        items.set(message.id, item);
      }
      setText(item.sender, props.senderName?.(message.sender_id) ?? '');
      setText(item.time, formatListTime(message.created_at));
      if (item.shownTerm !== term) {
        // Sempre nós de texto e <mark>: o corpo da mensagem não é confiável e nunca entra como HTML.
        replaceChildren(
          item.body,
          highlightParts(message.body, term).map((part) => (part.match ? h('mark', {}, part.text) : part.text)),
        );
        item.shownTerm = term;
      }
      return item.li;
    });
    for (const key of items.keys()) if (!seen.has(key)) items.delete(key);
    reconcileChildren(list, nodes);

    const searching = [...term].length >= MIN_CHARS;
    if (loading) replaceChildren(status, h('span', { class: 'spinner', 'aria-hidden': 'true' }), 'Buscando…');
    else if (error) setText(status, error);
    else if (term === '') setText(status, 'Digite para buscar nesta conversa.');
    else if (!searching) setText(status, `Digite pelo menos ${MIN_CHARS} caracteres.`);
    else if (results.length === 0) setText(status, 'Nenhuma mensagem encontrada.');
    else setText(status, `${results.length}${hasMore ? '+' : ''} ${results.length === 1 ? 'resultado' : 'resultados'}`);
    toggleClass(status, 'message-search__status--error', Boolean(error) && !loading);

    moreButton.hidden = !hasMore || results.length === 0;
    moreButton.disabled = loading;
  }

  input.maxLength = MAX_CHARS;
  render();
  return {
    el,
    focus: () => input.focus(),
    destroy() {
      clearTimeout(timer);
      request++;
    },
  };
}