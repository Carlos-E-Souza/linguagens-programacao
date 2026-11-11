import { h, reconcileChildren, replaceChildren, setText, toggleClass } from '../utils/dom.js';
import { dayKey, diffMs, formatDay } from '../utils/dates.js';
import { icon } from '../utils/icons.js';
import { conversationSubtitle, conversationTitle, messageKey, otherMember, senderName } from '../utils/conversations.js';
import { createAvatar } from './avatar.js';
import { createMessageBubble } from './message-bubble.js';
import { createMessageInput } from './message-input.js';

// Mensagens do mesmo remetente com menos de 5 minutos entre si formam um grupo visual.
const GROUP_WINDOW_MS = 5 * 60 * 1000;
// Distância do topo (px) que dispara o carregamento da página anterior.
const LOAD_OLDER_THRESHOLD = 240;
// Distância do fim (px) em que a lista ainda é considerada "no fim" e acompanha mensagens novas.
const STICK_TO_BOTTOM = 96;
// Quantas mensagens ficam no DOM. Rolar para cima mostra mais RENDER_STEP de cada vez (primeiro as que já estão
// na memória, depois páginas do servidor); voltar ao fim corta de novo para RENDER_WINDOW.
const RENDER_WINDOW = 150;
const RENDER_STEP = 100;

/**
 * Janela da conversa aberta: cabeçalho, histórico com separadores de dia e rolagem infinita para cima, e o campo
 * de escrita.
 * props: { conversation, me, page: { items, hasMore, loading, loaded, error }, onLoadOlder(), onRetry(),
 *          onSend(body), onRetryMessage(clientId), onShowDetails() }
 */
export function createChatWindow(props) {
  const avatar = createAvatar({ id: 0 });
  const title = h('h2', { class: 'chat-window__title' });
  const subtitle = h('p', { class: 'chat-window__subtitle', 'aria-live': 'polite' });
  const detailsButton = h(
    'button',
    {
      class: 'button button--icon chat-window__details',
      type: 'button',
      'aria-label': 'Detalhes do grupo',
      title: 'Membros e detalhes do grupo',
      on: { click: () => props.onShowDetails?.() },
    },
    icon('info'),
  );
  const header = h(
    'header',
    { class: 'chat-window__header' },
    avatar.el,
    h('div', { class: 'chat-window__heading' }, title, subtitle),
    detailsButton,
  );

  const topStatus = h('div', { class: 'chat-window__top-status', role: 'status' });
  const rows = h('div', { class: 'chat-window__rows' });
  const placeholder = h('div', { class: 'chat-window__placeholder' });
  const scroller = h(
    'div',
    { class: 'chat-window__scroller', role: 'log', 'aria-label': 'Mensagens', tabindex: '0' },
    topStatus,
    rows,
    placeholder,
  );
  const input = createMessageInput({
    conversationId: props.conversation.id,
    onSend: (body) => props.onSend(body),
  });
  const el = h('section', { class: 'chat-window' }, header, scroller, input.el);

  let conversationId = null;
  let bubbles = new Map();
  let separators = new Map();
  let firstKey = null;
  let lastKey = null;
  let renderLimit = RENDER_WINDOW;

  scroller.addEventListener('scroll', () => {
    if (scroller.scrollTop < LOAD_OLDER_THRESHOLD) requestOlder();
  });

  function requestOlder() {
    const { page } = props;
    if (page.items.length > renderLimit) {
      // Ainda há mensagens antigas na memória: mostra mais sem ir ao servidor.
      renderLimit += RENDER_STEP;
      update({});
      return;
    }
    if (page.loaded && page.hasMore && !page.loading && !page.error) {
      renderLimit = Math.max(renderLimit, page.items.length) + RENDER_STEP;
      props.onLoadOlder();
    }
  }

  function renderHeader() {
    const { conversation, me } = props;
    const group = conversation.type === 'group';
    const other = group ? null : otherMember(conversation, me);
    const name = conversationTitle(conversation, me);
    avatar.update({ id: group ? conversation.id : other?.user_id, name, group, online: other?.online });
    setText(title, name);
    setText(subtitle, conversationSubtitle(conversation, me));
    detailsButton.hidden = !group;
    subtitle.dataset.online = String(Boolean(other?.online));
  }

  function separatorFor(key, date) {
    let sep = separators.get(key);
    if (!sep) {
      sep = h('div', { class: 'chat-window__day', role: 'separator' }, h('span', {}, formatDay(date)));
      separators.set(key, sep);
    }
    return sep;
  }

  function renderRows() {
    const { conversation, me, page } = props;
    const items = page.items.slice(-renderLimit);
    const group = conversation.type === 'group';
    const nodes = [];
    const usedBubbles = new Map();
    const usedSeparators = new Map();

    items.forEach((message, index) => {
      const prev = items[index - 1];
      const next = items[index + 1];
      const day = dayKey(message.created_at);
      if (!prev || dayKey(prev.created_at) !== day) {
        const sep = separatorFor(day, message.created_at);
        usedSeparators.set(day, sep);
        nodes.push(sep);
      }
      const continues = (a, b) =>
        a && b && a.sender_id === b.sender_id && dayKey(a.created_at) === dayKey(b.created_at) &&
        diffMs(a.created_at, b.created_at) < GROUP_WINDOW_MS;
      const own = message.sender_id === me?.id;
      const first = !continues(prev, message);
      const bubbleProps = {
        message,
        own,
        first,
        last: !continues(message, next),
        sender: group && !own && first ? senderName(conversation, message.sender_id, me) : '',
        onRetry: props.onRetryMessage,
      };

      const key = messageKey(message);
      let bubble = bubbles.get(key);
      if (bubble) bubble.update(bubbleProps);
      else bubble = createMessageBubble(bubbleProps);
      usedBubbles.set(key, bubble);
      nodes.push(bubble.el);
    });

    bubbles = usedBubbles;
    separators = usedSeparators;
    reconcileChildren(rows, nodes);
    return items;
  }

  function renderStatus() {
    const { page } = props;
    const empty = page.items.length === 0;

    // Topo: carregando páginas anteriores, erro ou início da conversa.
    if (page.error && !empty) {
      replaceChildren(
        topStatus,
        h('span', {}, page.error),
        h('button', { class: 'button button--link', type: 'button', on: { click: props.onRetry } }, 'Tentar novamente'),
      );
    } else if (page.loading && !empty) {
      replaceChildren(topStatus, h('span', { class: 'spinner', 'aria-hidden': 'true' }), 'Carregando mensagens anteriores…');
    } else if (page.loaded && !page.hasMore && !empty && page.items.length <= renderLimit) {
      replaceChildren(topStatus, 'Início da conversa');
    } else {
      replaceChildren(topStatus);
    }

    // Conversa sem mensagens visíveis: carregando, erro ou vazia.
    placeholder.hidden = !empty;
    if (!empty) return;
    if (page.error) {
      replaceChildren(
        placeholder,
        emptyState('alert', 'Não foi possível carregar as mensagens', page.error),
        h('button', { class: 'button button--link', type: 'button', on: { click: props.onRetry } }, 'Tentar novamente'),
      );
    } else if (!page.loaded) {
      replaceChildren(placeholder, h('span', { class: 'spinner', role: 'status', 'aria-label': 'Carregando mensagens' }));
    } else {
      replaceChildren(placeholder, emptyState('chat', 'Nenhuma mensagem ainda', 'As mensagens desta conversa aparecem aqui.'));
    }
  }

  function update(next) {
    props = { ...props, ...next };
    const switched = props.conversation.id !== conversationId;
    if (switched) {
      conversationId = props.conversation.id;
      bubbles = new Map();
      separators = new Map();
      rows.replaceChildren();
      firstKey = lastKey = null;
      renderLimit = RENDER_WINDOW;
    }
    input.update({ conversationId: props.conversation.id });

    const nearBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < STICK_TO_BOTTOM;
    const prevHeight = scroller.scrollHeight;
    const prevTop = scroller.scrollTop;
    // De volta ao fim depois de ler o histórico: tira do DOM as mensagens antigas que estavam visíveis.
    if (!switched && nearBottom && renderLimit > RENDER_WINDOW) renderLimit = RENDER_WINDOW;

    renderHeader();
    const items = renderRows();
    renderStatus();

    const newFirst = items.length ? messageKey(items[0]) : null;
    const newLast = items.length ? messageKey(items[items.length - 1]) : null;
    if (switched || (firstKey === null && newFirst !== null)) {
      scroller.scrollTop = scroller.scrollHeight;
    } else if (newFirst !== firstKey) {
      // Página antiga entrou acima: mantém a mesma mensagem sob os olhos do usuário.
      scroller.scrollTop = prevTop + (scroller.scrollHeight - prevHeight);
    } else if (newLast !== lastKey && (nearBottom || items[items.length - 1]?.sender_id === props.me?.id)) {
      // Mensagem nova no fim: acompanha se o usuário já estava no fim ou se foi ele quem enviou.
      scroller.scrollTop = scroller.scrollHeight;
    }
    firstKey = newFirst;
    lastKey = newLast;

    // Se o histórico ainda não enche a área visível, carrega mais sem esperar rolagem.
    if (scroller.scrollHeight <= scroller.clientHeight + LOAD_OLDER_THRESHOLD) requestOlder();
  }

  update(props);
  return { el, update, destroy: () => input.destroy() };
}

function emptyState(iconName, titleText, text) {
  return h(
    'div',
    { class: 'empty-state' },
    h('span', { class: 'empty-state__icon' }, icon(iconName)),
    h('p', { class: 'empty-state__title' }, titleText),
    h('p', { class: 'empty-state__text' }, text),
  );
}
