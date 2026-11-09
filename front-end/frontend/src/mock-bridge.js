// Implementação falsa da ponte, usada fora do Wails (npm run dev) ou com VITE_MOCK=true, para desenvolver a
// interface sem o servidor. Imita as respostas e os eventos da camada Go.
//
// Envio de mensagens: o ACK chega após ~600ms. Para testar outros casos, no console (devtools):
//   mockChat.disconnect() / mockChat.reconnect() — mensagens enviadas offline ficam pendentes até reconectar;
//   uma mensagem que começa com "/falha" é recusada pelo "servidor" (status failed).

const MINUTE = 60 * 1000;
const now = Date.now();
const iso = (msAgo) => new Date(now - msAgo).toISOString();
const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

const me = { id: 1, username: 'ana', display_name: 'Ana Ribeiro' };
const people = {
  2: { username: 'bruno', display_name: 'Bruno Costa', online: true },
  3: { username: 'carla', display_name: 'Carla Mendes', online: false },
  4: { username: 'diego', display_name: 'Diego Lima', online: true },
};
const member = (id, role = 'member') => ({
  user_id: id,
  role,
  last_delivered: 0,
  last_read: 0,
  ...(id === me.id ? { ...me, online: true } : people[id]),
});

const lines = [
  'Bom dia! Conseguiu ver o relatório?',
  'Vi sim, ficou ótimo. Só ajustaria o gráfico da página 3.',
  'Combinado, mexo nisso hoje à tarde.',
  'Reunião continua às 15h?',
  'Continua. Vou mandar o link daqui a pouco.',
  'Alguém revisou o PR do cache local?',
  'Revisei. Deixei dois comentários sobre a paginação por sequência, mas no geral está pronto para entrar.',
  'Perfeito 👍',
];

function makeHistory(conversationId, count, senders, spacingMinutes) {
  return Array.from({ length: count }, (_, i) => {
    const sequence = i + 1;
    return {
      id: conversationId * 1000 + sequence,
      conversation_id: conversationId,
      sequence,
      sender_id: senders[i % senders.length],
      client_id: `mock-${conversationId}-${sequence}`,
      body: lines[i % lines.length],
      created_at: iso((count - i) * spacingMinutes * MINUTE),
      status: 'read',
    };
  });
}

const histories = {
  10: makeHistory(10, 120, [2, 1, 2, 1, 1], 37),
  11: makeHistory(11, 14, [3, 4, 1, 3], 300),
  12: makeHistory(12, 3, [4, 1], 3000),
};
histories[10].at(-1).status = 'delivered';

const conversations = [
  { id: 10, type: 'direct', name: '', created_by: 1, members: [member(1), member(2)], unread_count: 0 },
  { id: 11, type: 'group', name: 'Projeto de LP', created_by: 3, members: [member(1), member(3, 'admin'), member(4)], unread_count: 3 },
  { id: 12, type: 'direct', name: '', created_by: 4, members: [member(1), member(4)], unread_count: 0 },
].map((c) => ({
  ...c,
  created_at: iso(30 * 24 * 60 * MINUTE),
  last_sequence: histories[c.id].length,
  last_message: histories[c.id].at(-1),
}));

export function createMockBridge() {
  const listeners = new Map();
  let loggedIn = false;
  let state = 'disconnected';

  const emit = (event, ...data) => listeners.get(event)?.forEach((cb) => cb(...data));
  const setState = (next) => {
    state = next;
    emit('connection:state', next);
  };
  const connect = async () => {
    setState('connecting');
    await delay(900);
    if (!loggedIn) return;
    setState('connected');
    flushQueue();
  };

  // Fila de envio: mensagens aguardando ACK, na ordem.
  const queue = [];
  const flushQueue = () => {
    for (const message of queue.splice(0)) deliver(message);
  };
  const deliver = async (message) => {
    await delay(600);
    if (state !== 'connected') {
      queue.push(message);
      return;
    }
    const history = histories[message.conversation_id];
    if (message.body.startsWith('/falha')) {
      emit('message:updated', {
        conversation_id: message.conversation_id,
        client_id: message.client_id,
        status: 'failed',
        created_at: message.created_at,
        error: 'Você não participa mais desta conversa.',
      });
      return;
    }
    const sequence = history.length + 1;
    const acked = { ...message, id: message.conversation_id * 1000 + sequence, sequence, status: 'sent', created_at: new Date().toISOString() };
    history.push(acked);
    emit('message:updated', {
      conversation_id: acked.conversation_id,
      client_id: acked.client_id,
      id: acked.id,
      sequence,
      status: 'sent',
      created_at: acked.created_at,
    });
  };
  const sent = new Map();
  const enqueue = (message) => {
    sent.set(message.client_id, message);
    if (state === 'connected') deliver(message);
    else queue.push(message);
  };

  window.mockChat = {
    disconnect: () => setState('disconnected'),
    reconnect: () => connect(),
  };

  return {
    async Register(username, displayName) {
      await delay(400);
      if (username === 'ana') throw 'Esse nome de usuário já está em uso. Escolha outro.';
      return { id: 9, username, display_name: displayName };
    },
    async Login(username, password) {
      await delay(500);
      if (password !== 'senha1234') throw 'Usuário ou senha incorretos.';
      loggedIn = true;
      connect();
      return { ...me, username };
    },
    async RestoreSession() {
      await delay(150);
      return null;
    },
    async Logout() {
      loggedIn = false;
      setState('disconnected');
    },
    async CurrentUser() {
      return loggedIn ? me : null;
    },
    async ConnectionState() {
      return state;
    },
    async ListConversations() {
      await delay(200);
      return structuredClone(conversations);
    },
    async LoadMessages(conversationId, before) {
      await delay(before ? 600 : 250);
      const all = histories[conversationId] ?? [];
      const upper = before || Infinity;
      return structuredClone(all.filter((m) => m.sequence < upper).slice(-50));
    },
    async SendMessage(conversationId, body) {
      const text = body.trim();
      if (!text) throw 'Escreva uma mensagem antes de enviar.';
      const message = {
        id: 0,
        conversation_id: conversationId,
        sequence: 0,
        sender_id: me.id,
        client_id: crypto.randomUUID(),
        body: text,
        created_at: new Date().toISOString(),
        status: 'pending',
      };
      enqueue(message);
      return structuredClone(message);
    },
    async RetryMessage(clientId) {
      const message = sent.get(clientId);
      if (!message) throw 'Esta mensagem não existe mais.';
      // Na nova tentativa o "servidor" aceita, para dar para ver o fluxo completo.
      const retried = { ...message, body: message.body.replace(/^\/falha\s*/, '') || 'mensagem', status: 'pending' };
      enqueue(retried);
      return structuredClone(retried);
    },
    on(event, callback) {
      if (!listeners.has(event)) listeners.set(event, new Set());
      listeners.get(event).add(callback);
      return () => listeners.get(event).delete(callback);
    },
  };
}
