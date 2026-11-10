// Ações da aplicação: o único lugar que chama a ponte e altera o store.
// Fluxo: interface → ação → bridge (Go) → store.set → assinantes redesenham.

import { bridge, EVENTS, errorMessage, initBridge } from './bridge.js';
import { initialState, messagesOf, store } from './state.js';
import { mergeMessage, mergeMessages, sortConversations } from './utils/conversations.js';

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
  bridge.on(EVENTS.messageUpdated, applyMessageUpdate);
  bridge.on(EVENTS.messagesSynced, applySyncedMessages);
  bridge.on(EVENTS.conversationRemoved, ({ conversation_id: id }) => removeConversation(id));
  bridge.on(EVENTS.sessionExpired, () => resetToLogin('Sua sessão expirou. Entre novamente para continuar.'));
  bridge.on(EVENTS.error, ({ message }) => console.warn('Erro da camada Go:', message));
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
  store.set({ ...initialState, screen: 'login', notice });
}

async function enterChat(user) {
  store.set({ screen: 'chat', user, notice: '', connection: await bridge.connectionState() });
  await loadConversations();
}

export async function loadConversations() {
  try {
    const conversations = await bridge.listConversations();
    store.set({ conversations: sortConversations(conversations), conversationsLoaded: true });
  } catch (error) {
    console.error(error);
    store.set({ conversationsLoaded: true });
  }
}

function upsertConversation(conversation) {
  store.set((state) => {
    const others = state.conversations.filter((c) => c.id !== conversation.id);
    const messages = conversation.last_message
      ? updateMessages(state, conversation.id, (page) => ({
          ...page,
          // Só junta a última mensagem se a página mais recente já estiver carregada.
          items: page.loaded ? mergeMessages(page.items, [conversation.last_message]) : page.items,
        }))
      : state.messages;
    return { conversations: sortConversations([...others, conversation]), messages };
  });
}

function removeConversation(id) {
  store.set((state) => {
    const { [id]: _removed, ...messages } = state.messages;
    return {
      conversations: state.conversations.filter((c) => c.id !== id),
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
