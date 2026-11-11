import { h, setText } from '../utils/dom.js';
import { createModal } from './modal.js';
import { createUserSearch } from './user-search.js';

/**
 * Nova conversa individual: busca uma pessoa e abre (ou cria) a conversa com ela ao clicar.
 * props: { search(term), onStart(userId) → Promise (rejeita com Error exibível), onClose() }
 */
export function createNewConversationModal(props) {
  const error = h('p', { class: 'modal__error', role: 'alert', hidden: true });
  let busy = false;

  const finder = createUserSearch({
    search: (term) => props.search(term),
    onPick: start,
    label: 'Com quem você quer conversar?',
  });

  const modal = createModal({
    title: 'Nova conversa',
    body: [finder.el, error],
    onClose: () => props.onClose(),
  });

  async function start(user) {
    if (busy) return;
    busy = true;
    finder.update({ disabled: true });
    setText(error, '');
    error.hidden = true;
    try {
      await props.onStart(user.id);
    } catch (err) {
      busy = false;
      finder.update({ disabled: false });
      setText(error, err?.message || 'Não foi possível abrir a conversa.');
      error.hidden = false;
      finder.focus();
    }
  }

  return {
    el: modal.el,
    update() {},
    open() {
      modal.open();
      finder.focus();
    },
    destroy() {
      finder.destroy();
      modal.destroy();
    },
  };
}
