import { h, setText, toggleClass } from '../utils/dom.js';
import { icon } from '../utils/icons.js';

// Mesmo limite do servidor (MESSAGE_LIMIT_CHARS) e da camada Go.
export const MAX_MESSAGE_CHARS = 4000;
// A partir daqui o contador de caracteres aparece.
const COUNTER_FROM = MAX_MESSAGE_CHARS - 300;
// Altura máxima da caixa antes de rolar (cerca de 6 linhas).
const MAX_HEIGHT_PX = 160;

/**
 * Campo de escrita da conversa: Enter envia, Shift+Enter quebra a linha. Guarda um rascunho por conversa.
 * props: { conversationId, onSend(body) → Promise } — se onSend rejeitar com um Error, a mensagem dele é exibida e o
 * texto volta para o campo.
 */
export function createMessageInput(props) {
  const textarea = h('textarea', {
    class: 'message-input__field',
    rows: '1',
    placeholder: 'Escreva uma mensagem',
    'aria-label': 'Mensagem',
    'aria-describedby': 'message-input-help',
    spellcheck: 'true',
  });
  const help = h(
    'span',
    { class: 'visually-hidden', id: 'message-input-help' },
    'Enter envia. Shift+Enter quebra a linha.',
  );
  const counter = h('span', { class: 'message-input__counter', 'aria-live': 'polite' });
  const sendButton = h(
    'button',
    { class: 'message-input__send', type: 'submit', 'aria-label': 'Enviar mensagem', title: 'Enviar (Enter)' },
    icon('send'),
  );
  const error = h('p', { class: 'message-input__error', role: 'alert' });
  const form = h(
    'form',
    { class: 'message-input', on: { submit: onSubmit } },
    error,
    h('div', { class: 'message-input__box' }, textarea, counter),
    sendButton,
    help,
  );

  const drafts = new Map();
  let conversationId = null;

  textarea.addEventListener('input', () => {
    drafts.set(conversationId, textarea.value);
    showError('');
    refresh();
  });
  textarea.addEventListener('keydown', (event) => {
    // Durante a composição de acentos (IME), Enter confirma o caractere, não envia.
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      form.requestSubmit();
    }
  });

  function showError(message) {
    error.hidden = !message;
    setText(error, message);
  }

  function length() {
    return [...textarea.value.trim()].length;
  }

  function refresh() {
    textarea.style.height = 'auto';
    textarea.style.height = `${Math.min(textarea.scrollHeight, MAX_HEIGHT_PX)}px`;
    const count = length();
    const tooLong = count > MAX_MESSAGE_CHARS;
    sendButton.disabled = count === 0 || tooLong;
    counter.hidden = count < COUNTER_FROM;
    setText(counter, `${count}/${MAX_MESSAGE_CHARS}`);
    toggleClass(counter, 'message-input__counter--over', tooLong);
  }

  async function onSubmit(event) {
    event.preventDefault();
    const body = textarea.value.trim();
    if (!body || [...body].length > MAX_MESSAGE_CHARS) return;
    // Limpa na hora: a mensagem aparece como pendente e a fila garante a entrega.
    const id = conversationId;
    textarea.value = '';
    drafts.delete(id);
    refresh();
    textarea.focus();
    try {
      await props.onSend(body);
    } catch (err) {
      // Não guardou: devolve o texto ao campo para não perder o que foi escrito.
      if (conversationId !== id) return;
      showError(err?.message || 'Não foi possível enviar a mensagem.');
      if (!textarea.value) {
        textarea.value = body;
        drafts.set(id, body);
        refresh();
      }
    }
  }

  function update(next) {
    props = { ...props, ...next };
    if (props.conversationId !== conversationId) {
      conversationId = props.conversationId;
      textarea.value = drafts.get(conversationId) ?? '';
      showError('');
      refresh();
      queueMicrotask(() => textarea.focus());
    }
  }

  update(props);
  return { el: form, update, destroy() {} };
}
