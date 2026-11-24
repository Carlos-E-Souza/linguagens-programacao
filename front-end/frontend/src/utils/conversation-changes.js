// Reaplica eventos que chegaram enquanto a lista inicial era carregada.
// Uma resposta antiga do Go não pode apagar uma conversa ou presença mais recente.
export function applyConversationChange(conversations, change) {
  if (change.type === 'updated') {
    return [...conversations.filter((c) => c.id !== change.conversation.id), change.conversation];
  }
  if (change.type === 'removed') {
    return conversations.filter((c) => c.id !== change.id);
  }
  if (change.type === 'presence') {
    return conversations.map((conversation) =>
      conversation.members?.some((m) => m.user_id === change.userId && m.online !== change.online)
        ? {
            ...conversation,
            members: conversation.members.map((m) =>
              m.user_id === change.userId ? { ...m, online: change.online } : m,
            ),
          }
        : conversation,
    );
  }
  return conversations;
}
