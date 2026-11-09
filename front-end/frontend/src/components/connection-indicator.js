import { h, setText } from '../utils/dom.js';
import { icon } from '../utils/icons.js';

const messages = {
  connecting: 'Conectando ao servidor…',
  disconnected: 'Sem conexão. Tentando reconectar automaticamente.',
};

/**
 * Faixa exibida quando a conexão não está ativa. Fica oculta quando conectado.
 * props: { state: 'connected' | 'connecting' | 'disconnected' }
 */
export function createConnectionIndicator(props) {
  const text = h('span', { class: 'connection-indicator__text' });
  const spinner = h('span', { class: 'spinner connection-indicator__spinner', 'aria-hidden': 'true' });
  const offline = icon('offline');
  const el = h('div', { class: 'connection-indicator', role: 'status', 'aria-live': 'polite' }, spinner, offline, text);

  function update(next) {
    props = { ...props, ...next };
    const { state } = props;
    el.hidden = state === 'connected';
    el.dataset.state = state;
    spinner.hidden = state !== 'connecting';
    offline.style.display = state === 'disconnected' ? '' : 'none';
    setText(text, messages[state] ?? '');
  }

  update(props);
  return { el, update, destroy() {} };
}
