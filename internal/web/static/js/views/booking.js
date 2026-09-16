import { api } from "../api.js";
import { formatMoney, formatStamp, h, seatLabel, showtimeLine } from "../dom.js";

export async function booking([ref], signal) {
  const booked = await api.booking(ref, { signal });
  const showtime = await api.showtime(booked.showtime_id, { signal });
  document.title = `Booking ${booked.ref} · CineBook`;

  return h("section", { class: "page booking-page" },
    h("header", { class: "page-head" },
      h("p", { class: "eyebrow" }, "Booking confirmed"),
      h("h1", { tabindex: "-1" }, showtime.movie_title),
      h("p", { class: "lede" }, showtimeLine(showtime)),
    ),
    h("div", { class: "card ticket" },
      h("p", { class: "meta" }, "Booking reference"),
      h("p", { class: "ref" }, booked.ref),
      h("dl", { class: "facts" },
        h("dt", null, booked.seats.length === 1 ? "Seat" : "Seats"),
        h("dd", null, h("ul", { class: "chips", role: "list" }, booked.seats.map((s) => h("li", { class: "chip" }, seatLabel(s))))),
        h("dt", null, "Total"),
        h("dd", { class: "total" }, formatMoney(booked.total_minor, booked.currency)),
        h("dt", null, "Booked"),
        h("dd", null, formatStamp(booked.created_at)),
      ),
    ),
    h("p", { class: "actions" }, h("a", { class: "btn secondary", href: "#/" }, "Book another film")),
  );
}
