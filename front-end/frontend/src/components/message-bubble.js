import { h, setText, toggleClass } from '../utils/dom.js';
import { formatTime } from '../utils/dates.js';
import { icon } from '../utils/icons.js';

// Ícone e texto acessível de cada status das minhas mensagens.
const statusView = {
  pending: { icon: 'clock', label: 'Enviando' },
  sent: { icon: 'check', label: 'Enviada' },
  delivered: { icon: 'checks', label: 'Entregue' },
  read: { icon: 'checks', label: 'Lida' },
  failed: { icon: 'alert', label: 'Não enviada' },
};

/**
 * Uma mensagem. O conteúdo entra sempre como texto.
 * props: { message, own, sender (nome ou '' para ocultar), first, last, onRetry(clientId) } — first/last indicam a
 * posição dentro de um grupo de mensagens seguidas do mesmo remetente e controlam o espaçamento e o canto da bolha.
 * Mensagens próprias com status failed mostram o motivo e a ação "Tentar novamente".
 */
export function createMessageBubble(props) {
  const sender = h('span', { class: 'message-bubble__sender' });
  const body = h('p', { class: 'message-bubble__body' });
  const time = h('time', { class: 'message-bubble__time' });
  const status = h('span', { class: 'message-bubble__status' });
  const meta = h('span', { class: 'message-bubble__meta' }, time, status);
  const bubble = h('div', { class: 'message-bubble__bubble' }, sender, body, meta);
  const failureText = h('span');
  const retry = h(
    'button',
    { class: 'button button--link', type: 'button', on: { click: () => props.onRetry?.(props.message.client_id) } },
    'Tentar novamente',
  );
  const failure = h('p', { class: 'message-bubble__failure', role: 'alert' }, icon('alert'), failureText, retry);
  const el = h('div', { class: 'message-bubble' }, bubble, failure);
  let renderedStatus = null;

  function update(next) {
    props = { ...props, ...next };
    const { message, own } = props;
    toggleClass(el, 'message-bubble--own', own);
    toggleClass(el, 'message-bubble--first', props.first);
    toggleClass(el, 'message-bubble--last', props.last);

    sender.hidden = !props.sender;
    setText(sender, props.sender);
    setText(body, message.body);
    setText(time, formatTime(message.created_at));
    time.setAttribute('datetime', message.created_at ?? '');

    const view = own ? statusView[message.status] : null;
    status.hidden = !view;
    if (view && renderedStatus !== message.status) {
      renderedStatus = message.status;
      status.replaceChildren(icon(view.icon, view.label));
      status.dataset.status = message.status;
    }

    const failed = own && message.status === 'failed';
    failure.hidden = !failed;
    if (failed) setText(failureText, message.error ? `Não enviada: ${message.error}` : 'Não enviada.');
  }

  update(props);
  return { el, update, destroy() {} };
}
