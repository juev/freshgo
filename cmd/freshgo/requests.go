package main

import (
	"log/slog"
	"net/http"
	"time"
)

// statusWriter remembers the status of the answer it passes on.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the writer of the server, to
// flush a long answer as it goes.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// accessLog logs every request at the level debug: its method, path,
// status and how long it took. The query is left out, as it may carry a
// token, and so is anything of what was read.
func accessLog(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !log.Enabled(r.Context(), slog.LevelDebug) {
			next.ServeHTTP(w, r)
			return
		}
		began := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		log.Debug("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "duration", time.Since(began))
	})
}
