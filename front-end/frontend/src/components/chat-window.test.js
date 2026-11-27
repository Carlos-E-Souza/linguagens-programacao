import test from 'node:test';
import assert from 'node:assert/strict';
import { createChatWindow } from './chat-window.js';

// DOM mínimo para exercitar a janela sem abrir o aplicativo desktop.
class TestNode {
  constructor(tagName) {
    this.tagName = tagName;
    this.children = [];
    this.listeners = new Map();
    this.style = { height: '', setProperty() {} };
    this.classList = { toggle() {} };
    this.dataset = {};
    this.value = '';
    this.scrollHeight = 0;
    this.clientHeight = 0;
    this.scrollTop = 0;
    this.textContent = '';
  }

  setAttribute() {}
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; }
  addEventListener(name, callback) { this.listeners.set(name, callback); }
  fire(name) { return this.listeners.get(name)?.({ preventDefault() {} }); }
  requestSubmit() { return this.fire('submit'); }
  focus() {}
}

function find(node, tagName) {
  if (node.tagName === tagName) return node;
  for (const child of node.children) {
    if (child instanceof TestNode) {
      const found = find(child, tagName);
      if (found) return found;
    }
  }
  return null;
}

test('envia para a conversa original mesmo quando a pessoa troca de conversa durante a gravação', async (t) => {
  const previousNode = globalThis.Node;
  const previousDocument = globalThis.document;
  globalThis.Node = TestNode;
  globalThis.document = {
    createElement: (tag) => new TestNode(tag),
    createElementNS: (_namespace, tag) => new TestNode(tag),
    createTextNode: (value) => Object.assign(new TestNode('#text'), { textContent: value }),
  };
  t.after(() => {
    globalThis.Node = previousNode;
    globalThis.document = previousDocument;
  });

  let finishFirstSave;
  let firstSaveStarted;
  const started = new Promise((resolve) => { firstSaveStarted = resolve; });
  const sent = [];
  const me = { id: 1, username: 'ana', display_name: 'Ana' };
  const page = { items: [], loaded: true, hasMore: false, loading: false, error: '' };
  const conversation = (id) => ({
    id, type: 'direct', members: [
      { user_id: 1, username: 'ana', display_name: 'Ana' },
      { user_id: id + 1, username: `pessoa${id}`, display_name: `Pessoa ${id}` },
    ],
  });
  const props = (id) => ({
    conversation: conversation(id), me, page,
    onLoadDraft: async () => '',
    onSaveDraft: async (_conversationId, body) => {
      if (body === 'Mensagem para A') {
        await new Promise((resolve) => {
          finishFirstSave = resolve;
          firstSaveStarted();
        });
      }
    },
    onSend: async (body) => { sent.push({ conversationId: id, body }); },
  });

  const chat = createChatWindow(props(1));
  const textarea = find(chat.el, 'textarea');
  const form = find(chat.el, 'form');
  textarea.value = 'Mensagem para A';
  textarea.fire('input');
  const submission = form.requestSubmit();
  await started;

  chat.update(props(2));
  finishFirstSave();
  await submission;

  assert.deepEqual(sent, [{ conversationId: 1, body: 'Mensagem para A' }]);
});
