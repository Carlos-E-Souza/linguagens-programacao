import { h, reconcileChildren, setText, toggleClass } from '../utils/dom.js';
import { formatListTime } from '../utils/dates.js';
import { icon } from '../utils/icons.js';
import { conversationTitle, lastActivity, otherMember, senderName } from '../utils/conversations.js';
import { createAvatar } from './avatar.js';

/**
 * Lista de conversas da barra lateral. Mantém um item por conversa (chave = id) e só atualiza o que mudou.
 * props: { conversations, activeId, me, loaded, onSelect(id) }
 */
export function createConversationList(props) {
  const list = h('ul', { class: 'conversation-list__items', 'aria-label': 'Conversas' });
  const empty = h(
    'div',
    { class: 'empty-state conversation-list__empty' },
    h('span', { class: 'empty-state__icon' }, icon('inbox')),
    h('p', { class: 'empty-state__title' }, 'Nenhuma conversa ainda'),
    h('p', { class: 'empty-state__text' }, 'Quando alguém conversar com você, a conversa aparece aqui.'),
  );
  const el = h('nav', { class: 'conversation-list' }, list, empty);
  const items = new Map();

  function createItem(id) {
    const avatar = createAvatar({ id });
    const title = h('span', { class: 'conversation-list__title' });
    const time = h('time', { class: 'conversation-list__time' });
    const preview = h('span', { class: 'conversation-list__preview' });
    const badge = h('span', { class: 'conversation-list__badge' });
    const button = h(
      'button',
      { class: 'conversation-list__button', type: 'button', on: { click: () => props.onSelect(id) } },
      avatar.el,
      h(
        'span',
        { class: 'conversation-list__text' },
        h('span', { class: 'conversation-list__row' }, title, time),
        h('span', { class: 'conversation-list__row' }, preview, badge),
      ),
    );
    const li = h('li', { class: 'conversation-list__item' }, button);
    return { li, button, avatar, title, time, preview, badge };
  }

  function updateItem(item, conversation) {
    const { me } = props;
    const name = conversationTitle(conversation, me);
    const group = conversation.type === 'group';
    const other = group ? null : otherMember(conversation, me);
    item.avatar.update({ id: group ? conversation.id : other?.user_id, name, group, online: other?.online });
    setText(item.title, name);

    const last = conversation.last_message;
    setText(item.time, formatListTime(lastActivity(conversation)));
    let text = 'Sem mensagens';
    if (last) {
      const own = last.sender_id === me?.id;
      const prefix = own ? 'Você: ' : group ? `${senderName(conversation, last.sender_id, me).split(' ')[0]}: ` : '';
      text = prefix + last.body;
    }
    setText(item.preview, text);

    const unread = conversation.unread_count ?? 0;
    item.badge.hidden = unread === 0;
    setText(item.badge, unread > 99 ? '99+' : String(unread));
    toggleClass(item.button, 'conversation-list__button--unread', unread > 0);

    const active = conversation.id === props.activeId;
    toggleClass(item.button, 'conversation-list__button--active', active);
    if (active) item.button.setAttribute('aria-current', 'true');
    else item.button.removeAttribute('aria-current');

    const status = other ? (other.online ? ', online' : ', offline') : '';
    const unreadLabel = unread > 0 ? `, ${unread} não ${unread === 1 ? 'lida' : 'lidas'}` : '';
    item.button.setAttribute('aria-label', `${name}${status}${unreadLabel}`);
  }

  function update(next) {
    props = { ...props, ...next };
    const seen = new Set();
    const nodes = props.conversations.map((conversation) => {
      seen.add(conversation.id);
      let item = items.get(conversation.id);
      if (!item) {
        item = createItem(conversation.id);
        items.set(conversation.id, item);
      }
      updateItem(item, conversation);
      return item.li;
    });
    for (const id of items.keys()) if (!seen.has(id)) items.delete(id);
    reconcileChildren(list, nodes);
    empty.hidden = !props.loaded || nodes.length > 0;
  }

  update(props);
  return { el, update, destroy() {} };
}
