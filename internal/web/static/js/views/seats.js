import { api } from "../api.js";
import { formatMoney, h, listLabels, messageRegion, plural, say, seatLabel, showtimeLine } from "../dom.js";

// The seat map is allowed to be slightly stale, and the hold is the authority.
const REFRESH_MS = 5000;

export async function seats([idParam], signal) {
  const showtimeId = Number(idParam);
  const [showtime, seatMap] = await Promise.all([
    api.showtime(showtimeId, { signal }),
    api.seatMap(showtimeId, { signal }),
  ]);
  document.title = `${showtime.movie_title} · CineBook`;

  const messages = messageRegion();
  const byId = new Map();
  const selected = new Set();
  let salesOpen = showtime.sales_open;
  let holding = false;
  let coolUntil = 0;

  const summaryText = h("p", { class: "summary-text", "aria-live": "polite" });
  const holdButton = h("button", { class: "btn primary", type: "button", onclick: placeHold }, "Hold seats");

  const rows = seatMap.rows.map((row) =>
    h("div", { class: "seat-row" },
      h("span", { class: "row-label", "aria-hidden": "true" }, row.label),
      row.seats.map((seat) => {
        const entry = { id: seat.id, row: row.label, number: seat.number, kind: seat.kind, status: seat.status };
        entry.button = h("button", { type: "button", onclick: () => toggle(entry) }, String(seat.number));
        byId.set(seat.id, entry);
        paint(entry);
        return entry.button;
      }),
      h("span", { class: "row-label", "aria-hidden": "true" }, row.label),
    ));

  function paint(entry) {
    const isSelected = selected.has(entry.id);
    const state = isSelected ? "selected" : entry.status;
    entry.button.className = `seat ${entry.kind} ${entry.status}`;
    entry.button.disabled = !salesOpen || entry.status !== "free";
    entry.button.setAttribute("aria-pressed", String(isSelected));
    const kind = entry.kind === "standard" ? "" : `, ${entry.kind}`;
    entry.button.setAttribute("aria-label", `Row ${entry.row} seat ${entry.number}${kind}, ${state}`);
  }

  function toggle(entry) {
    if (entry.status !== "free") {
      return;
    }
    if (selected.has(entry.id)) {
      selected.delete(entry.id);
    } else {
      selected.add(entry.id);
    }
    paint(entry);
    say(messages, "", "");
    updateSummary();
  }

  function selection() {
    return [...byId.values()].filter((entry) => selected.has(entry.id));
  }

  function updateSummary() {
    const chosen = selection();
    if (chosen.length === 0) {
      summaryText.textContent = salesOpen ? "Choose your seats." : "Sales are closed for this showtime.";
    } else {
      const total = formatMoney(showtime.price_minor * chosen.length, showtime.currency);
      summaryText.textContent = `${plural(chosen.length, "seat", "seats")}: ${listLabels(chosen.map(seatLabel))} · ${total}`;
    }
    holdButton.textContent = chosen.length > 1 ? `Hold ${chosen.length} seats` : "Hold seat";
    holdButton.disabled = holding || chosen.length === 0 || !salesOpen || Date.now() < coolUntil;
  }

  // Returns the selected seats that are no longer free, so a seat someone else
  // took leaves the selection at once rather than at the next refresh.
  function applyStatus(ids, status) {
    const lost = [];
    for (const id of ids) {
      const entry = byId.get(id);
      if (!entry) {
        continue;
      }
      entry.status = status;
      if (selected.delete(id)) {
        lost.push(entry);
      }
      paint(entry);
    }
    return lost;
  }

  async function placeHold() {
    const chosen = selection();
    holding = true;
    updateSummary();
    holdButton.textContent = "Holding…";
    try {
      const held = await api.hold(showtimeId, chosen.map((entry) => entry.id), { signal });
      location.hash = `#/holds/${held.token}`;
    } catch (err) {
      holding = false;
      if (signal.aborted) {
        return;
      }
      if (err.slug === "seat-unavailable") {
        const lost = applyStatus(err.problem.seat_ids ?? [], "held");
        const names = listLabels(lost.map(seatLabel));
        say(messages, "warn", lost.length === 1
          ? `Someone else just took ${names}. Pick another seat.`
          : `Someone else just took ${names}. Pick other seats.`);
      } else if (err.slug === "sales-closed") {
        salesOpen = false;
        byId.forEach(paint);
        say(messages, "warn", "Sales for this showtime have just closed.");
      } else if (err.retryAfter > 0) {
        coolUntil = Date.now() + err.retryAfter * 1000;
        setTimeout(updateSummary, err.retryAfter * 1000);
        say(messages, "warn", `${err.message}. Try again in ${plural(err.retryAfter, "second", "seconds")}.`);
      } else {
        say(messages, "error", err.message);
      }
      updateSummary();
    }
  }

  async function refresh() {
    if (document.hidden || holding) {
      return;
    }
    try {
      const latest = await api.seatMap(showtimeId, { signal });
      if (holding) {
        return;
      }
      const lost = [];
      for (const row of latest.rows) {
        for (const seat of row.seats) {
          const entry = byId.get(seat.id);
          if (entry && entry.status !== seat.status) {
            lost.push(...applyStatus([seat.id], seat.status));
          }
        }
      }
      if (lost.length > 0) {
        say(messages, "warn", `${listLabels(lost.map(seatLabel))} ${lost.length === 1 ? "was" : "were"} just taken by someone else.`);
      }
      updateSummary();
    } catch {
      // A missed refresh leaves the map a little staler, and the next one
      // tries again.
    }
  }

  const timer = setInterval(refresh, REFRESH_MS);
  const onVisible = () => {
    if (!document.hidden) {
      refresh();
    }
  };
  document.addEventListener("visibilitychange", onVisible);
  signal.addEventListener("abort", () => {
    clearInterval(timer);
    document.removeEventListener("visibilitychange", onVisible);
  });

  updateSummary();
  if (!salesOpen) {
    say(messages, "warn", "Sales are closed for this showtime.");
  }

  return h("section", { class: "page seats-page" },
    h("a", { class: "back", href: `#/movies/${showtime.movie_id}` }, "← All showtimes"),
    h("header", { class: "page-head" },
      h("h1", { tabindex: "-1" }, showtime.movie_title),
      h("p", { class: "lede" }, showtimeLine(showtime), " · ", formatMoney(showtime.price_minor, showtime.currency), " per seat"),
    ),
    messages,
    h("div", { class: "seatmap card" },
      h("div", { class: "screen", "aria-hidden": "true" }, h("span", null, "Screen")),
      h("div", { class: "seat-scroll" }, h("div", { class: "seat-rows", role: "group", "aria-label": "Seats" }, rows)),
      legend(),
    ),
    h("div", { class: "summary" }, summaryText, holdButton),
  );
}

function legend() {
  const item = (className, label) =>
    h("li", null, h("span", { class: `seat swatch ${className}`, "aria-hidden": "true" }), label);
  return h("ul", { class: "legend", role: "list" },
    item("standard free", "Free"),
    item("standard free selected-swatch", "Selected"),
    item("standard held", "Held"),
    item("standard sold", "Sold"),
    item("premium free", "Premium"),
    item("accessible free", "Accessible"),
  );
}
