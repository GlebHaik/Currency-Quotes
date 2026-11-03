package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func NewRouter(svc QuoteService, log *slog.Logger, timeout time.Duration) http.Handler {
	h := newQuoteHandler(svc, log)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(accessLog(log)) // outside recoverer, so a panic is logged with its 500
	r.Use(recoverer(log))
	r.Use(requestTimeout(timeout))
	r.NotFound(notFound)
	r.MethodNotAllowed(allowMethods(r, methodNotAllowed))

	r.Post("/api/v1/quotes/updates", h.RequestQuoteUpdate)
	r.Get("/api/v1/quotes/updates/{updateId}", h.GetQuoteUpdate)
	r.Get("/api/v1/quotes/latest", h.GetLatestQuote)
	return r
}

func allowMethods(routes chi.Routes, next http.HandlerFunc) http.HandlerFunc {
	methods := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions}

	return func(w http.ResponseWriter, r *http.Request) {
		for _, m := range methods {
			if routes.Match(chi.NewRouteContext(), m, r.URL.Path) {
				w.Header().Add("Allow", m)
			}
		}
		next(w, r)
	}
}
