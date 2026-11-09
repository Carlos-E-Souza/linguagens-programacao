import { h, setText, toggleClass } from '../utils/dom.js';
import { avatarHue, initials } from '../utils/conversations.js';

/**
 * Avatar com iniciais e, opcionalmente, o ponto de presença.
 * props: { id, name, group, online (true/false/undefined = sem indicador), size: 'sm' | 'md' }
 */
export function createAvatar(props) {
  const presence = h('span', { class: 'avatar__presence' });
  const label = h('span', { 'aria-hidden': 'true' });
  const el = h('span', { class: 'avatar' }, label, presence);

  function update(next) {
    props = { ...props, ...next };
    setText(label, initials(props.name));
    el.style.setProperty('--avatar-hue', avatarHue(props.id));
    toggleClass(el, 'avatar--sm', props.size === 'sm');
    toggleClass(el, 'avatar--group', props.group);
    presence.hidden = props.online !== true;
  }

  update(props);
  return { el, update, destroy() {} };
}
