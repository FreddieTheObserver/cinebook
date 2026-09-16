import { customerRef } from "./session.js";

export class ApiError extends Error {
  constructor(status, problem, retryAfter = 0) {
    const title = problem?.title || "Something went wrong";
    super(problem?.detail ? `${title}: ${problem.detail}` : title);
    this.name = "ApiError";
    this.status = status;
    this.problem = problem ?? {};
    // Clients branch on the slug, never on the wording.
    this.slug = typeof this.problem.type === "string" ? this.problem.type.split("/").pop() : "";
    this.retryAfter = retryAfter;
  }
}

// Hold expiry is decided by the database clock, so the countdown follows the
// server's Date header rather than trusting this machine's clock. The header
// only has whole seconds, so small differences are treated as none.
let clockOffsetMs = 0;
const CLOCK_TOLERANCE_MS = 2000;

function noteServerClock(response) {
  const date = Date.parse(response.headers.get("Date") ?? "");
  if (Number.isNaN(date)) {
    return;
  }
  const offset = date + 500 - Date.now();
  clockOffsetMs = Math.abs(offset) > CLOCK_TOLERANCE_MS ? offset : 0;
}

export function serverNow() {
  return Date.now() + clockOffsetMs;
}

async function request(method, path, { body, headers = {}, signal } = {}) {
  const init = { method, signal, headers: { ...headers } };
  if (body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }

  let response;
  try {
    response = await fetch(path, init);
  } catch (err) {
    if (err.name === "AbortError") {
      throw err;
    }
    throw new ApiError(0, { title: "Could not reach CineBook", detail: "check your connection and try again" });
  }
  noteServerClock(response);

  if (response.status === 204) {
    return null;
  }
  const data = await response.json().catch(() => null);
  if (!response.ok) {
    const retryAfter = Number.parseInt(response.headers.get("Retry-After") ?? "", 10);
    throw new ApiError(response.status, data, Number.isNaN(retryAfter) ? 0 : retryAfter);
  }
  return data;
}

const segment = encodeURIComponent;

export const api = {
  movies: (opts) => request("GET", "/v1/movies", opts),
  showtimes: (movieId, from, opts) =>
    request("GET", `/v1/showtimes?movie_id=${segment(movieId)}&from=${segment(from.toISOString())}`, opts),
  showtime: (id, opts) => request("GET", `/v1/showtimes/${segment(id)}`, opts),
  seatMap: (id, opts) => request("GET", `/v1/showtimes/${segment(id)}/seats`, opts),
  // Only a hold names the customer, as the API requires. Reads stay anonymous,
  // so browsing never spends the per-customer throttle, and everything after
  // the hold is authorized by its token.
  hold: (showtimeId, seatIds, opts) =>
    request("POST", `/v1/showtimes/${segment(showtimeId)}/holds`, {
      ...opts,
      headers: { "X-Customer-Ref": customerRef() },
      body: { seat_ids: seatIds },
    }),
  getHold: (token, opts) => request("GET", `/v1/holds/${segment(token)}`, opts),
  release: (token, opts) => request("DELETE", `/v1/holds/${segment(token)}`, opts),
  confirm: (token, idempotencyKey, opts) =>
    request("POST", `/v1/holds/${segment(token)}/confirm`, { ...opts, headers: { "Idempotency-Key": idempotencyKey } }),
  booking: (ref, opts) => request("GET", `/v1/bookings/${segment(ref)}`, opts),
};
