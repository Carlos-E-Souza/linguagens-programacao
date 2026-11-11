import { h, setText } from '../utils/dom.js';
import { createMemberPicker } from './member-picker.js';
import { createModal } from './modal.js';

// Mesmo limite da camada Go (maxGroupNameChars).
const MAX_NAME_CHARS = 64;

/**
 * Novo grupo: nome e pessoas. O botão "Criar grupo" só habilita com nome e ao menos uma pessoa.
 * props: { search(term), onCreate(name, memberIds) → Promise (rejeita com Error exibível), onClose() }
 */
export function createGroupModal(props) {
  const nameInput = h('input', {
    class: 'field__input',
    id: 'group-name',
    type: 'text',
    maxlength: String(MAX_NAME_CHARS),
    autocomplete: 'off',
    placeholder: 'Ex.: Projeto de LP',
  });
  const nameField = h(
    'div',
    { class: 'field' },
    h('label', { class: 'field__label', for: 'group-name' }, 'Nome do grupo'),
    nameInput,
  );
  const picker = createMemberPicker({
    search: (term) => props.search(term),
    label: 'Adicionar pessoas',
    onChange: refresh,
  });
  const error = h('p', { class: 'modal__error', role: 'alert', hidden: true });
  const cancel = h('button', { class: 'button button--secondary', type: 'button', on: { click: () => props.onClose() } }, 'Cancelar');
  const create = h('button', { class: 'button button--primary', type: 'submit', form: 'group-form' }, 'Criar grupo');
  const form = h('form', { class: 'modal__form', id: 'group-form', on: { submit: onSubmit } }, nameField, picker.el, error);

  const modal = createModal({
    title: 'Novo grupo',
    body: form,
    footer: [cancel, create],
    onClose: () => props.onClose(),
  });

  let busy = false;
  nameInput.addEventListener('input', refresh);

  function refresh() {
    create.disabled = busy || !nameInput.value.trim() || picker.selected().length === 0;
  }

  function showError(message) {
    setText(error, message);
    error.hidden = !message;
  }

  async function onSubmit(event) {
    event.preventDefault();
    const name = nameInput.value.trim();
    const members = picker.selected().map((u) => u.id);
    if (busy || !name || members.length === 0) return;
    busy = true;
    nameInput.disabled = true;
    picker.update({ disabled: true });
    setText(create, 'Criando…');
    showError('');
    refresh();
    try {
      await props.onCreate(name, members);
    } catch (err) {
      busy = false;
      nameInput.disabled = false;
      picker.update({ disabled: false });
      setText(create, 'Criar grupo');
      showError(err?.message || 'Não foi possível criar o grupo.');
      refresh();
    }
  }

  refresh();
  return {
    el: modal.el,
    update() {},
    open() {
      modal.open();
      nameInput.focus();
    },
    destroy() {
      picker.destroy();
      modal.destroy();
    },
  };
}
