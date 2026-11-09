import { loadOlderMessages, logout, openConversation, retryMessage, sendMessage } from '../actions.js';
import { activeConversation, messagesOf, store } from '../state.js';
import { createAvatar } from '../components/avatar.js';
import { createChatWindow } from '../components/chat-window.js';
import { createConnectionIndicator } from '../components/connection-indicator.js';
import { createConversationList } from '../components/conversation-list.js';
import { h, setText } from '../utils/dom.js';
import { icon } from '../utils/icons.js';

/**
 * Layout principal: barra lateral (usuário + conversas) e área da conversa aberta.
 * Assina o store e repassa a cada componente só a fatia de estado que ele usa.
 */
export function createChatScreen() {
  const state = store.get();

  const meAvatar = createAvatar({ id: state.user.id, name: state.user.display_name, size: 'sm' });
  const meName = h('span', { class: 'sidebar__name' });
  const meUsername = h('span', { class: 'sidebar__username' });
  const logoutButton = h(
    'button',
    { class: 'button button--icon', type: 'button', 'aria-label': 'Sair', title: 'Sair', on: { click: () => logout() } },
    icon('logout'),
  );

  const list = createConversationList({
    conversations: state.conversations,
    activeId: state.activeConversationId,
    me: state.user,
    loaded: state.conversationsLoaded,
    onSelect: (id) => openConversation(id),
  });

  const sidebar = h(
    'aside',
    { class: 'sidebar' },
    h(
      'header',
      { class: 'sidebar__header' },
      meAvatar.el,
      h('div', { class: 'sidebar__identity' }, meName, meUsername),
      logoutButton,
    ),
    h('h2', { class: 'sidebar__heading' }, 'Conversas'),
    list.el,
  );

  const indicator = createConnectionIndicator({ state: state.connection });
  const noSelection = h(
    'div',
    { class: 'empty-state' },
    h('span', { class: 'empty-state__icon' }, icon('chat')),
    h('p', { class: 'empty-state__title' }, 'Selecione uma conversa'),
    h('p', { class: 'empty-state__text' }, 'Escolha uma conversa na lista para ver as mensagens.'),
  );
  const pane = h('div', { class: 'chat-screen__pane' }, noSelection);
  const main = h('main', { class: 'chat-screen__main' }, indicator.el, pane);
  const el = h('div', { class: 'chat-screen' }, sidebar, main);

  let chatWindow = null;

  function render(current, prev) {
    setText(meName, current.user.display_name);
    setText(meUsername, `@${current.user.username}`);

    if (!prev || current.connection !== prev.connection) indicator.update({ state: current.connection });

    if (
      !prev ||
      current.conversations !== prev.conversations ||
      current.activeConversationId !== prev.activeConversationId ||
      current.conversationsLoaded !== prev.conversationsLoaded
    ) {
      list.update({
        conversations: current.conversations,
        activeId: current.activeConversationId,
        loaded: current.conversationsLoaded,
      });
    }

    const conversation = activeConversation(current);
    if (!conversation) {
      if (chatWindow) {
        chatWindow = null;
        pane.replaceChildren(noSelection);
      }
      return;
    }
    const id = conversation.id;
    const windowProps = {
      conversation,
      me: current.user,
      page: messagesOf(current, id),
      onLoadOlder: () => loadOlderMessages(id),
      onRetry: () => (messagesOf(store.get(), id).loaded ? loadOlderMessages(id) : reopen(id)),
      onSend: (body) => sendMessage(id, body),
      onRetryMessage: (clientId) => retryMessage(clientId),
    };
    if (!chatWindow) {
      chatWindow = createChatWindow(windowProps);
      pane.replaceChildren(chatWindow.el);
    } else if (
      !prev ||
      conversation !== activeConversation(prev) ||
      current.messages[id] !== prev.messages[id]
    ) {
      chatWindow.update(windowProps);
    }
  }

  // Tentar de novo a primeira página: fecha e reabre a conversa.
  function reopen(id) {
    store.set({ activeConversationId: null });
    openConversation(id);
  }

  const unsubscribe = store.subscribe(render);
  render(state, null);

  return {
    el,
    update() {},
    destroy() {
      unsubscribe();
    },
  };
}
