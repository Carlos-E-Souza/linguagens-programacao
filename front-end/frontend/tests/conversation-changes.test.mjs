import assert from 'node:assert/strict';
import test from 'node:test';

import { applyConversationChange } from '../src/utils/conversation-changes.js';

test('eventos novos prevalecem sobre a lista antiga', () => {
  const old = [
    { id: 1, members: [{ user_id: 2, online: false }], unread_count: 0 },
    { id: 3, members: [] },
  ];
  const changes = [
    { type: 'updated', conversation: { id: 1, members: [{ user_id: 2, online: false }], unread_count: 2 } },
    { type: 'presence', userId: 2, online: true },
    { type: 'updated', conversation: { id: 2, members: [], unread_count: 0 } },
    { type: 'removed', id: 3 },
  ];

  const latest = changes.reduce(applyConversationChange, old);
  assert.deepEqual(latest.map((c) => c.id), [1, 2]);
  assert.equal(latest[0].unread_count, 2);
  assert.equal(latest[0].members[0].online, true);
  assert.equal(old[0].members[0].online, false);
});

test('presença offline também prevalece sobre a lista antiga', () => {
  const old = [{ id: 1, members: [{ user_id: 2, online: true }] }];
  const latest = applyConversationChange(old, { type: 'presence', userId: 2, online: false });
  assert.equal(latest[0].members[0].online, false);
});
