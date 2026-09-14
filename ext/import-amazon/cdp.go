package amazon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A DevTools Protocol client over the WebSocket above: launch Chrome, attach
// to a tab, navigate it, run a script in it, read the result. That is the
// whole of what fetching a page through a real browser needs, and it is the
// reason this module drives Chrome directly rather than through Playwright or
// chromedp — each of those would be a dependency the ext/ rule forbids, for
// four methods.
//
// Why a real browser at all is answered in the package comment. The short
// version: Amazon renders a product page's variations and hi-res image list
// with JavaScript, and serves a robot check to anything that does not look
// like a browser; a browser that *is* a browser is the only honest way to see
// what a shopper sees.

// browser is one Chrome process and the connection to it.
type browser struct {
	cmd *exec.Cmd
	ws  *wsConn
	log *slog.Logger

	nextID  atomic.Int64
	pending sync.Map // id → chan cdpMessage

	emu     sync.Mutex
	waiters []*eventWaiter

	done    chan struct{}
	readErr error
}

// cdpMessage is the one shape every DevTools frame takes, in either direction.
type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    any             `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type eventWaiter struct {
	sessionID, method string
	ch                chan json.RawMessage
}

// findChrome looks where Chrome is usually installed, and honours CHROME_PATH
// first for the machine where it is not.
func findChrome() (string, error) {
	if p := os.Getenv("CHROME_PATH"); p != "" {
		return p, nil
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		for _, base := range []string{
			os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData"),
		} {
			if base != "" {
				candidates = append(candidates, filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"))
			}
		}
	case "darwin":
		candidates = append(candidates,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium")
	default:
		for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
			if p, err := exec.LookPath(name); err == nil {
				return p, nil
			}
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", errors.New("no Chrome found — set Config.ChromePath or CHROME_PATH")
}

// launch starts Chrome with a DevTools endpoint and connects to it.
//
// The port is 0 so Chrome picks a free one and writes it to DevToolsActivePort
// in the profile directory; reading that file is the documented way to find
// the endpoint and avoids racing another process for a fixed port.
func launch(ctx context.Context, path, profileDir string, headless bool, log *slog.Logger) (*browser, error) {
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return nil, fmt.Errorf("chrome: profile directory: %w", err)
	}
	portFile := filepath.Join(profileDir, "DevToolsActivePort")
	_ = os.Remove(portFile) // a stale one from a previous run would be read as live

	args := []string{
		"--user-data-dir=" + profileDir,
		"--remote-debugging-port=0",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-sync",
		"--window-size=1280,900",
	}
	if headless {
		args = append(args, "--headless=new")
	}
	args = append(args, "about:blank")

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("chrome: start %s: %w", path, err)
	}

	port, err := waitForPort(ctx, portFile, cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}

	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := getJSON(ctx, "http://127.0.0.1:"+strconv.Itoa(port)+"/json/version", &version); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("chrome: read the DevTools endpoint: %w", err)
	}
	ws, err := dialWS(ctx, wsURLFromHTTP(version.WebSocketDebuggerURL))
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}

	b := &browser{cmd: cmd, ws: ws, log: log, done: make(chan struct{})}
	go b.readLoop()
	return b, nil
}

// waitForPort polls for the file Chrome writes once its endpoint is listening.
// Its first line is the port; the second is the browser target's path.
func waitForPort(ctx context.Context, portFile string, cmd *exec.Cmd) (int, error) {
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case err := <-exited:
			return 0, fmt.Errorf("chrome exited before its DevTools endpoint came up: %v", err)
		case <-deadline.C:
			return 0, errors.New("chrome did not open a DevTools endpoint within 30s")
		case <-tick.C:
			raw, err := os.ReadFile(portFile)
			if err != nil {
				continue
			}
			line, _, _ := strings.Cut(string(raw), "\n")
			port, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil || port == 0 {
				continue
			}
			// cmd.Wait has been claimed by the goroutine above; hand the
			// exit back through a channel the closer can drain.
			go func() { <-exited }()
			return port, nil
		}
	}
}

func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func (b *browser) readLoop() {
	defer close(b.done)
	for {
		raw, err := b.ws.readMessage()
		if err != nil {
			b.readErr = err
			// Wake every caller still waiting on a reply.
			b.pending.Range(func(key, value any) bool {
				close(value.(chan cdpMessage))
				b.pending.Delete(key)
				return true
			})
			return
		}
		var msg cdpMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.ID != 0 {
			if ch, ok := b.pending.LoadAndDelete(msg.ID); ok {
				ch.(chan cdpMessage) <- msg
			}
			continue
		}
		b.dispatchEvent(msg)
	}
}

func (b *browser) dispatchEvent(msg cdpMessage) {
	b.emu.Lock()
	defer b.emu.Unlock()
	kept := b.waiters[:0]
	for _, w := range b.waiters {
		if w.method == msg.Method && w.sessionID == msg.SessionID {
			params, _ := json.Marshal(msg.Params)
			select {
			case w.ch <- params:
			default:
			}
			continue // one-shot: a waiter is satisfied once
		}
		kept = append(kept, w)
	}
	b.waiters = kept
}

// waitFor registers interest in the next event of a kind *before* the call
// that will cause it, so a load that fires quickly is not missed.
func (b *browser) waitFor(sessionID, method string) <-chan json.RawMessage {
	w := &eventWaiter{sessionID: sessionID, method: method, ch: make(chan json.RawMessage, 1)}
	b.emu.Lock()
	b.waiters = append(b.waiters, w)
	b.emu.Unlock()
	return w.ch
}

// call sends one command and waits for its reply.
func (b *browser) call(ctx context.Context, sessionID, method string, params any, out any) error {
	id := b.nextID.Add(1)
	ch := make(chan cdpMessage, 1)
	b.pending.Store(id, ch)

	payload, err := json.Marshal(cdpMessage{ID: id, SessionID: sessionID, Method: method, Params: params})
	if err != nil {
		b.pending.Delete(id)
		return err
	}
	if err := b.ws.writeText(payload); err != nil {
		b.pending.Delete(id)
		return fmt.Errorf("chrome: send %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		b.pending.Delete(id)
		return ctx.Err()
	case msg, ok := <-ch:
		if !ok {
			return fmt.Errorf("chrome: connection closed during %s: %v", method, b.readErr)
		}
		if msg.Error != nil {
			return fmt.Errorf("chrome: %s: %s (%d)", method, msg.Error.Message, msg.Error.Code)
		}
		if out != nil && len(msg.Result) > 0 {
			return json.Unmarshal(msg.Result, out)
		}
		return nil
	}
}

// close asks Chrome to quit and makes sure it does.
func (b *browser) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = b.call(ctx, "", "Browser.close", nil, nil)
	_ = b.ws.close()
	select {
	case <-b.done:
	case <-time.After(2 * time.Second):
	}
	if b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
	}
	return nil
}

// tab is one attached page target.
type tab struct {
	b         *browser
	targetID  string
	sessionID string
}

// newTab opens a page and attaches to it in "flat" mode, which is the mode
// where every later command names the session rather than being tunnelled.
func (b *browser) newTab(ctx context.Context) (*tab, error) {
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := b.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
		return nil, err
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.call(ctx, "", "Target.attachToTarget",
		map[string]any{"targetId": created.TargetID, "flatten": true}, &attached); err != nil {
		return nil, err
	}
	t := &tab{b: b, targetID: created.TargetID, sessionID: attached.SessionID}
	if err := b.call(ctx, t.sessionID, "Page.enable", nil, nil); err != nil {
		return nil, err
	}
	return t, nil
}

// navigate loads a URL and returns once the page's load event has fired, plus
// a short settle so scripts that run on load — Amazon's variation widget among
// them — have written what the extractor reads.
func (t *tab) navigate(ctx context.Context, url string, settle time.Duration) error {
	loaded := t.b.waitFor(t.sessionID, "Page.loadEventFired")
	var nav struct {
		ErrorText string `json:"errorText"`
	}
	if err := t.b.call(ctx, t.sessionID, "Page.navigate", map[string]any{"url": url}, &nav); err != nil {
		return err
	}
	if nav.ErrorText != "" {
		return fmt.Errorf("chrome: navigate to %s: %s", url, nav.ErrorText)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-loaded:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(settle):
	}
	return nil
}

// evaluate runs a script and returns what it returned, by value.
func (t *tab) evaluate(ctx context.Context, expression string, out any) error {
	var res struct {
		Result struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := t.b.call(ctx, t.sessionID, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	}, &res); err != nil {
		return err
	}
	if res.ExceptionDetails != nil {
		detail := res.ExceptionDetails.Text
		if res.ExceptionDetails.Exception != nil {
			detail = res.ExceptionDetails.Exception.Description
		}
		return fmt.Errorf("chrome: the extraction script threw: %s", detail)
	}
	if out == nil || len(res.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(res.Result.Value, out)
}

func (t *tab) close(ctx context.Context) error {
	return t.b.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": t.targetID}, nil)
}
