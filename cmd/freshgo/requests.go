package main

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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

// requestCounter counts the answers of the server by status.
func requestCounter() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "freshgo_http_requests_total",
		Help: "Requests the server answered, by status.",
	}, []string{"code"})
}

// observe counts every request by the status of its answer and logs it at
// the level debug: its method, path, status and how long it took. The query
// is left out, as it may carry a token, and so is anything of what was read.
func observe(next http.Handler, log *slog.Logger, requests *prometheus.CounterVec) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		began := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		requests.WithLabelValues(strconv.Itoa(sw.status)).Inc()
		log.Debug("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "duration", time.Since(began))
	})
}
