import { h, setText, toggleClass } from '../utils/dom.js';
import { icon } from '../utils/icons.js';
import { createDraftStore } from '../utils/drafts.js';

// Mesmo limite do servidor (MESSAGE_LIMIT_CHARS) e da camada Go.
export const MAX_MESSAGE_CHARS = 4000;
// A partir daqui o contador de caracteres aparece.
const COUNTER_FROM = MAX_MESSAGE_CHARS - 300;
// Altura máxima da caixa antes de rolar (cerca de 6 linhas).
const MAX_HEIGHT_PX = 160;

/**
 * Campo de escrita da conversa: Enter envia, Shift+Enter quebra a linha. Guarda um rascunho por conversa no cache.
 * props: { conversationId, onSend(body), onLoadDraft(id), onSaveDraft(id, body) }.
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

  const drafts = createDraftStore(
    (id) => props.onLoadDraft(id),
    (id, body) => props.onSaveDraft(id, body),
  );
  let conversationId = null;
  let destroyed = false;

  textarea.addEventListener('input', () => {
    const id = conversationId;
    const body = textarea.value;
    drafts.save(id, body).catch(() => {
      if (!destroyed && conversationId === id && drafts.peek(id) === body) {
        showError('Não foi possível salvar o rascunho neste dispositivo.');
      }
    });
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
    const send = props.onSend;
    textarea.value = '';
    refresh();
    textarea.focus();
    try {
      // A gravação vazia vem depois das letras anteriores e antes do envio.
      await drafts.save(id, '');
      await send(body);
    } catch (err) {
      // Se o envio falhar, guarda o texto de novo na conversa de origem.
      if (drafts.peek(id) === '') drafts.save(id, body).catch(() => {});
      if (conversationId !== id) return;
      showError(err?.message || 'Não foi possível enviar a mensagem.');
      if (!textarea.value) {
        textarea.value = body;
        refresh();
      }
    }
  }

  function update(next) {
    props = { ...props, ...next };
    if (props.conversationId !== conversationId) {
      conversationId = props.conversationId;
      const id = conversationId;
      textarea.value = drafts.peek(id) ?? '';
      showError('');
      refresh();
      drafts.load(id).then((body) => {
        if (destroyed || conversationId !== id || drafts.peek(id) !== body) return;
        textarea.value = body;
        refresh();
      }).catch(() => {
        if (!destroyed && conversationId === id) showError('Não foi possível ler o rascunho salvo.');
      });
      queueMicrotask(() => { if (!destroyed && conversationId === id) textarea.focus(); });
    }
  }

  update(props);
  return { el: form, update, flush: () => drafts.flush(), destroy() { destroyed = true; } };
}
