import { h, setText } from '../utils/dom.js';
import { icon } from '../utils/icons.js';

let nextId = 0;

/**
 * Janela modal sobre a tela, com o <dialog> nativo: showModal prende o foco dentro dela e Esc fecha.
 * Clicar fora do cartão também fecha.
 * props: { title, body (elemento), footer (elemento opcional), onClose() }
 * O dono chama open() depois de montar e destroy() quando não precisar mais.
 */
export function createModal(props) {
  const titleId = `modal-title-${++nextId}`;
  const title = h('h2', { class: 'modal__title', id: titleId });
  const closeButton = h(
    'button',
    { class: 'button button--icon', type: 'button', 'aria-label': 'Fechar', title: 'Fechar (Esc)', on: { click: close } },
    icon('close'),
  );
  const card = h(
    'div',
    { class: 'modal__card' },
    h('header', { class: 'modal__header' }, title, closeButton),
    h('div', { class: 'modal__body' }, props.body),
    props.footer ? h('footer', { class: 'modal__footer' }, props.footer) : null,
  );
  const el = h('dialog', { class: 'modal', 'aria-labelledby': titleId }, card);
  let closed = false;

  // Esc dispara "cancel": o fechamento passa pelo dono, que decide desmontar.
  el.addEventListener('cancel', (event) => {
    event.preventDefault();
    close();
  });
  el.addEventListener('mousedown', (event) => {
    if (event.target === el) close();
  });

  function close() {
    if (closed) return;
    closed = true;
    props.onClose?.();
  }

  function update(next) {
    props = { ...props, ...next };
    setText(title, props.title);
  }

  update(props);
  return {
    el,
    update,
    open() {
      document.body.append(el);
      el.showModal();
    },
    destroy() {
      if (el.open) el.close();
      el.remove();
    },
  };
}
