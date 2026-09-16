import { ApiError } from "./api.js";
import { h, plural } from "./dom.js";
import { customerRef } from "./session.js";
import { browse } from "./views/browse.js";
import { seats } from "./views/seats.js";
import { hold } from "./views/hold.js";
import { booking } from "./views/booking.js";

// Hash routes keep hold tokens and booking references in the fragment, which
// the browser never sends to a server or puts in a Referer.
const routes = [
  [/^#\/?$/, browse],
  [/^#\/movies\/(\d+)$/, browse],
  [/^#\/showtimes\/(\d+)$/, seats],
  [/^#\/holds\/([A-Z2-7]{26})$/, hold],
  [/^#\/bookings\/(CB-[0-9A-Z]{4}-[0-9A-Z]{4})$/, booking],
];

// Short loads never flash a loading message.
const LOADING_DELAY_MS = 150;

const root = document.getElementById("view");
let active = null;
let lastView = null;

async function route() {
  active?.abort();
  const controller = new AbortController();
  active = controller;
  const { signal } = controller;

  const hash = location.hash || "#/";
  const found = routes.map(([pattern, view]) => [hash.match(pattern), view]).find(([match]) => match);
  const view = found?.[1] ?? null;
  if (view !== lastView) {
    window.scrollTo(0, 0);
  }
  lastView = view;

  if (!found) {
    show(notFound());
    return;
  }

  const loading = setTimeout(() => {
    if (!signal.aborted) {
      root.replaceChildren(h("p", { class: "loading" }, "Loading…"));
    }
  }, LOADING_DELAY_MS);
  try {
    // A view resolves to its page, or to nothing when it has navigated away.
    const page = await view(found[0].slice(1), signal);
    if (!signal.aborted && page) {
      show(page);
    }
  } catch (err) {
    if (!signal.aborted) {
      if (!(err instanceof ApiError)) {
        console.error(err);
      }
      show(failure(err));
    }
  } finally {
    clearTimeout(loading);
  }
}

function show(page) {
  root.replaceChildren(page);
  const target = page.querySelector("[data-autofocus]") ?? page.querySelector("h1");
  target?.focus({ preventScroll: true });
}

function notFound() {
  return h("section", { class: "page" },
    h("h1", { tabindex: "-1" }, "Not found"),
    h("p", { class: "lede" }, "Nothing lives at this address. The link may be mistyped or out of date."),
    h("p", { class: "actions" }, h("a", { class: "btn secondary", href: "#/" }, "Back to films")),
  );
}

function failure(err) {
  if (err instanceof ApiError && err.status === 404) {
    return notFound();
  }
  const wait = err instanceof ApiError && err.retryAfter > 0
    ? ` Try again in ${plural(err.retryAfter, "second", "seconds")}.`
    : "";
  return h("section", { class: "page" },
    h("h1", { tabindex: "-1" }, err instanceof ApiError && err.status === 0 ? "Could not reach CineBook" : "Something went wrong"),
    h("p", { class: "lede" }, `${err.message}.${wait}`),
    h("p", { class: "actions" },
      h("button", { class: "btn primary", type: "button", onclick: route }, "Try again"),
      h("a", { class: "btn secondary", href: "#/" }, "Back to films"),
    ),
  );
}

document.getElementById("customer-ref").textContent = customerRef();
window.addEventListener("hashchange", route);
route();
