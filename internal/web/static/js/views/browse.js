import { api, serverNow } from "../api.js";
import { formatDay, formatMoney, formatRuntime, formatTime, h, messageRegion, say } from "../dom.js";
import { rememberedBookings } from "../session.js";

export async function browse([movieParam], signal) {
  const movieId = movieParam ? Number(movieParam) : null;
  document.title = "CineBook";

  const [{ movies }, showtimes] = await Promise.all([
    api.movies({ signal }),
    movieId ? api.showtimes(movieId, new Date(serverNow()), { signal }) : null,
  ]);
  const movie = movies.find((m) => m.id === movieId) ?? null;

  return h("section", { class: "page" },
    h("header", { class: "page-head" },
      h("h1", { tabindex: "-1" }, "Now showing"),
      h("p", { class: "lede" }, "Pick a film, then a showtime."),
    ),
    movies.length === 0
      ? h("p", { class: "notice" }, "No films are showing yet.")
      : h("ul", { class: "movies", role: "list" }, movies.map((m) => movieCard(m, m.id === movieId))),
    movieId ? showtimesSection(movie, showtimes?.showtimes ?? []) : null,
    lookupSection(),
  );
}

function movieCard(movie, current) {
  return h("li", null,
    h("a", { class: "movie card", href: `#/movies/${movie.id}`, "aria-current": current ? "true" : null },
      h("span", { class: "movie-title" }, movie.title),
      h("span", { class: "meta" },
        h("span", { class: "rating" }, movie.rating),
        formatRuntime(movie.runtime_min),
      ),
    ),
  );
}

function showtimesSection(movie, showtimes) {
  const heading = h("h2", { tabindex: "-1", "data-autofocus": true },
    movie ? `Showtimes for ${movie.title}` : "Showtimes");

  if (!movie) {
    return h("section", { class: "showtimes-section" }, heading, h("p", { class: "notice warn" }, "That film is not in the catalog."));
  }
  if (showtimes.length === 0) {
    return h("section", { class: "showtimes-section" }, heading, h("p", { class: "notice" }, "No upcoming showtimes for this film."));
  }

  const days = new Map();
  for (const s of showtimes) {
    const day = formatDay(s.starts_at);
    days.set(day, [...(days.get(day) ?? []), s]);
  }
  return h("section", { class: "showtimes-section" },
    heading,
    [...days].map(([day, list]) => [
      h("h3", { class: "day" }, day),
      h("ul", { class: "showtimes", role: "list" }, list.map(showtimePill)),
    ]),
  );
}

function showtimePill(showtime) {
  const body = [
    h("span", { class: "time" }, formatTime(showtime.starts_at)),
    h("span", { class: "meta" },
      showtime.auditorium_name, " · ",
      showtime.sales_open ? formatMoney(showtime.price_minor, showtime.currency) : "Sales closed"),
  ];
  return h("li", null, showtime.sales_open
    ? h("a", { class: "showtime", href: `#/showtimes/${showtime.id}` }, body)
    : h("span", { class: "showtime closed", "aria-disabled": "true" }, body));
}

function lookupSection() {
  const messages = messageRegion();
  const input = h("input", {
    id: "booking-ref", name: "ref", class: "input", required: true,
    placeholder: "CB-XXXX-XXXX", autocomplete: "off", autocapitalize: "characters", spellcheck: "false",
  });
  const form = h("form", { class: "lookup-form", novalidate: true },
    h("label", { for: "booking-ref" }, "Booking reference"),
    h("div", { class: "inline" }, input, h("button", { class: "btn secondary", type: "submit" }, "Open booking")),
    messages,
  );
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    const ref = input.value.trim().toUpperCase();
    if (!/^CB-[0-9A-Z]{4}-[0-9A-Z]{4}$/.test(ref)) {
      say(messages, "error", "A booking reference looks like CB-7K2M-9QX4.");
      input.focus();
      return;
    }
    location.hash = `#/bookings/${ref}`;
  });

  const remembered = rememberedBookings();
  return h("section", { class: "lookup card" },
    h("h2", null, "Find a booking"),
    form,
    remembered.length > 0
      ? [
          h("p", { class: "meta" }, "Booked in this browser"),
          h("ul", { class: "refs", role: "list" },
            remembered.map((ref) => h("li", null, h("a", { class: "ref-link", href: `#/bookings/${ref}` }, ref)))),
        ]
      : null,
  );
}
