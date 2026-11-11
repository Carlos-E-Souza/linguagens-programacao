import { h, reconcileChildren } from '../utils/dom.js';
import { icon } from '../utils/icons.js';
import { createUserSearch } from './user-search.js';

/**
 * Escolha de várias pessoas: busca com seleção múltipla e, acima dela, as escolhidas como etiquetas removíveis.
 * props: { search(term), excludeIds: number[], label, disabled, onChange(users) }
 */
export function createMemberPicker(props) {
  const selected = new Map();
  const chips = h('ul', { class: 'member-picker__chips', 'aria-label': 'Pessoas escolhidas' });
  const finder = createUserSearch({
    search: (term) => props.search(term),
    onPick: toggle,
    multiple: true,
    label: props.label,
  });
  const el = h('div', { class: 'member-picker' }, chips, finder.el);
  const chipNodes = new Map();

  function toggle(user) {
    if (selected.has(user.id)) selected.delete(user.id);
    else selected.set(user.id, user);
    render();
    props.onChange?.([...selected.values()]);
  }

  function chipFor(user) {
    let chip = chipNodes.get(user.id);
    if (!chip) {
      const name = user.display_name || user.username;
      chip = h(
        'li',
        { class: 'member-picker__chip' },
        h('span', {}, name),
        h(
          'button',
          {
            class: 'member-picker__remove',
            type: 'button',
            'aria-label': `Tirar ${name}`,
            title: `Tirar ${name}`,
            on: { click: () => toggle(user) },
          },
          icon('close'),
        ),
      );
      chipNodes.set(user.id, chip);
    }
    return chip;
  }

  function render() {
    const users = [...selected.values()];
    for (const id of chipNodes.keys()) if (!selected.has(id)) chipNodes.delete(id);
    reconcileChildren(chips, users.map(chipFor));
    chips.hidden = users.length === 0;
    for (const button of chips.querySelectorAll('button')) button.disabled = Boolean(props.disabled);
    finder.update({ selectedIds: [...selected.keys()], excludeIds: props.excludeIds, disabled: props.disabled });
  }

  function update(next) {
    props = { ...props, ...next };
    // Quem passou a ser membro enquanto estava escolhido sai da seleção.
    for (const id of props.excludeIds ?? []) selected.delete(id);
    render();
  }

  update(props);
  return {
    el,
    update,
    focus: () => finder.focus(),
    selected: () => [...selected.values()],
    clear() {
      selected.clear();
      render();
      props.onChange?.([]);
    },
    destroy: () => finder.destroy(),
  };
}
