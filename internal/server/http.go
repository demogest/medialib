package server

import (
	"compress/gzip"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/s3"
)

// apiError is an error with an HTTP status.
type apiError struct {
	status int
	msg    string
	extra  map[string]any
}

func (e *apiError) Error() string { return e.msg }

func fail(status int, msg string) error { return &apiError{status: status, msg: msg} }

// errorStatus maps S3, library and validation errors onto HTTP answers.
func errorStatus(err error) (int, map[string]any) {
	var s3e *s3.Error
	var ae *apiError
	var ue *config.UserError
	switch {
	case errors.As(err, &s3e):
		status := 502
		if s3e.Status >= 400 && s3e.Status < 500 {
			status = s3e.Status
		}
		return status, map[string]any{"error": s3e.Error(), "code": s3e.Code, "status": s3e.Status, "request_id": s3e.RequestID}
	case errors.As(err, &ae):
		body := map[string]any{"error": ae.msg}
		for k, v := range ae.extra {
			body[k] = v
		}
		return ae.status, body
	case errors.As(err, &ue):
		return 400, map[string]any{"error": ue.Msg}
	}
	msg := err.Error()
	if msg == "" {
		msg = "error"
	}
	return 500, map[string]any{"error": msg}
}

// Ctx is what a route function gets: the app, the request, and helpers for query and body.
type Ctx struct {
	App  *App
	W    http.ResponseWriter
	R    *http.Request
	body Body
	got  bool
}

// Arg is a query parameter ("" when missing).
func (c *Ctx) Arg(name string) string { return c.R.URL.Query().Get(name) }

// ArgOr is a query parameter with a default.
func (c *Ctx) ArgOr(name, def string) string {
	if q := c.R.URL.Query(); q.Has(name) {
		return q.Get(name)
	}
	return def
}

// Int is an integer query parameter.
func (c *Ctx) Int(name string, def int) (int, error) {
	v := c.Arg(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, config.Errorf("invalid literal for %s: %q", name, v)
	}
	return n, nil
}

// P is a matched path part.
func (c *Ctx) P(name string) string { return c.R.PathValue(name) }

// Body is the decoded JSON request body.
func (c *Ctx) Body() (Body, error) {
	if c.got {
		return c.body, nil
	}
	c.got = true
	c.body = Body{}
	if c.R.ContentLength == 0 {
		return c.body, nil
	}
	dec := json.NewDecoder(io.LimitReader(c.R.Body, 32<<20))
	if err := dec.Decode(&c.body); err != nil && !errors.Is(err, io.EOF) {
		return nil, config.Errorf("The request body is not valid JSON.")
	}
	return c.body, nil
}

// Library finds a library by id (empty means the active one).
func (c *Ctx) Library(id string) (config.Library, error) {
	lib, ok := c.App.Cfg.Library(id)
	if !ok {
		return lib, fail(404, "unknown library")
	}
	return lib, nil
}

func (c *Ctx) Client() (*s3.Client, error) {
	cl, err := c.App.Clients.Get(c.P("conn"))
	if err != nil {
		return nil, fail(404, err.Error())
	}
	return cl, nil
}

// Body is a loose JSON object.
type Body map[string]any

func (b Body) Str(k string) string {
	s, _ := b[k].(string)
	return s
}

// StrP is nil when the key is missing or null.
func (b Body) StrP(k string) *string {
	if s, ok := b[k].(string); ok {
		return &s
	}
	return nil
}

func (b Body) Bool(k string) bool {
	v, _ := b[k].(bool)
	return v
}

// BoolP is nil when the key is missing.
func (b Body) BoolP(k string) *bool {
	if v, ok := b[k].(bool); ok {
		return &v
	}
	return nil
}

// IntP is nil when the key is missing or not a number.
func (b Body) IntP(k string) *int {
	if v, ok := b[k].(float64); ok {
		n := int(v)
		return &n
	}
	return nil
}

func (b Body) Strs(k string) []string {
	var out []string
	if l, ok := b[k].([]any); ok {
		for _, v := range l {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func (b Body) Maps(k string) []map[string]any {
	var out []map[string]any
	if l, ok := b[k].([]any); ok {
		for _, v := range l {
			if m, ok := v.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- responses

func clientAcceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// writeJSON answers with a JSON body. Big answers to other computers are compressed; on loopback that only costs CPU.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		status, body = 500, []byte(`{"error":"could not encode the answer"}`)
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	if len(body) > 4096 && clientAcceptsGzip(r) && !isLoopbackConn(r) {
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		w.WriteHeader(status)
		zw, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		_, _ = zw.Write(body)
		_ = zw.Close()
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func writeText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text)
}

// ---------------------------------------------------------------- access rules

var loopbackHosts = map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true}

// isLoopbackConn is true when the request really came from this computer: from a loopback address and not
// through a reverse proxy (which would make every visitor look local).
func isLoopbackConn(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	for _, h := range []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip", "X-Forwarded-Host"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	return true
}

func hostOnly(h string) string {
	if strings.HasPrefix(h, "[") {
		if i := strings.Index(h, "]"); i > 0 {
			return h[1:i]
		}
	}
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[:i], ":") {
		return h[:i]
	}
	return h
}

// access classes of a route
const (
	open    = iota // library views, thumbnails, streams: anyone who may reach the server
	private        // storage browser, connections, every change: this computer, or anyone who signed in
	machine        // acts on the computer running medialib (players, folder dialog): this computer only
)

type routeFn func(*Ctx) (any, error)

// guard applies the access rules to a route.
func (a *App) guard(class int, fn routeFn) http.HandlerFunc {
	password := a.Cfg.Settings().Password
	return func(w http.ResponseWriter, r *http.Request) {
		if a.Log != nil {
			a.Log.Printf("%s %s %s", r.RemoteAddr, r.Method, r.URL.RequestURI())
		}
		// Refuse cross-site requests from web pages, and foreign Host names (DNS rebinding) on loopback.
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeText(w, 403, "forbidden")
			return
		}
		if a.Loopback && !loopbackHosts[hostOnly(r.Host)] {
			writeText(w, 403, "forbidden")
			return
		}
		signedIn := false
		if password != "" {
			_, pw, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(pw), []byte(password)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="medialib", charset="UTF-8"`)
				writeText(w, 401, "sign in with the password (any user name)")
				return
			}
			signedIn = true
		}
		mutating := r.Method != http.MethodGet && r.Method != http.MethodHead
		if mutating && class == open {
			class = private
		}
		switch {
		case class == machine && !isLoopbackConn(r):
			writeJSON(w, r, 403, map[string]any{"error": "only available on the computer running the library"})
			return
		case class == private && !signedIn && !isLoopbackConn(r):
			writeJSON(w, r, 403, map[string]any{"error": "only available on the computer running the library"})
			return
		}
		if mutating && r.Header.Get("X-Medialib") != "1" {
			writeText(w, 403, "forbidden") // a custom header: web pages cannot send it cross-site
			return
		}
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				writeJSON(w, r, 500, map[string]any{"error": fmt.Sprintf("internal error: %v", p)})
			}
		}()
		c := &Ctx{App: a, W: w, R: r}
		out, err := fn(c)
		switch {
		case err != nil:
			status, body := errorStatus(err)
			if mutating && r.ContentLength > 0 {
				w.Header().Set("Connection", "close") // the request body may be half read: this connection is out of step
			}
			writeJSON(w, r, status, body)
		case out != nil:
			writeJSON(w, r, 200, out)
		}
	}
}
