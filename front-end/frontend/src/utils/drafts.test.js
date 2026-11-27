import test from 'node:test';
import assert from 'node:assert/strict';
import { createDraftStore } from './drafts.js';

test('salvamentos da mesma conversa terminam na ordem em que foram digitados', async () => {
  let releaseFirst;
  let firstStarted;
  const started = new Promise((resolve) => { firstStarted = resolve; });
  const saved = [];
  const drafts = createDraftStore(
    async () => '',
    async (_id, body) => {
      if (body === 'antigo') {
        firstStarted();
        await new Promise((resolve) => { releaseFirst = resolve; });
      }
      saved.push(body);
    },
  );

  const first = drafts.save(1, 'antigo');
  await started;
  const second = drafts.save(1, 'novo');
  releaseFirst();
  await Promise.all([first, second]);

  assert.deepEqual(saved, ['antigo', 'novo']);
  assert.equal(drafts.peek(1), 'novo');
});

test('leitura antiga não substitui texto digitado enquanto ela estava pendente', async () => {
  let finishLoad;
  let loadStarted;
  const started = new Promise((resolve) => { loadStarted = resolve; });
  const drafts = createDraftStore(
    () => {
      loadStarted();
      return new Promise((resolve) => { finishLoad = resolve; });
    },
    async () => {},
  );

  const reading = drafts.load(2);
  await started;
  drafts.save(2, 'texto novo');
  finishLoad('texto antigo');

  assert.equal(await reading, 'texto novo');
  assert.equal(drafts.peek(2), 'texto novo');
});

test('espera a última gravação antes de encerrar a sessão', async () => {
  let finishSave;
  let saveStarted;
  const started = new Promise((resolve) => { saveStarted = resolve; });
  const drafts = createDraftStore(
    async () => '',
    async () => {
      await new Promise((resolve) => {
        finishSave = resolve;
        saveStarted();
      });
    },
  );

  const saving = drafts.save(1, 'texto para depois');
  const flushed = drafts.flush();
  await started;
  let completed = false;
  flushed.then(() => { completed = true; });
  await Promise.resolve();
  assert.equal(completed, false);

  finishSave();
  await Promise.all([saving, flushed]);
  assert.equal(completed, true);
});
