// Formatação de horários no padrão brasileiro. Datas chegam do Go como strings ISO 8601.

const timeFormat = new Intl.DateTimeFormat('pt-BR', { hour: '2-digit', minute: '2-digit' });
const shortDateFormat = new Intl.DateTimeFormat('pt-BR', { day: '2-digit', month: '2-digit' });
const longDateFormat = new Intl.DateTimeFormat('pt-BR', { day: 'numeric', month: 'long', year: 'numeric' });
const weekdayFormat = new Intl.DateTimeFormat('pt-BR', { weekday: 'long' });

const DAY = 24 * 60 * 60 * 1000;

export function toDate(value) {
  const date = value instanceof Date ? value : new Date(value);
  return Number.isNaN(date.getTime()) || date.getFullYear() < 2000 ? null : date;
}

function startOfDay(date) {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
}

/** Dias inteiros entre a data e hoje (0 = hoje, 1 = ontem). */
function daysAgo(date, now = new Date()) {
  return Math.round((startOfDay(now) - startOfDay(date)) / DAY);
}

/** "14:05" — horário dentro da bolha. */
export function formatTime(value) {
  const date = toDate(value);
  return date ? timeFormat.format(date) : '';
}

/** Horário da lista de conversas: "14:05" hoje, "Ontem", ou "03/11". */
export function formatListTime(value) {
  const date = toDate(value);
  if (!date) return '';
  const days = daysAgo(date);
  if (days === 0) return timeFormat.format(date);
  if (days === 1) return 'Ontem';
  return shortDateFormat.format(date);
}

/** Separador de dia no histórico: "Hoje", "Ontem", "segunda-feira" (última semana) ou "3 de novembro de 2026". */
export function formatDay(value) {
  const date = toDate(value);
  if (!date) return '';
  const days = daysAgo(date);
  if (days === 0) return 'Hoje';
  if (days === 1) return 'Ontem';
  if (days < 7) {
    const name = weekdayFormat.format(date);
    return name.charAt(0).toUpperCase() + name.slice(1);
  }
  return longDateFormat.format(date);
}

/** Chave de dia (AAAA-MM-DD local), usada para agrupar mensagens. */
export function dayKey(value) {
  const date = toDate(value);
  if (!date) return 'sem-data';
  return `${date.getFullYear()}-${date.getMonth() + 1}-${date.getDate()}`;
}

/** Milissegundos entre duas datas (b - a). */
export function diffMs(a, b) {
  const da = toDate(a);
  const db = toDate(b);
  return da && db ? db.getTime() - da.getTime() : Infinity;
}
