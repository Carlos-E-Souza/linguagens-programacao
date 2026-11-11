// Ações da aplicação: o único lugar que chama a ponte e altera o store.
// Fluxo: interface → ação → bridge (Go) → store.set → assinantes redesenham.

import { bridge, EVENTS, errorMessage, initBridge } from './bridge.js';
import { initialState, messagesOf, store } from './state.js';
import { applyConversationChange } from './utils/conversation-changes.js';
import { mergeMessage, mergeMessages, sortConversations } from './utils/conversations.js';

// Intervalo mínimo para repetir MarkAsRead da mesma posição quando o servidor ainda indica não lidas.
const MARK_READ_RETRY_MS = 2000;
const pendingConversationLoads = new Set();

function rememberConversationChange(change) {
  for (const changes of pendingConversationLoads) changes.push(change);
}

/** Inicia a ponte, registra os eventos do Go e tenta restaurar a sessão salva. */
export async function boot() {
  await initBridge();
  listenToBackend();
  try {
    const user = await bridge.restoreSession();
    if (user) await enterChat(user);
    else store.set({ screen: 'login' });
  } catch (error) {
    console.error(error);
    store.set({ screen: 'login' });
  }
}

function listenToBackend() {
  bridge.on(EVENTS.connectionState, (connection) => store.set({ connection }));
  bridge.on(EVENTS.conversationUpdated, upsertConversation);
  bridge.on(EVENTS.messageNew, applyIncomingMessage);
  bridge.on(EVENTS.messageUpdated, applyMessageUpdate);
  bridge.on(EVENTS.messageStatus, applyReceipt);
  bridge.on(EVENTS.messagesSynced, applySyncedMessages);
  bridge.on(EVENTS.conversationRemoved, ({ conversation_id: id }) => removeConversation(id));
  bridge.on(EVENTS.presence, applyPresence);
  bridge.on(EVENTS.sessionExpired, () => resetToLogin('Sua sessão expirou. Entre novamente para continuar.'));
  // Voltar para a janela conta como ver a conversa aberta.
  window.addEventListener('focus', markActiveAsRead);
  document.addEventListener('visibilitychange', markActiveAsRead);
}

/** Entra com usuário e senha. Erros sobem para a tela exibir. */
export async function login(username, password) {
  const user = await bridge.login(username, password);
  await enterChat(user);
}

/** Cria a conta e entra em seguida. */
export async function register(username, displayName, password) {
  await bridge.register(username, displayName, password);
  await login(username, password);
}

export async function logout() {
  await bridge.logout();
  resetToLogin('');
}

function resetToLogin(notice) {
  lastMarked.clear();
  store.set({ ...initialState, screen: 'login', notice });
}

async function enterChat(user) {
  store.set({ screen: 'chat', user, notice: '', connection: await bridge.connectionState() });
  await loadConversations();
}

export async function loadConversations() {
  const userId = store.get().user?.id;
  const changes = [];
  pendingConversationLoads.add(changes);
  try {
    const conversations = await bridge.listConversations();
    if (store.get().user?.id !== userId) return;
    const latest = changes.reduce(applyConversationChange, conversations);
    store.set({ conversations: sortConversations(latest), conversationsLoaded: true });
  } catch (error) {
    console.error(error);
    if (store.get().user?.id === userId) store.set({ conversationsLoaded: true });
  } finally {
    pendingConversationLoads.delete(changes);
  }
}

function upsertConversation(conversation) {
  rememberConversationChange({ type: 'updated', conversation });
  store.set((state) => {
    const messages = conversation.last_message
      ? updateMessages(state, conversation.id, (page) => ({
          ...page,
          // Só junta a última mensagem se a página mais recente já estiver carregada.
          items: page.loaded ? mergeMessages(page.items, [conversation.last_message]) : page.items,
        }))
      : state.messages;
    return {
      conversations: sortConversations(applyConversationChange(state.conversations, { type: 'updated', conversation })),
      messages,
    };
  });
  // Ex.: não lidas que voltaram do servidor depois de uma leitura feita sem conexão.
  if (conversation.id === store.get().activeConversationId && conversation.unread_count > 0) markActiveAsRead();
}

function removeConversation(id) {
  rememberConversationChange({ type: 'removed', id });
  store.set((state) => {
    const { [id]: _removed, ...messages } = state.messages;
    return {
      conversations: applyConversationChange(state.conversations, { type: 'removed', id }),
      activeConversationId: state.activeConversationId === id ? null : state.activeConversationId,
      messages,
    };
  });
}

/** Abre uma conversa e carrega as mensagens mais recentes, se ainda não carregadas. */
export async function openConversation(id) {
  if (store.get().activeConversationId === id) return;
  store.set({ activeConversationId: id });
  if (!messagesOf(store.get(), id).loaded) await loadPage(id, 0);
  markActiveAsRead();
}

/** Carrega a página anterior à mensagem mais antiga exibida (rolagem infinita para cima). */
export async function loadOlderMessages(id) {
  const page = messagesOf(store.get(), id);
  if (page.loading || !page.hasMore) return;
  const oldest = page.items.find((m) => m.sequence > 0);
  if (!oldest) return;
  await loadPage(id, oldest.sequence);
}

async function loadPage(id, before) {
  store.set((state) => ({ messages: updateMessages(state, id, (page) => ({ ...page, loading: true, error: '' })) }));
  try {
    const items = await bridge.loadMessages(id, before);
    store.set((state) => ({
      messages: updateMessages(state, id, (page) => {
        const merged = mergeMessages(page.items, items);
        const oldest = merged.find((m) => m.sequence > 0);
        return { items: merged, loading: false, loaded: true, error: '', hasMore: Boolean(oldest && oldest.sequence > 1) };
      }),
    }));
  } catch (error) {
    store.set((state) => ({
      messages: updateMessages(state, id, (page) => ({ ...page, loading: false, error: errorMessage(error) })),
    }));
  }
}

function updateMessages(state, id, fn) {
  return { ...state.messages, [id]: fn(messagesOf(state, id)) };
}

/** Junta de uma vez as mensagens recuperadas após voltar a conexão. */
function applySyncedMessages({ conversation_id: id, messages }) {
  store.set((state) => ({
    messages: updateMessages(state, id, (page) => ({
      ...page,
      items: page.loaded ? mergeMessages(page.items, messages) : page.items,
    })),
  }));
  if (id === store.get().activeConversationId) markActiveAsRead();
}

/** Mensagem recebida ao vivo. A prévia e as não lidas chegam em seguida por conversation:updated. */
function applyIncomingMessage(message) {
  const id = message.conversation_id;
  store.set((state) => {
    const page = messagesOf(state, id);
    if (!page.loaded) return {};
    return { messages: updateMessages(state, id, () => ({ ...page, items: mergeMessages(page.items, [message]) })) };
  });
  if (id === store.get().activeConversationId) markActiveAsRead();
}

const STATUS_RANK = { sent: 1, delivered: 2, read: 3 };

/**
 * Entregue/lida das minhas mensagens até up_to_sequence. A camada Go já calcula a posição comum a todos os outros
 * membros, então em grupos o status só avança quando todos confirmaram.
 */
function applyReceipt({ conversation_id: id, up_to_sequence: upTo, status }) {
  const me = store.get().user?.id;
  const promote = (m) =>
    m.sender_id === me && m.sequence > 0 && m.sequence <= upTo && (STATUS_RANK[m.status] ?? 99) < STATUS_RANK[status]
      ? { ...m, status }
      : m;
  store.set((state) => {
    const page = messagesOf(state, id);
    if (!page.loaded) return {};
    const items = page.items.map(promote);
    if (items.every((m, i) => m === page.items[i])) return {};
    return { messages: updateMessages(state, id, () => ({ ...page, items })) };
  });
}

/** Contato entrou ou saiu: atualiza o membro em todas as conversas. */
function applyPresence({ user_id: userId, online }) {
  rememberConversationChange({ type: 'presence', userId, online });
  store.set((state) => ({
    conversations: applyConversationChange(state.conversations, { type: 'presence', userId, online }),
  }));
}

// Última posição enviada em MarkAsRead por conversa: { upTo, at }.
const lastMarked = new Map();

/**
 * Marca a conversa aberta como lida até a maior sequência carregada, se a janela estiver em foco e visível.
 * Só chama a camada Go quando a posição avançou ou quando ainda há não lidas.
 */
export function markActiveAsRead() {
  const state = store.get();
  const id = state.activeConversationId;
  if (id == null || !document.hasFocus() || document.visibilityState !== 'visible') return;
  const page = messagesOf(state, id);
  if (!page.loaded) return;
  const upTo = page.items.reduce((max, m) => Math.max(max, m.sequence || 0), 0);
  if (upTo === 0) return;
  const unread = state.conversations.find((c) => c.id === id)?.unread_count ?? 0;
  const last = lastMarked.get(id);
  if (last && upTo <= last.upTo && (unread === 0 || Date.now() - last.at < MARK_READ_RETRY_MS)) return;
  lastMarked.set(id, { upTo: Math.max(upTo, last?.upTo ?? 0), at: Date.now() });
  bridge.markAsRead(id, upTo).catch((error) => console.error(errorMessage(error)));
}

// ACKs que chegam antes de a chamada SendMessage terminar (a fila envia na hora). Aplicados quando a mensagem
// pendente entra no store.
const earlyUpdates = new Map();

/**
 * Envia uma mensagem: ela aparece na hora como pendente e o ACK chega depois por message:updated.
 * Rejeita com um Error de mensagem exibível se a camada Go não conseguir guardar a mensagem.
 */
export async function sendMessage(conversationId, body) {
  let message;
  try {
    message = await bridge.sendMessage(conversationId, body);
  } catch (error) {
    throw new Error(errorMessage(error));
  }
  const early = earlyUpdates.get(message.client_id);
  earlyUpdates.delete(message.client_id);
  putOwnMessage(early ? mergeMessage(message, early) : message);
}

/** Coloca de novo na fila uma mensagem que falhou. */
export async function retryMessage(clientId) {
  try {
    putOwnMessage(await bridge.retryMessage(clientId));
  } catch (error) {
    console.error(errorMessage(error));
  }
}

/** ACK (sent) ou falha definitiva (failed) de uma mensagem enviada. */
function applyMessageUpdate(update) {
  const page = messagesOf(store.get(), update.conversation_id);
  const known = page.items.some((m) => m.client_id === update.client_id);
  if (!known) {
    // Página aberta mas mensagem ainda não chegou ao store: é um ACK adiantado. Sem página carregada, o cache da
    // camada Go já tem o estado certo para quando ela for aberta.
    if (page.loaded) earlyUpdates.set(update.client_id, update);
    return;
  }
  putOwnMessage(update);
}

/** Insere ou atualiza uma mensagem própria na página da conversa e na prévia da lista. */
function putOwnMessage(message) {
  store.set((state) => {
    const id = message.conversation_id;
    const messages = updateMessages(state, id, (page) => ({ ...page, items: mergeMessages(page.items, [message]) }));
    const conversations = state.conversations.map((conversation) => {
      if (conversation.id !== id) return conversation;
      const last = conversation.last_message;
      // A prévia acompanha a mensagem nova, ou a atualização da mensagem que já é a última.
      if (last && last.client_id === message.client_id) return { ...conversation, last_message: mergeMessage(last, message) };
      if (last && message.status !== 'pending') return conversation;
      return { ...conversation, last_message: messages[id].items.at(-1) ?? message };
    });
    return { messages, conversations: sortConversations(conversations) };
  });
}

/** Busca pessoas para conversas e grupos. Rejeita com Error exibível. */
export async function searchUsers(term) {
  try {
    return await bridge.searchUsers(term);
  } catch (error) {
    throw new Error(errorMessage(error));
  }
}

/** Abre a conversa individual com a pessoa, criando-a se preciso. */
export async function startDirectConversation(userId) {
  await enterConversation(() => bridge.createDirectConversation(userId));
}

/** Cria um grupo e abre a conversa. */
export async function createGroup(name, memberIds) {
  await enterConversation(() => bridge.createGroup(name, memberIds));
}

async function enterConversation(create) {
  let conversation;
  try {
    conversation = await create();
  } catch (error) {
    throw new Error(errorMessage(error));
  }
  upsertConversation(conversation);
  await openConversation(conversation.id);
}

/** Adiciona pessoas ao grupo. A conversa atualizada chega por conversation:updated. */
export async function addMembers(conversationId, memberIds) {
  try {
    await bridge.addMembers(conversationId, memberIds);
  } catch (error) {
    throw new Error(errorMessage(error));
  }
}

/** Remove um membro do grupo (só administradores). */
export async function removeMember(conversationId, userId) {
  try {
    await bridge.removeMember(conversationId, userId);
  } catch (error) {
    throw new Error(errorMessage(error));
  }
}

/** Sai do grupo: a conversa some da lista. */
export async function leaveGroup(conversationId) {
  await removeMember(conversationId, store.get().user.id);
  removeConversation(conversationId);
}
