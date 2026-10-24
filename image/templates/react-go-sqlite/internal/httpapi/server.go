package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"prototype.local/react-go-sqlite/internal/store"
)

type Server struct {
	store *store.Store
	dist  string
}

func NewHandler(repository *store.Store, dist string) http.Handler {
	s := &Server{store: repository, dist: dist}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/items", s.listItems)
	mux.HandleFunc("POST /api/v1/items", s.createItem)
	mux.HandleFunc("DELETE /api/v1/items/{id}", s.deleteItem)
	mux.HandleFunc("GET /", s.spa)
	return s.headers(mux)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "database is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", "could not list items")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createItem(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "request body must be valid JSON")
		return
	}
	body.Title = strings.TrimSpace(body.Title)
	if len([]rune(body.Title)) == 0 || len([]rune(body.Title)) > 200 {
		writeError(w, http.StatusBadRequest, "INVALID_TITLE", "title must contain 1 to 200 characters")
		return
	}
	item, err := s.store.Create(r.Context(), body.Title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", "could not create item")
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) deleteItem(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "item ID is invalid")
		return
	}
	deleted, err := s.store.Delete(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", "could not delete item")
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "item not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) spa(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "API route not found")
		return
	}
	for _, part := range strings.Split(r.URL.Path, "/") {
		if part == ".." {
			writeError(w, http.StatusBadRequest, "BAD_PATH", "invalid path")
			return
		}
	}
	clean := path.Clean(r.URL.Path)
	candidate := filepath.Join(s.dist, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
	if clean != "/" {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			http.ServeFile(w, r, candidate)
			return
		}
	}
	index := filepath.Join(s.dist, "index.html")
	if _, err := os.Stat(index); err != nil {
		writeError(w, http.StatusServiceUnavailable, "WEB_NOT_BUILT", "web frontend has not been built")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, index)
}

func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
