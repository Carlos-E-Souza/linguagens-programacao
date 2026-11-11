import {
  addMembers,
  createGroup,
  leaveGroup,
  loadOlderMessages,
  logout,
  openConversation,
  removeMember,
  retryMessage,
  searchUsers,
  sendMessage,
  startDirectConversation,
} from '../actions.js';
import { activeConversation, messagesOf, store, totalUnread } from '../state.js';
import { createAvatar } from '../components/avatar.js';
import { createChatWindow } from '../components/chat-window.js';
import { createConnectionIndicator } from '../components/connection-indicator.js';
import { createConversationList } from '../components/conversation-list.js';
import { createGroupDetails } from '../components/group-details.js';
import { createGroupModal } from '../components/group-modal.js';
import { createNewConversationModal } from '../components/new-conversation-modal.js';
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
    h(
      'div',
      { class: 'sidebar__heading-row' },
      h('h2', { class: 'sidebar__heading' }, 'Conversas'),
      h(
        'button',
        {
          class: 'button button--icon',
          type: 'button',
          'aria-label': 'Nova conversa',
          title: 'Nova conversa',
          on: { click: () => showModal('new-conversation') },
        },
        icon('plus'),
      ),
      h(
        'button',
        {
          class: 'button button--icon',
          type: 'button',
          'aria-label': 'Novo grupo',
          title: 'Novo grupo',
          on: { click: () => showModal('group') },
        },
        icon('users'),
      ),
    ),
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
  // Janela modal aberta: { kind: 'new-conversation' | 'group' | 'details', view, conversationId }.
  let modal = null;

  function showModal(kind) {
    closeModal();
    const current = store.get();
    const onClose = closeModal;
    let view;
    if (kind === 'new-conversation') {
      view = createNewConversationModal({
        search: searchUsers,
        onStart: async (userId) => {
          await startDirectConversation(userId);
          closeModal();
        },
        onClose,
      });
    } else if (kind === 'group') {
      view = createGroupModal({
        search: searchUsers,
        onCreate: async (name, ids) => {
          await createGroup(name, ids);
          closeModal();
        },
        onClose,
      });
    } else {
      const conversation = activeConversation(current);
      if (!conversation || conversation.type !== 'group') return;
      const id = conversation.id;
      view = createGroupDetails({
        conversation,
        me: current.user,
        search: searchUsers,
        onAdd: (ids) => addMembers(id, ids),
        onRemove: (userId) => removeMember(id, userId),
        onLeave: async () => {
          await leaveGroup(id);
          closeModal();
        },
        onClose,
      });
      modal = { kind, view, conversationId: id };
      view.open();
      return;
    }
    modal = { kind, view };
    view.open();
  }

  function closeModal() {
    if (!modal) return;
    const { view } = modal;
    modal = null;
    view.destroy();
  }

  function render(current, prev) {
    setText(meName, current.user.display_name);
    setText(meUsername, `@${current.user.username}`);

    if (!prev || current.connection !== prev.connection) indicator.update({ state: current.connection });

    const unread = totalUnread(current);
    document.title = unread > 0 ? `(${unread > 99 ? '99+' : unread}) Chat` : 'Chat';

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

    // Detalhes do grupo acompanham a conversa; se ela sumiu (o usuário saiu ou foi removido), a janela fecha.
    if (modal?.kind === 'details') {
      const shown = current.conversations.find((c) => c.id === modal.conversationId);
      if (!shown) closeModal();
      else if (!prev || shown !== prev.conversations.find((c) => c.id === modal.conversationId)) {
        modal.view.update({ conversation: shown });
      }
    }

    if (!conversation) {
      if (chatWindow) {
        chatWindow.destroy();
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
      onShowDetails: () => showModal('details'),
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
      closeModal();
      chatWindow?.destroy();
      document.title = 'Chat';
    },
  };
}
