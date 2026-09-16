import { api, serverNow } from "../api.js";
import { formatCountdown, formatMoney, h, messageRegion, plural, say, seatLabel, showtimeLine } from "../dom.js";
import { confirmKey, rememberBooking } from "../session.js";

const URGENT_MS = 60_000;

export async function hold([token], signal) {
  const held = await api.getHold(token, { signal });

  // A confirmed hold already has a booking, and replaying the confirm is how
  // the API hands its reference back.
  if (held.status === "confirmed") {
    const booked = await api.confirm(token, confirmKey(token), { signal });
    rememberBooking(booked.ref);
    location.replace(`#/bookings/${booked.ref}`);
    return null;
  }

  const showtime = await api.showtime(held.showtime_id, { signal });
  document.title = `Your hold · ${showtime.movie_title} · CineBook`;

  const messages = messageRegion();
  const countdown = h("p", { class: "countdown" });
  const confirmButton = h("button", { class: "btn primary", type: "button", onclick: confirm }, "Confirm booking");
  const releaseButton = h("button", { class: "btn secondary", type: "button", onclick: release }, "Release seats");
  const actions = h("div", { class: "actions" }, confirmButton, releaseButton);
  const timerBlock = h("div", { class: "timer" }, countdown, h("p", { class: "meta" }, "left to confirm"));
  const seatsAgain = `#/showtimes/${held.showtime_id}`;
  const heading = h("h1", { tabindex: "-1" }, "Your seats are held");

  let busy = false;
  let coolUntil = 0;
  let warned = false;
  let timer = 0;

  function ended(title, text) {
    clearInterval(timer);
    heading.textContent = title;
    timerBlock.remove();
    say(messages, "", "");
    actions.replaceChildren(
      h("p", { class: "notice warn" }, text),
      h("a", { class: "btn primary", href: seatsAgain }, "Choose seats again"),
    );
  }

  function setBusy(value, button, label) {
    busy = value;
    confirmButton.disabled = busy || Date.now() < coolUntil;
    releaseButton.disabled = busy;
    if (button) {
      button.textContent = label;
    }
  }

  function tick() {
    const left = Date.parse(held.expires_at) - serverNow();
    countdown.textContent = formatCountdown(left);
    countdown.classList.toggle("urgent", left <= URGENT_MS);
    if (left <= URGENT_MS && left > 0 && !warned) {
      warned = true;
      say(messages, "warn", "Less than a minute left to confirm.");
    }
    if (left <= 0) {
      ended("Your hold expired", "The seats are back on sale, so choose again if you still want them.");
    }
  }

  function backoff(err) {
    coolUntil = Date.now() + err.retryAfter * 1000;
    setTimeout(() => setBusy(busy), err.retryAfter * 1000);
    say(messages, "warn", `${err.message}. Try again in ${plural(err.retryAfter, "second", "seconds")}.`);
  }

  async function confirm() {
    setBusy(true, confirmButton, "Confirming…");
    try {
      const booked = await api.confirm(token, confirmKey(token), { signal });
      rememberBooking(booked.ref);
      clearInterval(timer);
      location.hash = `#/bookings/${booked.ref}`;
    } catch (err) {
      if (signal.aborted) {
        return;
      }
      setBusy(false, confirmButton, "Confirm booking");
      if (err.slug === "hold-expired") {
        ended("Your hold expired", "The seats are back on sale, so choose again if you still want them.");
      } else if (err.retryAfter > 0) {
        backoff(err);
      } else if (err.status === 0) {
        say(messages, "error", "Could not reach CineBook. Your seats stay held until the timer runs out, so try again.");
      } else {
        say(messages, "error", err.message);
      }
    }
  }

  async function release() {
    setBusy(true, releaseButton, "Releasing…");
    try {
      await api.release(token, { signal });
      clearInterval(timer);
      location.hash = seatsAgain;
    } catch (err) {
      if (signal.aborted) {
        return;
      }
      setBusy(false, releaseButton, "Release seats");
      if (err.slug === "hold-confirmed") {
        say(messages, "warn", "These seats are already booked, so they cannot be released.");
      } else if (err.retryAfter > 0) {
        backoff(err);
      } else {
        say(messages, "error", err.message);
      }
    }
  }

  const page = h("section", { class: "page hold-page" },
    h("a", { class: "back", href: seatsAgain }, "← Seat map"),
    h("header", { class: "page-head" },
      heading,
      h("p", { class: "lede" }, showtime.movie_title, " · ", showtimeLine(showtime)),
    ),
    h("div", { class: "card hold-card" },
      timerBlock,
      h("dl", { class: "facts" },
        h("dt", null, held.seats.length === 1 ? "Seat" : "Seats"),
        h("dd", null, h("ul", { class: "chips", role: "list" }, held.seats.map((s) => h("li", { class: "chip" }, seatLabel(s))))),
        h("dt", null, "Total"),
        h("dd", { class: "total" }, formatMoney(held.total_minor, held.currency)),
      ),
      messages,
      actions,
    ),
  );

  if (held.status === "released") {
    ended("You released this hold", "Its seats are back on sale.");
  } else if (held.status === "expired") {
    ended("Your hold expired", "The seats are back on sale, so choose again if you still want them.");
  } else {
    tick();
    timer = setInterval(tick, 250);
    signal.addEventListener("abort", () => clearInterval(timer));
  }
  return page;
}
