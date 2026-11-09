// Ícones SVG em traço de 1.75px, 24×24, herdando currentColor. São constantes do código (nunca dados do usuário),
// por isso podem ser inseridos como marcação.

const paths = {
  chat: '<path d="M4 5.5A2.5 2.5 0 0 1 6.5 3h11A2.5 2.5 0 0 1 20 5.5v8a2.5 2.5 0 0 1-2.5 2.5H10l-4.5 4v-4h0A1.5 1.5 0 0 1 4 14.5z"/>',
  logout: '<path d="M10 4H6.5A2.5 2.5 0 0 0 4 6.5v11A2.5 2.5 0 0 0 6.5 20H10"/><path d="M15 8l4 4-4 4"/><path d="M19 12H9"/>',
  clock: '<circle cx="12" cy="12" r="8"/><path d="M12 8v4l2.5 1.5"/>',
  check: '<path d="M5 12.5l4.5 4.5L19 7.5"/>',
  checks: '<path d="M2.5 12.5L7 17l9.5-9.5"/><path d="M12 16l1 1 9.5-9.5"/>',
  alert: '<circle cx="12" cy="12" r="8.5"/><path d="M12 7.5v5.5"/><path d="M12 16.2v.3"/>',
  offline: '<path d="M3 3l18 18"/><path d="M8.5 16.5a5 5 0 0 1 7 0"/><path d="M5 12.6a10 10 0 0 1 4.3-2.4"/><path d="M19 12.6a10 10 0 0 0-2.2-1.5"/><path d="M2 9a15 15 0 0 1 4.2-2.7"/><path d="M22 9a15 15 0 0 0-9.4-3.8"/>',
  eye: '<path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="3"/>',
  eyeOff: '<path d="M3 3l18 18"/><path d="M10.6 6.6A9 9 0 0 1 12 5.5c6 0 9.5 6.5 9.5 6.5a16 16 0 0 1-2.6 3.4"/><path d="M6.6 6.6C4 8.3 2.5 12 2.5 12S6 18.5 12 18.5a9 9 0 0 0 4.4-1.1"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/>',
  send: '<path d="M4.5 12 3 4.5l18 7.5-18 7.5z"/><path d="M4.5 12H11"/>',
  inbox: '<path d="M4 13.5l2.4-7.2A2 2 0 0 1 8.3 5h7.4a2 2 0 0 1 1.9 1.3l2.4 7.2"/><path d="M4 13.5V18a1.5 1.5 0 0 0 1.5 1.5h13A1.5 1.5 0 0 0 20 18v-4.5h-4.5l-1 2h-5l-1-2z"/>',
};

/** Devolve um <svg> do ícone. label vazio marca o ícone como decorativo (aria-hidden). */
export function icon(name, label = '') {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('fill', 'none');
  svg.setAttribute('stroke', 'currentColor');
  svg.setAttribute('stroke-width', '1.75');
  svg.setAttribute('stroke-linecap', 'round');
  svg.setAttribute('stroke-linejoin', 'round');
  svg.setAttribute('class', `icon icon--${name}`);
  if (label) {
    svg.setAttribute('role', 'img');
    svg.setAttribute('aria-label', label);
  } else {
    svg.setAttribute('aria-hidden', 'true');
  }
  svg.innerHTML = paths[name] ?? '';
  return svg;
}
