package httpapi

import (
	"net/http"
)

func (a *api) listMovies(w http.ResponseWriter, r *http.Request) error {
	movies, err := a.svc.ListMovies(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, struct {
		Movies []movieJSON `json:"movies"`
	}{moviesFrom(movies)})
}

func (a *api) listShowtimes(w http.ResponseWriter, r *http.Request) error {
	filter, err := showtimeFilter(r.URL.Query())
	if err != nil {
		return err
	}
	showtimes, err := a.svc.ListShowtimes(r.Context(), filter)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, struct {
		Showtimes []showtimeJSON `json:"showtimes"`
	}{showtimesFrom(showtimes)})
}

func (a *api) getShowtime(w http.ResponseWriter, r *http.Request) error {
	showtimeID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	showtime, err := a.svc.GetShowtime(r.Context(), showtimeID)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, showtimeFrom(*showtime))
}

func (a *api) seatMap(w http.ResponseWriter, r *http.Request) error {
	showtimeID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	seatMap, err := a.svc.SeatMap(r.Context(), showtimeID)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, seatMapFrom(seatMap))
}

func (a *api) createHold(w http.ResponseWriter, r *http.Request) error {
	showtimeID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	customer, err := requireHeaderToken(r, headerCustomerRef)
	if err != nil {
		return err
	}
	var req holdRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	held, err := a.svc.Hold(r.Context(), showtimeID, customer, req.SeatIDs)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/holds/"+held.Token)
	return writeJSON(w, http.StatusCreated, holdFrom(held))
}

func (a *api) getHold(w http.ResponseWriter, r *http.Request) error {
	held, err := a.svc.GetHold(r.Context(), r.PathValue("token"))
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, holdFrom(held))
}

func (a *api) releaseHold(w http.ResponseWriter, r *http.Request) error {
	if err := a.svc.Release(r.Context(), r.PathValue("token")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// A replay answers 201 with the original booking, so a client that lost the
// first response sees exactly what it would have seen.
func (a *api) confirmHold(w http.ResponseWriter, r *http.Request) error {
	key, err := requireHeaderToken(r, headerIdempotencyKey)
	if err != nil {
		return err
	}

	booked, err := a.svc.Confirm(r.Context(), r.PathValue("token"), key)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/bookings/"+booked.Ref)
	return writeJSON(w, http.StatusCreated, bookingFrom(booked))
}

func (a *api) getBooking(w http.ResponseWriter, r *http.Request) error {
	booked, err := a.svc.GetBooking(r.Context(), r.PathValue("ref"))
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, bookingFrom(booked))
}
