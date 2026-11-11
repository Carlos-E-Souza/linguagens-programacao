import { h, reconcileChildren, replaceChildren, setText, toggleClass } from '../utils/dom.js';
import { icon } from '../utils/icons.js';
import { createAvatar } from './avatar.js';

// Espera depois da última tecla antes de buscar no servidor.
const DEBOUNCE_MS = 300;

let nextId = 0;

/**
 * Busca de usuários com debounce. Cada resultado é um botão: com multiple, alterna a seleção (aria-pressed); sem
 * multiple, escolhe o usuário. Enter no campo escolhe o primeiro resultado.
 * props: { search(term) → Promise<User[]>, onPick(user), multiple, selectedIds: number[], excludeIds: number[],
 *          label, placeholder, disabled }
 */
export function createUserSearch(props) {
  const id = `user-search-${++nextId}`;
  const input = h('input', {
    class: 'field__input user-search__input',
    id,
    type: 'search',
    autocomplete: 'off',
    spellcheck: 'false',
    'aria-controls': `${id}-results`,
  });
  const status = h('p', { class: 'user-search__status', role: 'status' });
  const list = h('ul', { class: 'user-search__results', id: `${id}-results`, 'aria-label': 'Resultados da busca' });
  const label = h('label', { class: 'field__label', for: id });
  const el = h(
    'div',
    { class: 'user-search' },
    h('div', { class: 'field' }, label, h('div', { class: 'user-search__control' }, icon('search'), input)),
    status,
    list,
  );

  const items = new Map();
  let users = [];
  let term = '';
  let loading = false;
  let error = '';
  let timer = null;
  let request = 0;

  input.addEventListener('input', () => {
    clearTimeout(timer);
    term = input.value.trim();
    if (!term) {
      request++;
      users = [];
      loading = false;
      error = '';
      render();
      return;
    }
    loading = true;
    render();
    timer = setTimeout(run, DEBOUNCE_MS);
  });
  input.addEventListener('keydown', (event) => {
    if (event.key !== 'Enter') return;
    event.preventDefault();
    const first = visible()[0];
    if (first) props.onPick(first);
  });

  async function run() {
    const current = ++request;
    try {
      const found = await props.search(term);
      if (current !== request) return; // resposta de uma busca antiga
      users = found;
      error = '';
    } catch (err) {
      if (current !== request) return;
      users = [];
      error = err?.message || 'Não foi possível buscar agora.';
    }
    loading = false;
    render();
  }

  function visible() {
    const excluded = new Set(props.excludeIds ?? []);
    return users.filter((u) => !excluded.has(u.id));
  }

  function createItem(user) {
    const avatar = createAvatar({ id: user.id, name: user.display_name, size: 'sm' });
    const name = h('span', { class: 'user-search__name' });
    const username = h('span', { class: 'user-search__username' });
    const check = h('span', { class: 'user-search__check', 'aria-hidden': 'true' }, icon('check'));
    const button = h(
      'button',
      { class: 'user-search__button', type: 'button', on: { click: () => props.onPick(item.user) } },
      avatar.el,
      h('span', { class: 'user-search__text' }, name, username),
      check,
    );
    const item = { li: h('li', {}, button), button, avatar, name, username, check, user };
    return item;
  }

  function render() {
    const shown = visible();
    const selected = new Set(props.selectedIds ?? []);
    const seen = new Set();
    const nodes = shown.map((user) => {
      seen.add(user.id);
      let item = items.get(user.id);
      if (!item) {
        item = createItem(user);
        items.set(user.id, item);
      }
      item.user = user;
      item.avatar.update({ name: user.display_name, online: user.online });
      setText(item.name, user.display_name || user.username);
      setText(item.username, `@${user.username}${user.online ? ' · online' : ''}`);
      item.button.disabled = Boolean(props.disabled);
      item.check.hidden = !props.multiple;
      if (props.multiple) {
        const on = selected.has(user.id);
        item.button.setAttribute('aria-pressed', String(on));
        toggleClass(item.button, 'user-search__button--selected', on);
      } else {
        item.button.removeAttribute('aria-pressed');
      }
      return item.li;
    });
    for (const key of items.keys()) if (!seen.has(key)) items.delete(key);
    reconcileChildren(list, nodes);

    if (!term) setText(status, 'Digite um nome ou nome de usuário para buscar.');
    else if (loading) replaceChildren(status, h('span', { class: 'spinner', 'aria-hidden': 'true' }), 'Buscando…');
    else if (error) setText(status, error);
    else if (shown.length === 0) setText(status, 'Ninguém encontrado com esse nome.');
    else setText(status, `${shown.length} ${shown.length === 1 ? 'pessoa encontrada' : 'pessoas encontradas'}`);
    toggleClass(status, 'user-search__status--error', Boolean(error) && !loading);
  }

  function update(next) {
    props = { ...props, ...next };
    setText(label, props.label ?? 'Buscar pessoas');
    input.placeholder = props.placeholder ?? 'Nome ou @usuário';
    input.disabled = Boolean(props.disabled);
    render();
  }

  update(props);
  return {
    el,
    update,
    focus: () => input.focus(),
    destroy() {
      clearTimeout(timer);
      request++;
    },
  };
}
