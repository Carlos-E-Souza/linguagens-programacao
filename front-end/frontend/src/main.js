// Ponto de entrada: carrega os estilos, inicia a aplicação e troca de tela conforme store.screen.

import './styles/tokens.css';
import './styles/base.css';
import './styles/login.css';
import './styles/chat.css';

import { boot } from './actions.js';
import { store } from './state.js';
import { createChatScreen } from './screens/chat.js';
import { createLoginScreen } from './screens/login.js';
import { h } from './utils/dom.js';

const root = document.querySelector('#app');
let screen = null;
let screenName = null;

function mount(name, state) {
  screen?.destroy();
  screenName = name;
  if (name === 'login') screen = createLoginScreen({ notice: state.notice });
  else if (name === 'chat') screen = createChatScreen();
  else screen = { el: h('div', { class: 'splash', role: 'status' }, h('span', { class: 'spinner' }), h('span', { class: 'visually-hidden' }, 'Carregando')), update() {}, destroy() {} };
  root.replaceChildren(screen.el);
}

store.subscribe((state) => {
  if (state.screen !== screenName) mount(state.screen, state);
  else if (state.screen === 'login') screen.update({ notice: state.notice });
});

mount(store.get().screen, store.get());
boot();
