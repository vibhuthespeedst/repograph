// Package api serves orders over HTTP.
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Store is what the handlers need from storage.
type Store interface {
	Get(id string) string
	Save(id string) error
}

type Handler struct{ store Store }

// New builds the router.
func New(s Store) http.Handler {
	h := &Handler{store: s}
	r := chi.NewRouter()
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	r.Route("/orders", func(r chi.Router) {
		r.Get("/", h.list)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", h.get)
			r.Post("/", h.save)
		})
	})
	return r
}

func (h *Handler) list(w http.ResponseWriter, _ *http.Request) { write(w, "[]") }

func (h *Handler) get(w http.ResponseWriter, _ *http.Request) { write(w, h.store.Get("x")) }

func (h *Handler) save(w http.ResponseWriter, _ *http.Request) {
	if err := h.store.Save("x"); err != nil {
		write(w, err.Error())
	}
}

func write(w http.ResponseWriter, s string) { _, _ = w.Write([]byte(s)) }
