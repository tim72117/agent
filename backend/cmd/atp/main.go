// Command atp is a CLI for developers to authenticate to the console API
// (internal/console) and push local tool schema changes, without opening
// the browser console for routine updates (e.g. from a script or CI job).
//
// Auth is a token minted once via `atp login` (email/password, entered
// interactively) and cached locally; every subsequent command reads that
// cached token and sends it as a bearer token, exactly like the browser
// console sends its session cookie — internal/console's withAuth accepts
// either.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/tim72117/agent/internal/toolschema"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "login":
		err = runLogin(args)
	case "list-apps":
		err = runListApps(args)
	case "create-app":
		err = runCreateApp(args)
	case "issue-key":
		err = runIssueKey(args)
	case "set-origin":
		err = runSetOrigin(args)
	case "save-tools":
		err = runSaveTools(args)
	case "get-tools":
		err = runGetTools(args)
	default:
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "atp:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  atp login [-api <url>]              sign in with email/password, typed into this terminal
  atp login --web [-api <url>] [-console <url>]
                                       sign in via a browser tab instead (see below)
  atp list-apps [-api <url>]
  atp create-app [-api <url>] <appId>
  atp issue-key [-api <url>] <appId>
  atp set-origin [-api <url>] <appId> <origin>
  atp save-tools [-api <url>] <appId> <tools.yaml>
  atp get-tools [-api <url>] <appId>

  -api and -console both default to https://agent.shuttle.tools (the
  deployed onagent service). -console (login --web only) is the origin
  the console front-end is served from — the CLI appends /app/cli-auth
  itself, since that's the path prefix the console is mounted under.
  Point either at a local onagent dev server with e.g. -api
  http://localhost:8080 -console http://localhost:8080.`)
}

// --- login -------------------------------------------------------------

func runLogin(args []string) error {
	web := false
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--web" {
			web = true
			continue
		}
		rest = append(rest, a)
	}
	if web {
		return runLoginWeb(rest)
	}
	return runLoginPassword(rest)
}

func runLoginPassword(args []string) error {
	base, rest := apiFlag(args)
	if len(rest) != 0 {
		return fmt.Errorf("login takes no extra arguments")
	}

	email, err := readLine("Email: ")
	if err != nil {
		return err
	}
	password, err := readPassword("Password: ")
	if err != nil {
		return err
	}

	client := &apiClient{base: base}
	cookie, err := client.login(email, password)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}

	name, _ := os.Hostname()
	if name == "" {
		name = "cli"
	}
	client.sessionCookie = cookie
	token, err := client.issueToken(name)
	if err != nil {
		return fmt.Errorf("issue token: %w", err)
	}

	if err := saveToken(token); err != nil {
		return fmt.Errorf("save token: %w", err)
	}

	fmt.Printf("Logged in. Token saved (labeled %q) — future commands won't ask again.\n", name)
	return nil
}

// runLoginWeb signs in via a browser tab instead of typing a password into
// this terminal: it starts a temporary local server, opens (or prints) a
// console URL for the user to approve in their browser, and waits for that
// page to redirect back to the local server with a freshly minted token.
// See docs/cli-device-flow-design.md for why this isn't the OAuth device
// flow (this machine needs to be able to run a local server and open a
// browser; the device flow doesn't need either, at the cost of more moving
// parts) and apps/console/src/CliAuthPage.tsx for the browser side.
func runLoginWeb(args []string) error {
	apiBase, rest := apiFlag(args)
	consoleBase, rest := consoleFlag(rest)
	if len(rest) != 0 {
		return fmt.Errorf("login --web takes no extra arguments")
	}

	name, _ := os.Hostname()
	if name == "" {
		name = "cli"
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start local server: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	client := &apiClient{base: apiBase}
	id, err := client.startCliAuth(redirectURI, name)
	if err != nil {
		listener.Close()
		return fmt.Errorf("start sign-in: %w", err)
	}

	result := make(chan callbackResult, 1)
	srv := &http.Server{Handler: callbackHandler(client, result)}
	go func() { _ = srv.Serve(listener) }()
	defer srv.Close()

	// The URL carries only this one opaque, single-use id — never a token,
	// never the redirect target itself. See
	// apps/console/src/CliAuthPage.tsx and internal/cliauth's package doc
	// for why that's the point: the actual redirect_uri was registered by
	// the startCliAuth call above, server-side, before this URL ever
	// existed, so nothing in the URL itself can redirect a minted token
	// anywhere an attacker chose.
	authURL := consoleBase + "/app/cli-auth?" + url.Values{"id": {id}}.Encode()

	fmt.Println("Opening your browser to sign in...")
	fmt.Println("If it doesn't open automatically, visit:")
	fmt.Println(" ", authURL)
	fmt.Println("Waiting for approval...")
	_ = openBrowser(authURL) // best-effort; the printed URL above is the fallback

	select {
	case res := <-result:
		if res.err != nil {
			return res.err
		}
		if err := saveToken(res.token); err != nil {
			return fmt.Errorf("save token: %w", err)
		}
		fmt.Printf("Logged in. Token saved (labeled %q) — future commands won't ask again.\n", name)
		return nil
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("timed out waiting for browser approval")
	}
}

type callbackResult struct {
	token string
	err   error
}

// callbackHandler serves the one-shot GET /callback CliAuthPage redirects
// the browser to after approval, carrying only ?code=<the same opaque id
// startCliAuth returned>. It exchanges that code for the actual token via
// the backend (client), so the plaintext token never travels through the
// browser or this URL at all — only this local process ever sees it,
// straight from the backend's response body.
//
// It only ever answers a single request meaningfully — result is a
// size-1 channel, so a second request (a stray reload, e.g.) just gets
// the same "you can close this" page without blocking or panicking on a
// second channel send.
func callbackHandler(client *apiClient, result chan<- callbackResult) http.HandlerFunc {
	var done bool
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><body style="font-family:sans-serif;padding:2rem">
<p>You can close this tab and return to your terminal.</p></body></html>`)

		if done {
			return
		}
		done = true

		code := r.URL.Query().Get("code")
		if code == "" {
			result <- callbackResult{err: fmt.Errorf("no code in callback (was the sign-in cancelled?)")}
			return
		}

		token, err := client.exchangeCliAuth(code)
		if err != nil {
			result <- callbackResult{err: fmt.Errorf("exchange failed: %w", err)}
			return
		}
		result <- callbackResult{token: token}
	}
}

// openBrowser best-effort launches url in the user's default browser.
// Failure isn't fatal — runLoginWeb always prints the URL too, so a
// headless environment or an unrecognized OS just falls back to the user
// copy-pasting it.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		// Not "cmd /c start": cmd.exe re-parses its own command line after
		// Go's argv-level quoting already happened, and treats an
		// unescaped "&" (which every URL here has — the query string
		// joins params with it) as a command separator, silently
		// truncating the URL at the first "&" and dropping every param
		// after it. rundll32 calls the URL-open API directly — no shell,
		// no second parsing pass, no special characters to worry about.
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// --- list-apps -----------------------------------------------------------

func runListApps(args []string) error {
	base, rest := apiFlag(args)
	if len(rest) != 0 {
		return fmt.Errorf("list-apps takes no extra arguments")
	}

	client, err := authenticatedClient(base)
	if err != nil {
		return err
	}

	apps, err := client.listApps()
	if err != nil {
		return fmt.Errorf("list apps: %w", err)
	}

	if len(apps) == 0 {
		fmt.Println("No apps yet.")
		return nil
	}
	for _, a := range apps {
		key := "no key"
		if a.HasKey {
			key = "has key"
		}
		fmt.Printf("%-30s %d tools, %s\n", a.AppID, a.ToolCount, key)
	}
	return nil
}

// --- create-app ------------------------------------------------------------

func runCreateApp(args []string) error {
	base, rest := apiFlag(args)
	if len(rest) != 1 {
		return fmt.Errorf("usage: atp create-app [-api <url>] <appId>")
	}
	appID := rest[0]

	client, err := authenticatedClient(base)
	if err != nil {
		return err
	}

	if _, err := client.createApp(appID); err != nil {
		return fmt.Errorf("create app: %w", err)
	}

	fmt.Printf("Created app %q.\n", appID)
	return nil
}

// --- issue-key ---------------------------------------------------------

func runIssueKey(args []string) error {
	base, rest := apiFlag(args)
	if len(rest) != 1 {
		return fmt.Errorf("usage: atp issue-key [-api <url>] <appId>")
	}
	appID := rest[0]

	client, err := authenticatedClient(base)
	if err != nil {
		return err
	}

	key, err := client.issueKey(appID)
	if err != nil {
		return fmt.Errorf("issue key: %w", err)
	}

	// Printed once, same as the console UI's KeyModal — the backend stores
	// only a hash, so this is the only chance to see the plaintext value.
	fmt.Printf("API key for %q (shown once — copy it now, it can't be retrieved again):\n", appID)
	fmt.Println(" ", key)
	fmt.Println("Issuing a new key later immediately revokes this one.")
	return nil
}

// --- set-origin ----------------------------------------------------------

func runSetOrigin(args []string) error {
	base, rest := apiFlag(args)
	if len(rest) != 2 {
		return fmt.Errorf("usage: atp set-origin [-api <url>] <appId> <origin>")
	}
	appID, origin := rest[0], rest[1]

	client, err := authenticatedClient(base)
	if err != nil {
		return err
	}

	if _, err := client.setOrigin(appID, origin); err != nil {
		return fmt.Errorf("set origin: %w", err)
	}

	fmt.Printf("Set %q's allowed origin to %q.\n", appID, origin)
	return nil
}

// --- save-tools ----------------------------------------------------------

func runSaveTools(args []string) error {
	base, rest := apiFlag(args)
	if len(rest) != 2 {
		return fmt.Errorf("usage: atp save-tools [-api <url>] <appId> <tools.yaml>")
	}
	appID, path := rest[0], rest[1]

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// Parsed as a full toolschema.App so a file already shaped like
	// backend/tools/*.yaml (appId + tools + thought) just works — only
	// .Tools is actually sent; the appId to target comes from the command
	// argument, not the file, so one file can be reused across apps.
	var app toolschema.App
	if err := yaml.Unmarshal(data, &app); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if err := app.Validate(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	client, err := authenticatedClient(base)
	if err != nil {
		return err
	}

	summary, err := client.saveTools(appID, app.Tools)
	if err != nil {
		return fmt.Errorf("save tools: %w", err)
	}

	fmt.Printf("Saved %d tools to %q.\n", summary.ToolCount, appID)
	return nil
}

// --- get-tools -----------------------------------------------------------

func runGetTools(args []string) error {
	base, rest := apiFlag(args)
	if len(rest) != 1 {
		return fmt.Errorf("usage: atp get-tools [-api <url>] <appId>")
	}
	appID := rest[0]

	client, err := authenticatedClient(base)
	if err != nil {
		return err
	}

	app, err := client.getApp(appID)
	if err != nil {
		return fmt.Errorf("get tools: %w", err)
	}

	// Marshaled straight back out as toolschema.App's own yaml tags
	// (appId + tools + thought) — the same shape save-tools reads, so this
	// output can be piped into a file and round-tripped back in.
	out, err := yaml.Marshal(app)
	if err != nil {
		return fmt.Errorf("encode yaml: %w", err)
	}
	fmt.Print(string(out))
	return nil
}

// --- api client ------------------------------------------------------------

type appSummary struct {
	AppID     string `json:"appId"`
	ToolCount int    `json:"toolCount"`
	HasKey    bool   `json:"hasKey"`
}

type apiClient struct {
	base string
	// sessionCookie is only ever set transiently during login, to
	// authenticate the one issueToken call that bootstraps a real token;
	// every other client in this program uses a bearer token instead.
	sessionCookie string
	token         string
}

// authenticatedClient loads the token saved by a prior `atp login` and
// fails with a clear next step if none exists yet, rather than letting
// every subsequent request fail with an opaque 401.
func authenticatedClient(base string) (*apiClient, error) {
	token, err := loadToken()
	if err != nil {
		return nil, fmt.Errorf("not logged in (run `atp login` first): %w", err)
	}
	return &apiClient{base: base, token: token}, nil
}

func (c *apiClient) login(email, password string) (cookie string, err error) {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, err := http.NewRequest(http.MethodPost, c.base+"/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach %s: %w", c.base, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", statusError(res)
	}

	for _, ck := range res.Cookies() {
		if ck.Name == "atp_session" {
			return ck.Value, nil
		}
	}
	return "", fmt.Errorf("login succeeded but no session cookie was returned")
}

func (c *apiClient) issueToken(name string) (string, error) {
	body, _ := json.Marshal(map[string]string{"name": name})
	req, err := http.NewRequest(http.MethodPost, c.base+"/console/tokens", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "atp_session", Value: c.sessionCookie})

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		return "", statusError(res)
	}

	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return out.Token, nil
}

// startCliAuth registers redirectURI with the backend (validated there as
// loopback-only) and returns the opaque session id that's safe to put in
// a URL — see internal/cliauth's package doc. Unauthenticated: this is
// the very first call of the whole login --web flow, before any
// credential exists yet.
func (c *apiClient) startCliAuth(redirectURI, name string) (id string, err error) {
	body, _ := json.Marshal(map[string]string{"redirectUri": redirectURI, "name": name})
	req, err := http.NewRequest(http.MethodPost, c.base+"/console/cli-auth/start", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach %s: %w", c.base, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		return "", statusError(res)
	}

	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return out.ID, nil
}

// exchangeCliAuth collects the token an approved session minted, once —
// see internal/cliauth.Store.Exchange. Unauthenticated: the id itself
// (already single-use and only ever handed to this same local process, in
// its own callback) is the credential here.
func (c *apiClient) exchangeCliAuth(id string) (token string, err error) {
	req, err := http.NewRequest(http.MethodPost, c.base+"/console/cli-auth/"+pathEscape(id)+"/exchange", nil)
	if err != nil {
		return "", err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach %s: %w", c.base, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", statusError(res)
	}

	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return out.Token, nil
}

func (c *apiClient) createApp(appID string) (appSummary, error) {
	body, err := json.Marshal(map[string]string{"appId": appID})
	if err != nil {
		return appSummary{}, err
	}

	res, err := c.do(http.MethodPost, "/console/apps", bytes.NewReader(body))
	if err != nil {
		return appSummary{}, err
	}
	defer res.Body.Close()

	var out appSummary
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return appSummary{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// issueKey returns the plaintext apiKey — the backend stores only its hash,
// so this is the caller's one and only chance to read it, same as the
// console UI's KeyModal.
func (c *apiClient) issueKey(appID string) (apiKey string, err error) {
	res, err := c.do(http.MethodPost, "/console/apps/"+pathEscape(appID)+"/key", nil)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	var out struct {
		ApiKey string `json:"apiKey"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return out.ApiKey, nil
}

func (c *apiClient) setOrigin(appID, origin string) (appSummary, error) {
	body, err := json.Marshal(map[string]string{"origin": origin})
	if err != nil {
		return appSummary{}, err
	}

	res, err := c.do(http.MethodPut, "/console/apps/"+pathEscape(appID)+"/origin", bytes.NewReader(body))
	if err != nil {
		return appSummary{}, err
	}
	defer res.Body.Close()

	var out appSummary
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return appSummary{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

func (c *apiClient) listApps() ([]appSummary, error) {
	res, err := c.do(http.MethodGet, "/console/apps", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	var out []appSummary
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

func (c *apiClient) saveTools(appID string, tools []toolschema.Tool) (appSummary, error) {
	body, err := json.Marshal(tools)
	if err != nil {
		return appSummary{}, err
	}

	res, err := c.do(http.MethodPut, "/console/apps/"+pathEscape(appID)+"/tools", bytes.NewReader(body))
	if err != nil {
		return appSummary{}, err
	}
	defer res.Body.Close()

	var out appSummary
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return appSummary{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// getApp returns the full App definition (every tool with its complete
// parameter and returns schema) — the same document runSaveTools sends,
// read back. Mirrors internal/console's getApp handler, which the web
// console's editor calls for the same purpose.
func (c *apiClient) getApp(appID string) (toolschema.App, error) {
	res, err := c.do(http.MethodGet, "/console/apps/"+pathEscape(appID), nil)
	if err != nil {
		return toolschema.App{}, err
	}
	defer res.Body.Close()

	var out toolschema.App
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return toolschema.App{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

func (c *apiClient) do(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", c.base, err)
	}
	if res.StatusCode >= 300 {
		defer res.Body.Close()
		return nil, statusError(res)
	}
	return res, nil
}

func statusError(res *http.Response) error {
	text, _ := io.ReadAll(res.Body)
	msg := strings.TrimSpace(string(text))
	if msg == "" {
		msg = res.Status
	}
	return fmt.Errorf("%s: %s", res.Status, msg)
}

func pathEscape(s string) string {
	// appIds are already constrained to [a-zA-Z0-9_-] (toolschema.ValidAppID),
	// so no character in a valid one ever needs percent-escaping; this just
	// guards against a malformed id doing anything unexpected to the URL
	// path rather than failing clearly server-side.
	return strings.ReplaceAll(s, "/", "%2F")
}

// --- token storage ---------------------------------------------------------

// tokenPath returns where the CLI caches its bearer token: a per-user
// config directory (resolved per-OS by os.UserConfigDir — e.g. %AppData%
// on Windows, ~/.config on Linux/macOS), not the working directory, so it
// survives across projects and isn't accidentally committed to one.
func tokenPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "onagent", "token"), nil
}

func saveToken(token string) error {
	path, err := tokenPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// 0600: the token is a bearer credential — anyone who reads this file
	// can act as this user via the console API.
	return os.WriteFile(path, []byte(token), 0600)
}

func loadToken() (string, error) {
	path, err := tokenPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// --- small input/flag helpers -----------------------------------------------

// defaultServerURL is where atp talks by default: the deployed onagent
// backend, not localhost — most people running this CLI are talking to the
// real service, not developing onagent itself. Both apiFlag and
// consoleFlag default here since the console front-end is embedded
// same-origin with the API in production (backend/cmd/server/web.go).
const defaultServerURL = "https://agent.shuttle.tools"

// apiFlag pulls an optional "-api <url>" out of args, wherever it appears
// (a hand-rolled parse, not the stdlib flag package, since flag doesn't
// compose with the "flags then positional args" subcommand shape used
// here), and returns the resolved base URL plus whatever args remain.
// Override with -api only — no environment variable, so there's exactly
// one way to point this somewhere else (e.g. -api http://localhost:8080
// for local onagent development).
func apiFlag(args []string) (base string, rest []string) {
	val, rest := extractFlag(args, "-api")
	base = defaultServerURL
	if val != "" {
		base = val
	}
	return strings.TrimSuffix(base, "/"), rest
}

// consoleFlag is apiFlag's counterpart for login --web: where the console
// front-end (not the API) is served, since that's what actually opens in
// the browser for the user to approve. Override with -console only — see
// apiFlag's comment for why there's no environment variable form.
func consoleFlag(args []string) (base string, rest []string) {
	val, rest := extractFlag(args, "-console")
	base = defaultServerURL
	if val != "" {
		base = val
	}
	return strings.TrimSuffix(base, "/"), rest
}

// extractFlag removes the first "name <value>" pair found anywhere in args
// (not just a leading position — login --web takes two such flags, and
// requiring a specific order between them would be a needless footgun) and
// returns the value plus args with that pair removed, in original order.
func extractFlag(args []string, name string) (value string, rest []string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) && value == "" {
			value = args[i+1]
			i++ // also skip the value on the next loop increment
			continue
		}
		rest = append(rest, args[i])
	}
	return value, rest
}

func readLine(prompt string) (string, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func readPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	bytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(bytes)), nil
}
