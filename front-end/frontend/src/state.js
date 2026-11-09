// Store da aplicação: um objeto imutável e uma lista de assinantes. Só actions.js chama store.set; telas e
// componentes leem o estado e assinam mudanças.

/** Cria um store. set aceita um objeto parcial ou uma função (estado) → objeto parcial. */
export function createStore(initial) {
  let state = initial;
  const listeners = new Set();
  return {
    get: () => state,
    set(patch) {
      const prev = state;
      const next = typeof patch === 'function' ? patch(state) : patch;
      state = { ...state, ...next };
      for (const listener of listeners) listener(state, prev);
    },
    /** Assina mudanças; devolve a função que cancela a assinatura. */
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
}

/**
 * Estado inicial.
 * - screen: 'loading' | 'login' | 'chat'
 * - connection: 'connected' | 'connecting' | 'disconnected'
 * - conversations: lista ordenada pela última atividade
 * - messages: { [conversationId]: { items, hasMore, loading, loaded, error } }
 * - notice: aviso exibido na tela de login (ex.: sessão expirada)
 */
export const initialState = {
  screen: 'loading',
  user: null,
  connection: 'disconnected',
  conversations: [],
  conversationsLoaded: false,
  activeConversationId: null,
  messages: {},
  notice: '',
};

export const store = createStore(initialState);

/** Conversa aberta, ou null. */
export function activeConversation(state) {
  return state.conversations.find((c) => c.id === state.activeConversationId) ?? null;
}

/** Página de mensagens de uma conversa (ou um estado vazio). */
export function messagesOf(state, conversationId) {
  return state.messages[conversationId] ?? { items: [], hasMore: true, loading: false, loaded: false, error: '' };
}
