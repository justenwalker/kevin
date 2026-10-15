package runapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/justenwalker/kevin/internal/runstate"
)

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrUnknownStep reports that the environment declares no such step.
const ErrUnknownStep = Error("runapi: no such step")

// ErrBusy reports that the step is already running, so the rerun did not
// start.
const ErrBusy = Error("runapi: step is busy")

// Console routes, shared by the client and the console's handlers.
const (
	StatusPath = "/api/status"
	RerunPath  = "/api/rerun"
)

// maxReason bounds how much of an error response body the client reports.
const maxReason = 512

// statusTimeout bounds a status request, so a wedged console fails fast
// instead of hanging the caller (ADR-0004). A rerun has no timeout: it is
// bound to the caller's context, so an interrupt cancels it.
const statusTimeout = 5 * time.Second

// Client talks to a running kevin over the console listener.
type Client struct {
	base string
	hc   *http.Client
}

// Dial returns a Client for the run listening at addrs.
func Dial(addrs runstate.Addrs) *Client {
	return &Client{base: "http://" + addrs.ConsoleAddr, hc: &http.Client{}}
}

// Status asks for the current step states.
func (c *Client) Status(ctx context.Context) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()

	var out Status
	if err := c.do(ctx, http.MethodGet, StatusPath, nil, &out); err != nil {
		return Status{}, fmt.Errorf("runapi: status: %w", err)
	}
	return out, nil
}

// Rerun runs the step again and returns once it finishes. A step that ran
// and failed is a result with Failed set, not an error. A step that is
// already running is [ErrBusy], and one the environment does not declare is
// [ErrUnknownStep].
func (c *Client) Rerun(ctx context.Context, req RerunRequest) (RerunResult, error) {
	var out RerunResult
	if err := c.do(ctx, http.MethodPost, RerunPath, req, &out); err != nil {
		return RerunResult{}, fmt.Errorf("runapi: rerun: %w", err)
	}
	return out, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, &body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return ErrUnknownStep
	case http.StatusConflict:
		return ErrBusy
	default:
		reason, _ := io.ReadAll(io.LimitReader(res.Body, maxReason))
		if msg := strings.TrimSpace(string(reason)); msg != "" {
			return fmt.Errorf("console returned %s: %s", res.Status, msg)
		}
		return fmt.Errorf("console returned %s", res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
