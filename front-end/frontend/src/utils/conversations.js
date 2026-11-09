// Regras de apresentação de conversas e usuários, compartilhadas pelos componentes.

/** O outro participante de uma conversa individual. */
export function otherMember(conversation, me) {
  return conversation.members?.find((m) => m.user_id !== me?.id) ?? null;
}

/** Nome exibido da conversa: nome do grupo ou do outro usuário. */
export function conversationTitle(conversation, me) {
  if (conversation.type === 'group') return conversation.name || 'Grupo sem nome';
  const other = otherMember(conversation, me);
  return other?.display_name || other?.username || 'Conversa';
}

/** Nome de quem enviou a mensagem, a partir dos membros da conversa. */
export function senderName(conversation, senderId, me) {
  if (senderId === me?.id) return 'Você';
  const member = conversation?.members?.find((m) => m.user_id === senderId);
  return member?.display_name || member?.username || 'Ex-membro';
}

/** Até duas iniciais do nome. */
export function initials(name) {
  const words = String(name ?? '').trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) return '?';
  const first = words[0][0];
  const last = words.length > 1 ? words[words.length - 1][0] : words[0][1] ?? '';
  return (first + last).toUpperCase();
}

/** Matiz estável (0–359) para a cor do avatar, derivado do id. */
export function avatarHue(id) {
  return (Number(id) * 137) % 360;
}

/** Data da última atividade, usada para ordenar a lista. */
export function lastActivity(conversation) {
  return conversation.last_message?.created_at || conversation.created_at || '';
}

/** Ordena da atividade mais recente para a mais antiga. */
export function sortConversations(conversations) {
  return [...conversations].sort((a, b) => {
    const diff = new Date(lastActivity(b)) - new Date(lastActivity(a));
    return diff || b.id - a.id;
  });
}

/** Subtítulo do cabeçalho: presença na individual, membros no grupo. */
export function conversationSubtitle(conversation, me) {
  if (conversation.type === 'group') {
    const names = (conversation.members ?? []).map((m) => (m.user_id === me?.id ? 'você' : m.display_name));
    const count = names.length;
    if (count === 0) return 'Grupo';
    const shown = names.slice(0, 4);
    const rest = count - shown.length;
    const list = rest > 0 ? `${shown.join(', ')} e mais ${rest}` : joinNames(shown);
    return `${count} ${count === 1 ? 'membro' : 'membros'}: ${list}`;
  }
  return otherMember(conversation, me)?.online ? 'Online' : 'Offline';
}

function joinNames(names) {
  if (names.length <= 1) return names.join('');
  return `${names.slice(0, -1).join(', ')} e ${names[names.length - 1]}`;
}

/** Chave estável de uma mensagem (o client_id existe desde o envio, antes do id do servidor). */
export function messageKey(message) {
  return message.client_id || `id-${message.id}`;
}

// Ordem dos status: uma atualização nunca faz a mensagem voltar (ex.: read → sent). failed só entra explicitamente.
const STATUS_RANK = { pending: 0, sent: 1, delivered: 2, read: 3 };

/** Combina duas versões da mesma mensagem: campos novos vencem, mas status, id e sequência nunca regridem. */
export function mergeMessage(current, incoming) {
  if (!current) return incoming;
  const merged = { ...current, ...incoming };
  merged.id = incoming.id || current.id;
  merged.sequence = incoming.sequence || current.sequence;
  if (incoming.status !== 'failed' && (STATUS_RANK[current.status] ?? -1) > (STATUS_RANK[incoming.status] ?? -1)) {
    merged.status = current.status;
  }
  if (merged.status !== 'failed') delete merged.error;
  return merged;
}

/** Junta duas listas de mensagens sem duplicatas, ordenadas por sequência; pendentes (sem sequência) ficam no fim. */
export function mergeMessages(current, incoming) {
  const byKey = new Map(current.map((m) => [messageKey(m), m]));
  for (const message of incoming) byKey.set(messageKey(message), mergeMessage(byKey.get(messageKey(message)), message));
  const order = (m) => m.sequence || Number.MAX_SAFE_INTEGER;
  return [...byKey.values()].sort((a, b) => order(a) - order(b));
}
