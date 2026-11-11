import { h, reconcileChildren, setText, toggleClass } from '../utils/dom.js';
import { conversationTitle } from '../utils/conversations.js';
import { createAvatar } from './avatar.js';
import { createMemberPicker } from './member-picker.js';
import { createModal } from './modal.js';

// Ações destrutivas pedem um segundo clique; depois deste tempo o botão volta ao normal.
const CONFIRM_MS = 4000;

/**
 * Detalhes do grupo: membros com presença e papel, adicionar pessoas, remover membro (só administradores) e sair.
 * Acompanha a conversa ao vivo: o dono chama update({ conversation }) a cada mudança no store.
 * props: { conversation, me, search(term), onAdd(memberIds), onRemove(userId), onLeave(), onClose() } — as três
 * ações devolvem Promise e rejeitam com Error exibível.
 */
export function createGroupDetails(props) {
  const summary = h('p', { class: 'group-details__summary' });
  const list = h('ul', { class: 'group-details__members', 'aria-label': 'Membros' });
  const error = h('p', { class: 'modal__error', role: 'alert', hidden: true });

  const picker = createMemberPicker({
    search: (term) => props.search(term),
    label: 'Adicionar pessoas ao grupo',
    onChange: refreshAdd,
  });
  const addButton = h('button', { class: 'button button--primary', type: 'button', on: { click: add } }, 'Adicionar');
  const addSection = h(
    'section',
    { class: 'group-details__add', 'aria-label': 'Adicionar pessoas' },
    picker.el,
    h('div', { class: 'group-details__actions' }, addButton),
  );

  const leaveButton = h('button', { class: 'button button--danger', type: 'button', on: { click: leave } }, 'Sair do grupo');

  const modal = createModal({
    title: '',
    body: [summary, list, addSection, error],
    footer: leaveButton,
    onClose: () => props.onClose(),
  });

  const rows = new Map();
  let busy = false;
  let confirming = null; // id do membro (ou 'leave') aguardando o segundo clique
  let confirmTimer = null;

  function showError(message) {
    setText(error, message);
    error.hidden = !message;
  }

  function isAdmin() {
    return props.conversation.members?.some((m) => m.user_id === props.me.id && m.role === 'admin');
  }

  function askConfirm(key) {
    confirming = key;
    clearTimeout(confirmTimer);
    confirmTimer = setTimeout(() => {
      confirming = null;
      render();
    }, CONFIRM_MS);
    render();
  }

  async function run(action) {
    busy = true;
    showError('');
    render();
    try {
      await action();
    } catch (err) {
      showError(err?.message || 'Não foi possível concluir a ação.');
    } finally {
      busy = false;
      confirming = null;
      clearTimeout(confirmTimer);
      render();
    }
  }

  function remove(userId) {
    if (confirming !== userId) return askConfirm(userId);
    run(() => props.onRemove(userId));
  }

  function leave() {
    if (confirming !== 'leave') return askConfirm('leave');
    run(() => props.onLeave());
  }

  function add() {
    const ids = picker.selected().map((u) => u.id);
    if (ids.length === 0) return;
    run(async () => {
      await props.onAdd(ids);
      picker.clear();
    });
  }

  function refreshAdd() {
    addButton.disabled = busy || picker.selected().length === 0;
    const count = picker.selected().length;
    setText(addButton, count > 1 ? `Adicionar ${count} pessoas` : 'Adicionar');
  }

  function rowFor(member) {
    let row = rows.get(member.user_id);
    if (!row) {
      const avatar = createAvatar({ id: member.user_id, size: 'sm' });
      const name = h('span', { class: 'group-details__name' });
      const meta = h('span', { class: 'group-details__meta' });
      const role = h('span', { class: 'group-details__role' }, 'Administrador');
      const removeButton = h('button', {
        class: 'button button--link group-details__remove',
        type: 'button',
        on: { click: () => remove(member.user_id) },
      });
      const li = h(
        'li',
        { class: 'group-details__member' },
        avatar.el,
        h('span', { class: 'group-details__text' }, name, meta),
        role,
        removeButton,
      );
      row = { li, avatar, name, meta, role, removeButton };
      rows.set(member.user_id, row);
    }
    return row;
  }

  function render() {
    const { conversation, me } = props;
    const admin = isAdmin();
    const members = [...(conversation.members ?? [])].sort(
      (a, b) =>
        (b.user_id === me.id) - (a.user_id === me.id) ||
        (b.role === 'admin') - (a.role === 'admin') ||
        (a.display_name || a.username || '').localeCompare(b.display_name || b.username || '', 'pt-BR'),
    );
    const online = members.filter((m) => m.user_id !== me.id && m.online).length;

    modal.update({ title: conversationTitle(conversation, me) });
    setText(
      summary,
      `${members.length} ${members.length === 1 ? 'membro' : 'membros'}` +
        (online ? ` · ${online} online` : ''),
    );

    const seen = new Set();
    const nodes = members.map((member) => {
      seen.add(member.user_id);
      const row = rowFor(member);
      const self = member.user_id === me.id;
      const name = member.display_name || member.username || 'Ex-membro';
      row.avatar.update({ name, online: self ? undefined : member.online });
      setText(row.name, self ? `${name} (você)` : name);
      setText(row.meta, `@${member.username ?? ''}${self ? '' : member.online ? ' · online' : ' · offline'}`);
      row.role.hidden = member.role !== 'admin';
      row.removeButton.hidden = !admin || self;
      row.removeButton.disabled = busy;
      const asking = confirming === member.user_id;
      setText(row.removeButton, asking ? 'Confirmar remoção' : 'Remover');
      toggleClass(row.removeButton, 'group-details__remove--confirm', asking);
      row.removeButton.setAttribute('aria-label', asking ? `Confirmar remoção de ${name}` : `Remover ${name} do grupo`);
      return row.li;
    });
    for (const id of rows.keys()) if (!seen.has(id)) rows.delete(id);
    reconcileChildren(list, nodes);

    picker.update({ excludeIds: members.map((m) => m.user_id), disabled: busy });
    refreshAdd();

    leaveButton.disabled = busy;
    setText(leaveButton, confirming === 'leave' ? 'Clique de novo para sair' : 'Sair do grupo');
  }

  function update(next) {
    props = { ...props, ...next };
    render();
  }

  update(props);
  return {
    el: modal.el,
    update,
    open: () => modal.open(),
    destroy() {
      clearTimeout(confirmTimer);
      picker.destroy();
      modal.destroy();
    },
  };
}
