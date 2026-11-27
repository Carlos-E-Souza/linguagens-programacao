/** Guarda o texto atual na memória e envia as gravações ao Go na mesma ordem. */
export function createDraftStore(loadFromDisk, saveToDisk) {
  const values = new Map();
  let lastWrite = Promise.resolve();

  return {
    peek: (conversationId) => values.get(conversationId),

    async load(conversationId) {
      if (values.has(conversationId)) return values.get(conversationId);
      await lastWrite.catch(() => {});
      if (values.has(conversationId)) return values.get(conversationId);
      const saved = await loadFromDisk(conversationId);
      // Se a pessoa digitou enquanto o banco respondia, o texto novo prevalece.
      if (!values.has(conversationId)) values.set(conversationId, saved);
      return values.get(conversationId);
    },

    save(conversationId, body) {
      values.set(conversationId, body);
      // Uma falha anterior não impede a próxima tentativa de salvar.
      lastWrite = lastWrite.catch(() => {}).then(() => saveToDisk(conversationId, body));
      return lastWrite;
    },

    flush: () => lastWrite,
  };
}
