// What this browser remembers between visits. Storage can be missing or throw
// (private windows, blocked site data), so every access falls back to memory.

const CUSTOMER_KEY = "cinebook.customer";
const BOOKINGS_KEY = "cinebook.bookings";
const MAX_REMEMBERED = 10;

const memory = new Map();

function storage(kind) {
  try {
    return window[kind] ?? null;
  } catch {
    return null;
  }
}

function read(kind, key) {
  try {
    return storage(kind)?.getItem(key) ?? memory.get(key) ?? null;
  } catch {
    return memory.get(key) ?? null;
  }
}

function write(kind, key, value) {
  memory.set(key, value);
  try {
    storage(kind)?.setItem(key, value);
  } catch {
    // Memory already has it, which lasts as long as the page.
  }
}

function randomHex(bytes) {
  return Array.from(crypto.getRandomValues(new Uint8Array(bytes)), (b) => b.toString(16).padStart(2, "0")).join("");
}

// The API accepts 1 to 255 visible ASCII characters.
const validHeaderValue = /^[\x21-\x7e]{1,255}$/;

export function customerRef() {
  let ref = read("localStorage", CUSTOMER_KEY);
  if (!ref || !validHeaderValue.test(ref)) {
    ref = `demo-${randomHex(6)}`;
    write("localStorage", CUSTOMER_KEY, ref);
  }
  return ref;
}

// crypto.randomUUID exists only in secure contexts, and a demo is often served
// over plain HTTP on a LAN address.
export function newIdempotencyKey() {
  if (typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const hex = Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

// One key per hold, reused on every attempt, so retrying after a lost response
// replays the first confirm instead of starting a new one.
export function confirmKey(token) {
  const name = `cinebook.confirm.${token}`;
  let key = read("sessionStorage", name);
  if (!key) {
    key = newIdempotencyKey();
    write("sessionStorage", name, key);
  }
  return key;
}

export function rememberedBookings() {
  try {
    const refs = JSON.parse(read("localStorage", BOOKINGS_KEY) ?? "[]");
    return Array.isArray(refs) ? refs.filter((r) => typeof r === "string" && /^CB-[0-9A-Z]{4}-[0-9A-Z]{4}$/.test(r)) : [];
  } catch {
    return [];
  }
}

export function rememberBooking(ref) {
  const refs = [ref, ...rememberedBookings().filter((r) => r !== ref)].slice(0, MAX_REMEMBERED);
  write("localStorage", BOOKINGS_KEY, JSON.stringify(refs));
}
