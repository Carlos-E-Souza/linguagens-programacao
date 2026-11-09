// Helpers de DOM. Regra do projeto: texto vindo do usuário ou do servidor entra sempre como nó de texto
// (h() com strings ou setText), nunca por innerHTML.

/**
 * Cria um elemento. attrs aceita: class, dataset (objeto), on (objeto evento → função), qualquer atributo
 * (aria-*, type, ...) e propriedades booleanas (disabled, hidden). Valores null/false/undefined são ignorados.
 * Filhos podem ser elementos, strings (viram texto) ou arrays.
 */
export function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (value === null || value === undefined || value === false) continue;
    if (key === 'class') el.className = value;
    else if (key === 'dataset') Object.assign(el.dataset, value);
    else if (key === 'on') for (const [event, fn] of Object.entries(value)) el.addEventListener(event, fn);
    else if (key in el && typeof value === 'boolean') el[key] = value;
    else el.setAttribute(key, value === true ? '' : String(value));
  }
  append(el, children);
  return el;
}

function append(el, children) {
  for (const child of children) {
    if (child === null || child === undefined || child === false) continue;
    if (Array.isArray(child)) append(el, child);
    else el.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
}

/** Troca o texto do elemento só se ele mudou (evita reflow e preserva seleção). */
export function setText(el, text) {
  const value = text ?? '';
  if (el.textContent !== value) el.textContent = value;
}

/** Liga ou desliga uma classe. */
export function toggleClass(el, name, on) {
  el.classList.toggle(name, Boolean(on));
}

/** Substitui todos os filhos do elemento. */
export function replaceChildren(el, ...children) {
  el.replaceChildren();
  append(el, children);
}

/**
 * Reordena os filhos de parent para ficarem exatamente como nodes, movendo apenas o que estiver fora do lugar.
 * Nós que não estão em nodes são removidos.
 */
export function reconcileChildren(parent, nodes) {
  const keep = new Set(nodes);
  for (const child of [...parent.children]) {
    if (!keep.has(child)) child.remove();
  }
  nodes.forEach((node, index) => {
    if (parent.children[index] !== node) parent.insertBefore(node, parent.children[index] ?? null);
  });
}
