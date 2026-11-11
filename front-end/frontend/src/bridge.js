// Ponte entre a interface e a camada Go. É o único módulo que conhece wailsjs: o resto da interface chama
// bridge.* e bridge.on(EVENTS.x, ...). Fora do Wails (npm run dev no navegador) usa dados falsos de mock-bridge.js.

import * as App from '../wailsjs/go/main/App';
import { EventsOn } from '../wailsjs/runtime/runtime';

/** Nomes dos eventos emitidos pela camada Go (internal/events). */
export const EVENTS = {
  connectionState: 'connection:state',
  messageNew: 'message:new',
  messagesSynced: 'messages:synced',
  messageUpdated: 'message:updated',
  messageStatus: 'message:status',
  conversationUpdated: 'conversation:updated',
  conversationRemoved: 'conversation:removed',
  presence: 'presence',
  sessionExpired: 'session:expired',
};

let impl = null;

/**
 * Escolhe a implementação: dados falsos quando VITE_MOCK=true (ex.: `VITE_MOCK=true wails dev`, útil enquanto o
 * back-end não existe) ou quando os bindings do Wails estão ausentes (navegador); a camada Go nos demais casos.
 */
export async function initBridge() {
  const forceMock = import.meta.env.VITE_MOCK === 'true';
  if (!forceMock && window.go?.main?.App) {
    impl = { ...App, on: EventsOn };
  } else {
    console.info(forceMock ? 'VITE_MOCK=true: usando dados falsos.' : 'Wails ausente: usando dados falsos.');
    impl = (await import('./mock-bridge.js')).createMockBridge();
  }
}

/** Erros do Go chegam como string; normaliza para uma mensagem exibível. */
export function errorMessage(error) {
  if (typeof error === 'string') return error;
  return error?.message || 'Algo deu errado. Tente novamente.';
}

const call = (name) => (...args) => impl[name](...args);

export const bridge = {
  register: call('Register'),
  login: call('Login'),
  restoreSession: call('RestoreSession'),
  logout: call('Logout'),
  currentUser: call('CurrentUser'),
  listConversations: call('ListConversations'),
  loadMessages: call('LoadMessages'),
  sendMessage: call('SendMessage'),
  retryMessage: call('RetryMessage'),
  markAsRead: call('MarkAsRead'),
  searchUsers: call('SearchUsers'),
  createDirectConversation: call('CreateDirectConversation'),
  createGroup: call('CreateGroup'),
  addMembers: call('AddMembers'),
  removeMember: call('RemoveMember'),
  connectionState: call('ConnectionState'),
  /** Escuta um evento; devolve a função que cancela. */
  on: (event, callback) => impl.on(event, callback),
};
