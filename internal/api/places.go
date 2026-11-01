package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/t0mer/cic/internal/auth"
	"github.com/t0mer/cic/internal/places"
)

// PlaceStore is the safe-place store as seen by the API.
type PlaceStore interface {
	List() []places.Place
	Get(id int64) (places.Place, error)
	Create(ctx context.Context, p places.Place) (places.Place, error)
	Update(ctx context.Context, id int64, patch places.Patch) (places.Place, error)
	Delete(ctx context.Context, id int64) error
}

const codeDuplicate = "duplicate_name"

func placeID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		writeCmdError(w, http.StatusBadRequest, codeInvalid, "invalid place id")
		return 0, false
	}
	return id, true
}

// placeError maps a store error to a response and reports whether it handled one.
func (s *server) placeError(w http.ResponseWriter, done func(string), err error, what string) {
	switch {
	case errors.Is(err, places.ErrInvalid):
		done("rejected:" + codeInvalid)
		writeCmdError(w, http.StatusBadRequest, codeInvalid, strings.TrimPrefix(err.Error(), places.ErrInvalid.Error()+": "))
	case errors.Is(err, places.ErrDuplicate):
		done("rejected:" + codeDuplicate)
		writeCmdError(w, http.StatusConflict, codeDuplicate, err.Error())
	case errors.Is(err, places.ErrNotFound):
		done("rejected:" + codeNotFound)
		writeCmdError(w, http.StatusNotFound, codeNotFound, "place not found")
	default:
		done("failed")
		s.internalError(w, what, err)
	}
}

func (s *server) placesOrUnavailable(w http.ResponseWriter) (PlaceStore, bool) {
	if s.Places == nil {
		writeCmdError(w, http.StatusServiceUnavailable, codeUnavailable, "safe places are not available")
		return nil, false
	}
	return s.Places, true
}

func (s *server) listPlaces(w http.ResponseWriter, _ *http.Request) {
	out := []places.Place{}
	if s.Places != nil {
		out = append(out, s.Places.List()...)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) createPlace(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.placesOrUnavailable(w)
	if !ok {
		return
	}
	var body struct {
		Name      *string  `json:"name"`
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
		RadiusM   *int     `json:"radius_m"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	if body.Name == nil || body.Latitude == nil || body.Longitude == nil || body.RadiusM == nil {
		writeCmdError(w, http.StatusBadRequest, codeInvalid, "name, latitude, longitude and radius_m are required")
		return
	}
	name, valid := places.CleanName(*body.Name)
	u, _ := auth.UserFrom(r.Context())
	if !valid {
		name = "(invalid)"
	}
	done, ok := s.audited(u.Username, "-", "place_create", "place="+name)
	if !ok {
		writeCmdError(w, http.StatusServiceUnavailable, codeUnavailable, "service temporarily unavailable; nothing was changed")
		return
	}
	p, err := ps.Create(r.Context(), places.Place{Name: *body.Name, Latitude: *body.Latitude, Longitude: *body.Longitude, RadiusM: *body.RadiusM})
	if err != nil {
		s.placeError(w, done, err, "creating place")
		return
	}
	done(statusOK)
	writeJSON(w, http.StatusCreated, p)
}

func (s *server) patchPlace(w http.ResponseWriter, r *http.Request) {
	id, ok := placeID(w, r)
	if !ok {
		return
	}
	ps, ok := s.placesOrUnavailable(w)
	if !ok {
		return
	}
	var body struct {
		Name      *string  `json:"name"`
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
		RadiusM   *int     `json:"radius_m"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	if body.Name == nil && body.Latitude == nil && body.Longitude == nil && body.RadiusM == nil {
		writeCmdError(w, http.StatusBadRequest, codeInvalid, "nothing to change")
		return
	}
	cur, err := ps.Get(id)
	if errors.Is(err, places.ErrNotFound) {
		writeCmdError(w, http.StatusNotFound, codeNotFound, "place not found")
		return
	}
	if err != nil {
		s.internalError(w, "reading place", err)
		return
	}
	var changed []string
	if body.Name != nil {
		changed = append(changed, "name")
	}
	if body.Latitude != nil || body.Longitude != nil {
		changed = append(changed, "position")
	}
	if body.RadiusM != nil {
		changed = append(changed, "radius_m")
	}
	u, _ := auth.UserFrom(r.Context())
	done, ok := s.audited(u.Username, "-", "place_update", "place="+cur.Name+" fields="+strings.Join(changed, ","))
	if !ok {
		writeCmdError(w, http.StatusServiceUnavailable, codeUnavailable, "service temporarily unavailable; nothing was changed")
		return
	}
	p, err := ps.Update(r.Context(), id, places.Patch{Name: body.Name, Latitude: body.Latitude, Longitude: body.Longitude, RadiusM: body.RadiusM})
	if err != nil {
		s.placeError(w, done, err, "updating place")
		return
	}
	done(statusOK)
	writeJSON(w, http.StatusOK, p)
}

func (s *server) deletePlace(w http.ResponseWriter, r *http.Request) {
	id, ok := placeID(w, r)
	if !ok {
		return
	}
	ps, ok := s.placesOrUnavailable(w)
	if !ok {
		return
	}
	cur, err := ps.Get(id)
	if errors.Is(err, places.ErrNotFound) {
		writeCmdError(w, http.StatusNotFound, codeNotFound, "place not found")
		return
	}
	if err != nil {
		s.internalError(w, "reading place", err)
		return
	}
	u, _ := auth.UserFrom(r.Context())
	done, ok := s.audited(u.Username, "-", "place_delete", "place="+cur.Name)
	if !ok {
		writeCmdError(w, http.StatusServiceUnavailable, codeUnavailable, "service temporarily unavailable; nothing was changed")
		return
	}
	if err := ps.Delete(r.Context(), id); err != nil {
		s.placeError(w, done, err, "deleting place")
		return
	}
	done(statusOK)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
