package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/FreddieTheObserver/cinebook/internal/booking"
)

const (
	headerCustomerRef    = "X-Customer-Ref"
	headerIdempotencyKey = "Idempotency-Key"
	headerRequestID      = "X-Request-Id"
)

// A hold request names at most a handful of seats, so anything near this size
// is not a hold request.
const maxBodyBytes = 64 << 10

const maxHeaderTokenLen = 255

// headerToken reads a header that must carry a single opaque token. Visible
// ASCII only, since the value reaches Postgres as text and the server passes
// through bytes that are not valid UTF-8.
func headerToken(r *http.Request, name string) (string, bool, error) {
	values := r.Header.Values(name)
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) > 1 || !visibleASCII(values[0], maxHeaderTokenLen) {
		return "", false, badRequest("%s must be a single value of 1 to %d visible ASCII characters", name, maxHeaderTokenLen)
	}
	return values[0], true, nil
}

func requireHeaderToken(r *http.Request, name string) (string, error) {
	value, ok, err := headerToken(r, name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", badRequest("the %s header is required", name)
	}
	return value, nil
}

func visibleASCII(s string, maxLen int) bool {
	if len(s) == 0 || len(s) > maxLen {
		return false
	}
	for i := range len(s) {
		if s[i] < '!' || s[i] > '~' {
			return false
		}
	}
	return true
}

// A malformed id cannot name a resource, so it is a 404 like any other miss.
func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, &problem{problemType: notFound}
	}
	return id, nil
}

func decodeJSON(r *http.Request, dst any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return &problem{problemType: unsupportedMediaType, detail: "Content-Type must be application/json"}
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	switch err := dec.Decode(&struct{}{}); {
	case errors.Is(err, io.EOF):
		return nil
	case err == nil:
		return badRequest("the request body must hold a single JSON value")
	default:
		return decodeError(err)
	}
}

func decodeError(err error) error {
	var (
		tooLarge  *http.MaxBytesError
		syntax    *json.SyntaxError
		wrongType *json.UnmarshalTypeError
	)
	switch {
	case errors.As(err, &tooLarge):
		return &problem{problemType: requestTooLarge, detail: fmt.Sprintf("the body must not exceed %d bytes", tooLarge.Limit)}
	case errors.Is(err, io.EOF):
		return badRequest("the request body is empty")
	case errors.As(err, &syntax), errors.Is(err, io.ErrUnexpectedEOF):
		return badRequest("the request body is not valid JSON")
	case errors.As(err, &wrongType) && wrongType.Field == "":
		return badRequest("the request body must be a JSON object")
	case errors.As(err, &wrongType):
		return badRequest("field %q has the wrong type", wrongType.Field)
	// encoding/json has no typed error for an unknown field.
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return badRequest("%s", strings.TrimPrefix(err.Error(), "json: "))
	}
	return badRequest("the request body could not be decoded")
}

func showtimeFilter(q url.Values) (booking.ShowtimeFilter, error) {
	var (
		f   booking.ShowtimeFilter
		err error
	)
	if f.MovieID, err = queryID(q, "movie_id"); err != nil {
		return f, err
	}
	if f.StartsFrom, err = queryTime(q, "from"); err != nil {
		return f, err
	}
	if f.StartsBefore, err = queryTime(q, "to"); err != nil {
		return f, err
	}
	if !f.StartsFrom.IsZero() && !f.StartsBefore.IsZero() && !f.StartsBefore.After(f.StartsFrom) {
		return f, badRequest("to must be later than from")
	}
	return f, nil
}

func queryParam(q url.Values, name string) (string, bool, error) {
	switch values := q[name]; len(values) {
	case 0:
		return "", false, nil
	case 1:
		return values[0], true, nil
	}
	return "", false, badRequest("query parameter %s is repeated", name)
}

func queryID(q url.Values, name string) (int64, error) {
	raw, ok, err := queryParam(q, name)
	if !ok || err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("%s must be a positive integer", name)
	}
	return id, nil
}

func queryTime(q url.Values, name string) (time.Time, error) {
	raw, ok, err := queryParam(q, name)
	if !ok || err != nil {
		return time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, badRequest("%s must be an RFC 3339 timestamp such as 2026-09-16T13:00:00+07:00, with + encoded as %%2B", name)
	}
	return t, nil
}
