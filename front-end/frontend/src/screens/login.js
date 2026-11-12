import { login, register } from '../actions.js';
import { errorMessage } from '../bridge.js';
import { h, setText, toggleClass } from '../utils/dom.js';
import { icon } from '../utils/icons.js';
import { displayNameError, normalizedUsername, passwordError, usernameError } from '../utils/account-validation.js';

const modes = {
  login: {
    title: 'Entrar',
    subtitle: 'Use seu nome de usuário e senha.',
    submit: 'Entrar',
    busy: 'Entrando…',
    switchText: 'Ainda não tem conta?',
    switchAction: 'Criar conta',
  },
  register: {
    title: 'Criar conta',
    subtitle: 'Escolha como você vai aparecer para os outros.',
    submit: 'Criar conta',
    busy: 'Criando conta…',
    switchText: 'Já tem conta?',
    switchAction: 'Entrar',
  },
};

/**
 * Botão dentro do campo de senha que alterna entre mostrar e ocultar o texto. Devolve { el, reset }.
 */
function createPasswordToggle(input) {
  const button = h('button', {
    class: 'field__toggle',
    type: 'button',
    'aria-controls': input.id,
    on: { click: () => set(input.type === 'password') },
  });

  function set(visible) {
    // Guarda o cursor: trocar o type não deve mover a posição de digitação.
    const { selectionStart, selectionEnd } = input;
    input.type = visible ? 'text' : 'password';
    const label = visible ? 'Ocultar senha' : 'Mostrar senha';
    button.setAttribute('aria-label', label);
    button.setAttribute('aria-pressed', String(visible));
    button.title = label;
    button.replaceChildren(icon(visible ? 'eyeOff' : 'eye'));
    if (document.activeElement === input) input.setSelectionRange(selectionStart, selectionEnd);
  }

  set(false);
  return { el: button, reset: () => set(false) };
}

/** Campo com rótulo, dica e mensagem de erro ligada por aria-describedby. */
function createField({ id, label, type = 'text', autocomplete, hint = '' }) {
  const input = h('input', {
    class: 'field__input',
    id,
    name: id,
    type,
    autocomplete,
    spellcheck: 'false',
    autocapitalize: 'off',
    'aria-describedby': `${id}-hint ${id}-error`,
  });
  const hintEl = h('p', { class: 'field__hint', id: `${id}-hint` }, hint);
  const error = h('p', { class: 'field__error', id: `${id}-error`, 'aria-live': 'polite' });
  const toggle = type === 'password' ? createPasswordToggle(input) : null;
  const control = h('div', { class: 'field__control' }, input, toggle?.el);
  const el = h('div', { class: 'field' }, h('label', { class: 'field__label', for: id }, label), control, hintEl, error);
  hintEl.hidden = !hint;
  return {
    el,
    input,
    /** Volta a ocultar a senha (ao trocar de modo ou após enviar). */
    resetVisibility: () => toggle?.reset(),
    value: () => input.value,
    setError(message) {
      setText(error, message);
      toggleClass(el, 'field--invalid', Boolean(message));
      if (message) input.setAttribute('aria-invalid', 'true');
      else input.removeAttribute('aria-invalid');
    },
  };
}

/**
 * Tela de login e cadastro.
 * props: { notice } — aviso vindo de fora (ex.: sessão expirada).
 */
export function createLoginScreen(props) {
  let mode = 'login';
  let busy = false;

  const username = createField({
    id: 'username',
    label: 'Nome de usuário',
    autocomplete: 'username',
    hint: '3 a 32 letras sem acento, números ou _. Maiúsculas viram minúsculas.',
  });
  username.input.addEventListener('blur', () => {
    if (mode === 'register') {
      username.input.value = normalizedUsername(username.value());
      validateUsername();
    }
  });
  username.input.addEventListener('input', () => username.setError(''));
  const displayName = createField({
    id: 'display-name',
    label: 'Nome de exibição',
    autocomplete: 'name',
    hint: 'Até 64 bytes; acentos podem ocupar mais de um byte.',
  });
  displayName.input.addEventListener('blur', () => {
    if (mode === 'register') validateDisplayName();
  });
  displayName.input.addEventListener('input', () => displayName.setError(''));
  const password = createField({
    id: 'password',
    label: 'Senha',
    type: 'password',
    autocomplete: 'current-password',
    hint: 'De 8 a 72 bytes. Acentos podem ocupar mais de um byte.',
  });
  password.input.addEventListener('blur', () => {
    if (mode === 'register') validatePassword();
  });
  password.input.addEventListener('input', () => password.setError(''));

  const title = h('h1', { class: 'login__title', id: 'login-title' });
  const subtitle = h('p', { class: 'login__subtitle' });
  const alert = h('p', { class: 'login__alert', role: 'alert' });
  const notice = h('p', { class: 'login__notice', role: 'status' });
  const submitLabel = h('span');
  const spinner = h('span', { class: 'spinner', 'aria-hidden': 'true' });
  const submit = h('button', { class: 'button button--primary button--block', type: 'submit' }, spinner, submitLabel);
  const switchText = h('span');
  const switchButton = h('button', { class: 'button button--link', type: 'button', on: { click: toggleMode } });

  const form = h(
    'form',
    { class: 'login__form', novalidate: true, on: { submit: onSubmit } },
    notice,
    alert,
    username.el,
    displayName.el,
    password.el,
    submit,
  );

  const el = h(
    'main',
    { class: 'login' },
    h(
      'div',
      { class: 'login__card', role: 'region', 'aria-labelledby': 'login-title' },
      h('div', { class: 'login__brand' }, h('span', { class: 'login__mark' }, icon('chat')), 'Chat'),
      title,
      subtitle,
      form,
      h('p', { class: 'login__switch' }, switchText, ' ', switchButton),
    ),
  );

  function render() {
    const copy = modes[mode];
    const registering = mode === 'register';
    setText(title, copy.title);
    setText(subtitle, copy.subtitle);
    setText(submitLabel, busy ? copy.busy : copy.submit);
    setText(switchText, copy.switchText);
    setText(switchButton, copy.switchAction);
    displayName.el.hidden = !registering;
    displayName.el.querySelector('.field__hint').hidden = !registering;
    username.el.querySelector('.field__hint').hidden = !registering;
    password.el.querySelector('.field__hint').hidden = !registering;
    password.input.autocomplete = registering ? 'new-password' : 'current-password';
    submit.disabled = busy;
    switchButton.disabled = busy;
    spinner.hidden = !busy;
    form.setAttribute('aria-busy', String(busy));
    notice.hidden = !props.notice || registering;
    setText(notice, props.notice);
  }

  function setAlert(message) {
    alert.hidden = !message;
    setText(alert, message);
  }

  function clearErrors() {
    setAlert('');
    for (const field of [username, displayName, password]) field.setError('');
  }

  function toggleMode() {
    mode = mode === 'login' ? 'register' : 'login';
    clearErrors();
    password.resetVisibility();
    render();
    username.input.focus();
  }

  function validateUsername() {
    let error = '';
    if (mode === 'register') error = usernameError(username.value());
    else if (!username.value().trim()) error = 'Informe o nome de usuário.';
    username.setError(error);
    return !error;
  }

  function validateDisplayName() {
    const error = displayNameError(displayName.value());
    displayName.setError(error);
    return !error;
  }

  function validatePassword() {
    let error = '';
    if (mode === 'register') error = passwordError(password.value());
    else if (!password.value()) error = 'Informe a senha.';
    password.setError(error);
    return !error;
  }

  /** Validação local; devolve o primeiro campo inválido para receber o foco. */
  function validate() {
    const invalid = [];
    if (!validateUsername()) invalid.push(username);
    if (mode === 'register' && !validateDisplayName()) invalid.push(displayName);
    if (!validatePassword()) invalid.push(password);
    return invalid[0] ?? null;
  }

  async function onSubmit(event) {
    event.preventDefault();
    if (busy) return;
    clearErrors();
    const firstInvalid = validate();
    if (firstInvalid) {
      firstInvalid.input.focus();
      return;
    }
    busy = true;
    render();
    try {
      const user = normalizedUsername(username.value());
      if (mode === 'register') await register(user, displayName.value().trim(), password.value());
      else await login(user, password.value());
      // Sucesso: o store troca para a tela de chat e esta tela é desmontada.
    } catch (error) {
      busy = false;
      render();
      setAlert(errorMessage(error));
      password.input.select();
      password.input.focus();
    }
  }

  function update(next) {
    props = { ...props, ...next };
    render();
  }

  setAlert('');
  render();
  queueMicrotask(() => username.input.focus());
  return { el, update, destroy() {} };
}
