// Package console serves the kevin web console.
//
//	store := session.NewStore()
//	c := console.New(console.Config{Project: "demo", Network: "kevin-demo", Store: store, Rerun: rerun})
//	mux := http.NewServeMux()
//	c.RegisterRoutes(mux)
//	go httpserver.Serve(ctx, listener, mux)
//	store.SetStep("api", session.Running, "")
//
// The page renders the state that store holds, then opens one event
// stream. Every later change to store arrives on that stream as an out of
// band fragment that names its own target.
package console

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/a-h/templ"

	"github.com/justenwalker/kevin/internal/browser"
	"github.com/justenwalker/kevin/internal/logging"
	"github.com/justenwalker/kevin/internal/mcpserver"
	"github.com/justenwalker/kevin/internal/session"
)

var log = logging.New("console")

//go:embed static
var static embed.FS

// clientBuffer is how far one browser may fall behind before the server
// drops it. A dropped client reconnects and gets the state again.
const clientBuffer = 256

type (
	// State aliases session.State, so the rest of this package - and its
	// templates - can keep referring to it as console.State.
	State = session.State

	// Step aliases session.Step.
	Step = session.Step

	// Detail aliases session.Detail.
	Detail = session.Detail

	// Line aliases session.Line.
	Line = session.Line

	// Request aliases session.Request.
	Request = session.Request
)

// The states of a step.
const (
	Pending  = session.Pending
	Running  = session.Running
	Ready    = session.Ready
	Failed   = session.Failed
	Skipped  = session.Skipped
	Removing = session.Removing
	Removed  = session.Removed
)

// View is everything the page needs to render.
type View struct {
	session.View

	// Project is the name of the project the console is running.
	Project string
	// McpURL is the MCP server's URL.
	McpURL string
	// Network is the name of the docker network the project runs on.
	Network string
	// Browser is the kind of browser viewing the page, detected from its
	// User-Agent - used to target the Proxy tab's setup instructions.
	Browser browser.Kind
}

// Server serves the page for one session's state, held by a
// [session.Store] the caller owns and mutates. The zero value is not
// usable. Call [New]. A Server is safe for concurrent use.
type Server struct {
	project string // the project name
	network string // the docker network the project runs on
	store   *session.Store
	rerunFn func(ctx context.Context, step string, cascade bool) error

	clientsMu sync.Mutex // guards clients
	clients   map[chan []byte]struct{}
}

// Config is the console's construction-time dependencies, passed to [New].
type Config struct {
	// Project is the name of the project the console is running.
	Project string
	// Network is the name of the docker network the project runs on.
	Network string
	// Store holds the session state the console renders and streams.
	Store *session.Store
	// Rerun is the func the page's rerun buttons call. Without one, a
	// rerun request reports an error and changes nothing.
	Rerun func(ctx context.Context, step string, cascade bool) error
}

// New builds a console for one project, streaming store's changes to every
// connected browser.
func New(cfg Config) *Server {
	s := &Server{
		project: cfg.Project,
		network: cfg.Network,
		store:   cfg.Store,
		rerunFn: cfg.Rerun,
		clients: map[chan []byte]struct{}{},
	}
	cfg.Store.OnChange(s.onChange)
	return s
}

// onChange renders the fragment for one change to store and pushes it to
// every connected browser.
func (s *Server) onChange(e session.Event) {
	switch v := e.(type) {
	case session.Step:
		s.publish(StepUpdate(v))
	case session.Line:
		// The per-step panel is the same line, routed to a second target -
		// no separate per-step storage. Snapshot() derives a step's tail by
		// filtering the buffer.
		s.publish(oobLog(v))
		s.publish(oobStepLog(v))
	case session.Request:
		s.publish(oobTraffic(v))
	}
}

// View returns the state that the page renders. host is the console's own
// address, as the requesting client dialed it (an http.Request's Host), used
// to build the MCP server's URL - mounted alongside the console, so it
// shares the same host.
func (s *Server) View(host string) View {
	return View{
		View:    s.store.Snapshot(),
		Project: s.project,
		McpURL:  "http://" + host + mcpserver.Path,
		Network: s.network,
	}
}

// RegisterRoutes registers the page, the stream, and the assets on mux.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("GET /static/", http.FileServerFS(static))
	mux.HandleFunc("GET /events", s.events)
	mux.HandleFunc("POST /steps/{name}/rerun", s.rerun)
	mux.HandleFunc("GET /{$}", s.page)
}

// Handler returns an http.Handler serving the console's own routes - a
// convenience for callers that don't need to share a mux with anything
// else. See [Server.RegisterRoutes].
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	return mux
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	v := s.View(r.Host)
	v.Browser = browser.Detect(r.UserAgent())
	if err := Page(v).Render(r.Context(), w); err != nil {
		log.Ctx(r.Context()).Debug("render the page", "error", err)
	}
}

// rerun asks the registered rerun func to re-execute one step. The result
// arrives on the SSE stream as a StepUpdate, the same way every other step
// change does, so this handler itself has nothing to render.
func (s *Server) rerun(w http.ResponseWriter, r *http.Request) {
	if s.rerunFn == nil {
		http.Error(w, "rerun is not available", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	cascade, _ := strconv.ParseBool(r.FormValue("cascade"))
	if err := s.rerunFn(r.Context(), name, cascade); err != nil {
		log.Ctx(r.Context()).Debug("rerun step", "step", name, "error", err)
		// A step that ran and failed already reported it over the SSE
		// stream (RerunStep's own doc comment); this handler has nothing
		// to add. A step rejected before it ran at all (ErrStepBusy) never
		// touched the store, so it's the one case worth an HTTP error.
		if errors.Is(err, session.ErrStepBusy) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

// events streams every change until the client goes away.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := s.subscribe()
	defer s.unsubscribe(ch)

	// Repaint at once. A browser that reconnects after a drop would otherwise
	// hold stale rows until the next change, which may never come.
	if _, err := w.Write(render(Snapshot(s.View(r.Host)))); err != nil {
		return
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				// publish dropped this client for falling too far behind.
				return
			}
			if _, err := w.Write(msg); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) subscribe() chan []byte {
	ch := make(chan []byte, clientBuffer)

	s.clientsMu.Lock()
	s.clients[ch] = struct{}{}
	s.clientsMu.Unlock()

	return ch
}

func (s *Server) unsubscribe(ch chan []byte) {
	s.clientsMu.Lock()
	delete(s.clients, ch)
	s.clientsMu.Unlock()
}

// render turns a component into one framed event.
func render(c templ.Component) []byte {
	var buf bytes.Buffer
	if err := c.Render(context.Background(), &buf); err != nil {
		log.Ctx(context.Background()).Debug("render an event", "error", err)
		return nil
	}
	return sseMessage(buf.String())
}

// publish renders a component and sends it to every client.
func (s *Server) publish(c templ.Component) {
	msg := render(c)
	if msg == nil {
		return
	}

	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()

	for ch := range s.clients {
		select {
		case ch <- msg:
		default:
			// The browser fell too far behind. Drop it rather than block the
			// supervisor; it reconnects and reloads the state.
			delete(s.clients, ch)
			close(ch)
		}
	}
}

// sseMessage frames HTML as one unnamed event. Every line needs its own data
// field, and the client joins them with a newline.
func sseMessage(html string) []byte {
	var b strings.Builder
	for line := range strings.SplitSeq(strings.TrimRight(html, "\n"), "\n") {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return []byte(b.String())
}

// iconDataURI encodes a step's icon (already validated PNG bytes, or nil)
// as a data: URI the page can use for an <img> src.
func iconDataURI(icon []byte) string {
	if len(icon) == 0 {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(icon)
}

// statusClass groups a status code for the stylesheet.
func statusClass(status int) string {
	return "status-" + strconv.Itoa(status/100)
}

// routedClass marks a request that went to the internet rather than to a
// workload.
func routedClass(routed bool) string {
	if routed {
		return "routed"
	}
	return "direct"
}
