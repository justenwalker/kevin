package console

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/session"
)

func TestSSEMessage(t *testing.T) {
	t.Run("gives every line its own data field", func(t *testing.T) {
		// A raw newline inside a data field ends the event. Every line must
		// carry its own prefix, or the browser sees a truncated fragment.
		got := string(sseMessage("<div>\n  <span>hi</span>\n</div>"))

		assert.Equal(t, "data: <div>\ndata:   <span>hi</span>\ndata: </div>\n\n", got)
	})

	t.Run("ends with a blank line", func(t *testing.T) {
		got := string(sseMessage("one line"))

		assert.Equal(t, "data: one line\n\n", got)
		assert.True(t, strings.HasSuffix(got, "\n\n"), "an event ends on a blank line")
	})
}

func TestStatusClass(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{status: 0, want: "status-0"},
		{status: 200, want: "status-2"},
		{status: 301, want: "status-3"},
		{status: 404, want: "status-4"},
		{status: 500, want: "status-5"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, statusClass(tt.status))
		})
	}
}

func TestRoutedClass(t *testing.T) {
	assert.Equal(t, "routed", routedClass(true))
	assert.Equal(t, "direct", routedClass(false))
}

func TestPage(t *testing.T) {
	t.Run("renders every region and the connection", func(t *testing.T) {
		store := session.NewStore()
		s := New(Config{Project: "demo", Network: "kevin-demo", Store: store})
		store.SetProxyAddr("127.0.0.1:8080")
		store.AddStep("web", "", "", "", nil, nil, false, "", false)
		store.SetStep("web", Ready, "")
		store.AddStepDetail("web", Detail{Value: "web.kevin.test", Href: "https://web.kevin.test"})
		store.Log("web", "stdout", "hello")
		store.Record(Request{Method: "GET", Host: "web.kevin.test", Path: "/", Status: 200, Routed: true})

		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/", nil))

		require.Equal(t, 200, rec.Code)
		body := rec.Body.String()

		assert.Contains(t, body, `hx-sse:connect="/events"`, "the page must open the stream")
		assert.Contains(t, body, `id="steps"`)
		assert.Contains(t, body, `id="log-all"`)
		assert.Contains(t, body, `id="log-web"`, "a step gets its own log panel")
		assert.Contains(t, body, `id="traffic"`)
		assert.Contains(t, body, `id="cards"`)
		assert.Contains(t, body, `<li id="step-web"`)
		assert.Contains(t, body, `id="card-web"`)
		assert.Contains(t, body, "web.kevin.test")
		assert.Contains(t, body, "hello")
		assert.Contains(t, body, "/static/htmx.min.js")
		assert.Contains(t, body, "demo")
		assert.Contains(t, body, `id="dep-lines"`, "the sidebar needs the svg overlay for dependency lines")
	})

	t.Run("a sensitive detail is masked but stays copyable", func(t *testing.T) {
		store := session.NewStore()
		s := New(Config{Project: "demo", Network: "kevin-demo", Store: store})
		store.AddStep("db", "", "", "", nil, nil, false, "", false)
		store.SetStep("db", Ready, "")
		store.AddStepDetail("db", Detail{
			Label: "admin password", Value: "hunter2", Href: "https://admin.kevin.test", Copyable: true, Sensitive: true,
		})

		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/", nil))

		require.Equal(t, 200, rec.Code)
		body := rec.Body.String()

		assert.Contains(t, body, "admin password", "the label is not secret and still shows")
		assert.Contains(t, body, `data-copy="hunter2"`, "the copy button still carries the real value, so copy-to-clipboard keeps working")
		assert.NotContains(t, body, "https://admin.kevin.test", "a sensitive detail must not render as a link")
		assert.NotContains(t, body, `title="hunter2"`, "a sensitive value must not leak into a tooltip")
		assert.NotContains(t, body, ">hunter2<", "a sensitive value must not render as visible text")
	})

	t.Run("a step item carries needs for the dependency lines", func(t *testing.T) {
		store := session.NewStore()
		s := New(Config{Project: "demo", Network: "kevin-demo", Store: store})
		store.AddStep("a", "", "", "", nil, nil, false, "", false)
		store.AddStep("b", "", "", "", nil, []string{"a"}, false, "", false)

		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/", nil))

		body := rec.Body.String()
		assert.Contains(t, body, `data-needs="a"`, "the js reads this to draw a line from b to a")
		assert.Contains(t, body, `data-needs=""`, "a step with no dependencies still carries the attribute, just empty")
	})

	t.Run("a group nests its members, collapsed by default", func(t *testing.T) {
		store := session.NewStore()
		s := New(Config{Project: "demo", Network: "kevin-demo", Store: store})
		store.AddStep("db", "", "group", "", nil, []string{"net"}, false, "", true)
		store.AddStep("db.primary", "", "", "echo", nil, nil, false, "db", false)

		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/", nil))

		body := rec.Body.String()
		assert.Contains(t, body, `<li id="step-db" class="group"`, "the group gets its own collapsible row")
		assert.Contains(t, body, `id="group-header-db"`)
		assert.Contains(t, body, `<li id="step-db.primary"`, "a member nests inside its group's row")
		assert.Contains(t, body, `<input type="checkbox" id="group-toggle-db" class="group-toggle" onchange="drawDepLines()">`,
			"collapsed by default: the toggle carries no checked attribute")
		assert.Equal(t, 1, strings.Count(body, `id="step-db.primary"`), "a member never appears a second time at the top level")
	})

	t.Run("a denied request renders differently from an allowed one", func(t *testing.T) {
		store := session.NewStore()
		s := New(Config{Project: "demo", Network: "kevin-demo", Store: store})
		store.Record(Request{Method: "GET", Host: "api.kevin.test", Path: "/", Status: 200, Routed: true})
		store.Record(Request{Method: "GET", Host: "evil.test", Path: "/", Status: 403, Denied: true})

		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/", nil))

		require.Equal(t, 200, rec.Code)
		body := rec.Body.String()

		assert.Contains(t, body, `class="status-2 routed"`,
			"an allowed request must not carry the denied class")
		assert.Contains(t, body, `class="status-4 direct denied"`,
			"a denied request must carry its own class so the row stands out")
	})

	t.Run("shows setup guidance targeted at the requesting browser", func(t *testing.T) {
		store := session.NewStore()
		s := New(Config{Project: "demo", Network: "kevin-demo", Store: store})
		store.SetProxyAddr("127.0.0.1:8080")

		tests := []struct {
			name string
			ua   string
			want string
		}{
			{
				name: "Firefox",
				ua:   "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:128.0) Gecko/20100101 Firefox/128.0",
				want: "Firefox keeps its own proxy settings",
			},
			{
				name: "Chrome",
				ua:   "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
				want: "Chrome follows the OS's network proxy settings",
			},
			{
				name: "Safari",
				ua:   "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
				want: "Safari follows macOS's network proxy settings",
			},
			{
				name: "unknown",
				ua:   "curl/8.7.1",
				want: "Point your browser's proxy settings",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				req := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
				req.Header.Set("User-Agent", tt.ua)

				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, req)

				body := rec.Body.String()
				assert.Contains(t, body, "127.0.0.1:8080/proxy.pac", "the PAC URL is always shown")
				assert.Contains(t, body, "export HTTP_PROXY=http://127.0.0.1:8080", "the shell export line is always shown")
				assert.Contains(t, body, tt.want)
			})
		}
	})

	t.Run("static assets are embedded", func(t *testing.T) {
		s := New(Config{Project: "demo", Network: "kevin-demo", Store: session.NewStore()})

		for _, name := range []string{"htmx.min.js", "hx-sse.min.js"} {
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/static/"+name, nil))

			assert.Equal(t, 200, rec.Code, "%s must ship in the binary", name)
			assert.NotEmpty(t, rec.Body.Bytes())
		}
	})
}

func TestEvents(t *testing.T) {
	t.Run("sends a snapshot at once", func(t *testing.T) {
		store := session.NewStore()
		s := New(Config{Project: "demo", Network: "kevin-demo", Store: store})
		store.AddStep("web", "", "", "", nil, nil, false, "", false)
		store.SetStep("web", Ready, "")
		store.Log("web", "stdout", "already here")

		frame := readFrame(t, s)

		// A browser that reconnects would otherwise hold stale rows until the
		// next change, which may never come.
		assert.Contains(t, frame, `<li id="step-web"`)
		assert.Contains(t, frame, "already here")
		assert.Contains(t, frame, `hx-target="#log-all"`)
		assert.Contains(t, frame, `hx-target="#log-web"`, "a step's own panel is also repainted")
		assert.Contains(t, frame, `hx-target="#traffic"`)
	})

	t.Run("streams a later change", func(t *testing.T) {
		store := session.NewStore()
		s := New(Config{Project: "demo", Network: "kevin-demo", Store: store})

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		rec := &syncRecorder{}
		req := httptest.NewRequestWithContext(ctx, "GET", "/events", nil)

		done := make(chan struct{})
		go func() {
			defer close(done)
			s.Handler().ServeHTTP(rec, req)
		}()

		// Wait for the subscription, then change something.
		require.Eventually(t, func() bool {
			s.clientsMu.Lock()
			defer s.clientsMu.Unlock()
			return len(s.clients) == 1
		}, time.Second, 5*time.Millisecond)

		store.SetStep("api", Running, "")
		store.Record(Request{Method: "GET", Host: "api.test", Path: "/x", Status: 201, Routed: true})

		require.Eventually(t, func() bool {
			return strings.Contains(rec.String(), "step-api") &&
				strings.Contains(rec.String(), "201")
		}, 2*time.Second, 10*time.Millisecond)

		cancel()
		<-done

		body := rec.String()
		assert.Contains(t, body, `hx-swap-oob="true"`, "a step row replaces itself out of band")
		assert.Contains(t, body, `hx-swap="afterbegin"`, "a request goes to the top of the table")
	})

	t.Run("a group's state change swaps only its header, not the whole row", func(t *testing.T) {
		store := session.NewStore()
		s := New(Config{Project: "demo", Network: "kevin-demo", Store: store})
		store.AddStep("db", "", "group", "", nil, nil, false, "", true)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		rec := &syncRecorder{}
		req := httptest.NewRequestWithContext(ctx, "GET", "/events", nil)

		done := make(chan struct{})
		go func() {
			defer close(done)
			s.Handler().ServeHTTP(rec, req)
		}()

		require.Eventually(t, func() bool {
			s.clientsMu.Lock()
			defer s.clientsMu.Unlock()
			return len(s.clients) == 1
		}, time.Second, 5*time.Millisecond)

		store.SetStep("db", Ready, "")

		require.Eventually(t, func() bool {
			return strings.Contains(rec.String(), "group-header-db")
		}, 2*time.Second, 10*time.Millisecond)

		cancel()
		<-done

		body := rec.String()
		assert.Contains(t, body, `id="group-header-db" hx-swap-oob="true"`,
			"only the header swaps, so an already-expanded group's checkbox is never replaced")
		assert.NotContains(t, body, `<li id="step-db"`,
			"the group's outer row must never be replaced whole - that would reset its expand/collapse state")
	})

	t.Run("a log line routes to both the all panel and the step's own panel", func(t *testing.T) {
		store := session.NewStore()
		s := New(Config{Project: "demo", Network: "kevin-demo", Store: store})

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		rec := &syncRecorder{}
		req := httptest.NewRequestWithContext(ctx, "GET", "/events", nil)

		done := make(chan struct{})
		go func() {
			defer close(done)
			s.Handler().ServeHTTP(rec, req)
		}()

		require.Eventually(t, func() bool {
			s.clientsMu.Lock()
			defer s.clientsMu.Unlock()
			return len(s.clients) == 1
		}, time.Second, 5*time.Millisecond)

		store.Log("web", "stdout", "building the image")

		require.Eventually(t, func() bool {
			return strings.Contains(rec.String(), "building the image")
		}, 2*time.Second, 10*time.Millisecond)

		cancel()
		<-done

		body := rec.String()
		assert.Contains(t, body, `hx-target="#log-all"`, "the line reaches the All panel")
		assert.Contains(t, body, `hx-target="#log-web"`, "the same line also reaches the step's own panel")
	})

	t.Run("publish drops a client that falls behind", func(t *testing.T) {
		store := session.NewStore()
		s := New(Config{Project: "demo", Network: "kevin-demo", Store: store})
		ch := s.subscribe()

		// Fill the buffer and then some. The supervisor must never block on a
		// browser that stopped reading.
		for range clientBuffer + 10 {
			store.SetStep("web", Running, "")
		}

		s.clientsMu.Lock()
		remaining := len(s.clients)
		s.clientsMu.Unlock()

		assert.Equal(t, 0, remaining, "a client that cannot keep up is dropped")

		// The channel is closed, thus a reader sees the end rather than a hang.
		require.Eventually(t, func() bool {
			for range ch {
				continue
			}
			return true
		}, time.Second, 10*time.Millisecond)
	})
}

// readFrame opens the stream, reads the first event, and closes it.
func readFrame(t *testing.T, s *Server) string {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	rec := &syncRecorder{}
	req := httptest.NewRequestWithContext(ctx, "GET", "/events", nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(rec, req)
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(rec.String(), "data: ")
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	<-done

	// Strip the data prefixes, which is what the browser does.
	var b strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(rec.String()))
	for scanner.Scan() {
		b.WriteString(strings.TrimPrefix(scanner.Text(), "data: "))
	}
	return b.String()
}

// syncRecorder collects a streaming response. httptest.ResponseRecorder is not
// safe for concurrent use, and a test of a stream reads while the handler
// writes.
type syncRecorder struct {
	mu     sync.Mutex
	buf    strings.Builder
	header http.Header
}

func (r *syncRecorder) Header() http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.header == nil {
		r.header = http.Header{}
	}
	return r.header
}

func (r *syncRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *syncRecorder) WriteHeader(int) {}

func (r *syncRecorder) Flush() {}

func (r *syncRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}
