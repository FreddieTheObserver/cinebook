// Every element is built through h, which only ever sets text and attributes,
// so nothing from the API can be interpreted as markup.
export function h(tag, props, ...children) {
  const el = document.createElement(tag);
  for (const [name, value] of Object.entries(props ?? {})) {
    if (value == null || value === false) {
      continue;
    }
    if (name === "class") {
      el.className = value;
    } else if (name.startsWith("on")) {
      el.addEventListener(name.slice(2), value);
    } else {
      el.setAttribute(name, value === true ? "" : String(value));
    }
  }
  el.append(...children.flat(Infinity).filter((c) => c != null && c !== false));
  return el;
}

// A live region has to exist before its content changes to be announced, so
// views keep one and swap what is inside it.
export function messageRegion() {
  return h("div", { class: "messages", "aria-live": "polite" });
}

export function say(region, kind, text) {
  region.replaceChildren(text ? h("p", { class: `notice ${kind}` }, text) : "");
}

const moneyFormats = new Map();

export function formatMoney(minor, currency) {
  let format = moneyFormats.get(currency);
  if (!format) {
    format = new Intl.NumberFormat(undefined, { style: "currency", currency });
    moneyFormats.set(currency, format);
  }
  return format.format(minor / 10 ** format.resolvedOptions().maximumFractionDigits);
}

const dayFormat = new Intl.DateTimeFormat(undefined, { weekday: "long", day: "numeric", month: "long" });
const timeFormat = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" });
const stampFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

export const formatDay = (iso) => dayFormat.format(new Date(iso));
export const formatTime = (iso) => timeFormat.format(new Date(iso));
export const formatStamp = (iso) => stampFormat.format(new Date(iso));

export function formatRuntime(minutes) {
  const hours = Math.floor(minutes / 60);
  const rest = String(minutes % 60).padStart(2, "0");
  return hours ? `${hours}h ${rest}m` : `${minutes}m`;
}

export function formatCountdown(ms) {
  const total = Math.max(0, Math.ceil(ms / 1000));
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, "0")}`;
}

export const seatLabel = (seat) => `${seat.row}${seat.number}`;

export function listLabels(labels) {
  if (labels.length <= 1) {
    return labels.join("");
  }
  return `${labels.slice(0, -1).join(", ")} and ${labels.at(-1)}`;
}

export function plural(n, one, many) {
  return `${n} ${n === 1 ? one : many}`;
}

export function showtimeLine(showtime) {
  return `${formatDay(showtime.starts_at)} at ${formatTime(showtime.starts_at)} · ${showtime.auditorium_name}`;
}
