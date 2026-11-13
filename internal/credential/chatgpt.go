package credential

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ChatGPT is the Source for ChatGPT subscriptions (Plus/Pro). It signs in
// the way OpenAI's Codex CLI and pi do, with OpenAI's device-code sign-in
// (the user enters a short code on OpenAI's site), and renews over HTTP.
// The zero value talks to OpenAI.
type ChatGPT struct {
	AuthBase string       // default https://auth.openai.com; tests use a fake server
	Client   *http.Client // default: a client with a timeout
}

// SignIn shows the user a code to enter on OpenAI's site and waits (up to
// 15 minutes, or until ctx is done) for them to approve it.
func (c ChatGPT) SignIn(ctx context.Context, spec Spec, ui UI) (Entry, error) {
	if !ui.Interactive() {
		return Entry{}, fmt.Errorf("%s signs in with your browser; run agents auth login %s on a terminal", spec.ID, spec.ID)
	}
	d, err := c.startSignIn(ctx)
	if err != nil {
		return Entry{}, cancelled(err)
	}
	ui.ShowSignInCode(d.url, d.code)
	e, err := c.finishSignIn(ctx, d)
	return e, cancelled(err)
}

// cancelled turns a sign-in stopped by the user (Ctrl-C) into the error
// that says nothing was saved.
func cancelled(err error) error {
	if errors.Is(err, context.Canceled) {
		return errCancelledNothingSaved
	}
	return err
}

// Renew exchanges e's refresh token for a new access token. The old refresh
// token stops working. Callers say what was being renewed; errors don't.
func (c ChatGPT) Renew(ctx context.Context, e Entry) (Entry, error) {
	return c.exchange(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {chatGPTClientID},
		"refresh_token": {e.Refresh},
	})
}

// The Codex CLI's public client ID and the claim holding the account ID, as
// pi 0.87.1 uses them (pi-ai/dist/auth/oauth/openai-codex.js).
const (
	chatGPTClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	chatGPTAccountClaim = "https://api.openai.com/auth"
)

// How long OpenAI keeps a device code valid (pi uses the same), and polling
// limits (RFC 8628).
const (
	deviceCodeLifetime = 15 * time.Minute
	minPollInterval    = time.Second
	maxPollInterval    = time.Hour // a longer interval in a reply is refused
	slowDownIncrement  = 5 * time.Second
)

// maxLifetimeSeconds is the longest credential lifetime agents believes a
// reply about: a year. ChatGPT credentials last about 10 days. It's in
// seconds, as replies give it, so the check can't overflow a Duration.
const maxLifetimeSeconds = 365 * 24 * 60 * 60

// validUserCode is what a code shown to the user may look like, so a reply
// can't put terminal escape codes on the user's screen.
var validUserCode = regexp.MustCompile(`^[A-Za-z0-9-]{4,32}$`)

// validErrorCode is what an OAuth error code from a reply may look like to be
// kept in an error message, so a reply can't put a secret in one.
var validErrorCode = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// errCodeExpired is why finishSignIn stops when the device code expires.
var errCodeExpired = errors.New("the ChatGPT sign-in code expired before it was approved; run the login again")

// maxResponse is the most agents reads of any response: far more than any
// real one, so a broken server can't make it read forever.
const maxResponse = 1 << 20

// device is a started sign-in, waiting for the user to enter code at url.
type device struct {
	url  string // where the user enters code
	code string // shown to the user, e.g. "ABCD-1234"

	authID   string
	interval time.Duration
	expires  time.Time // when OpenAI stops accepting code
}

// startSignIn asks OpenAI for a code for the user to enter.
func (c ChatGPT) startSignIn(ctx context.Context) (device, error) {
	var resp struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Interval     json.RawMessage `json:"interval"` // a number, or a number in a string
	}
	err := c.postJSON(ctx, "/api/accounts/deviceauth/usercode", map[string]string{"client_id": chatGPTClientID}, &resp)
	if statusOf(err) == http.StatusNotFound {
		return device{}, errors.New("ChatGPT device sign-in isn't available right now; try again later")
	}
	if err != nil {
		return device{}, fmt.Errorf("starting ChatGPT sign-in: %w", err)
	}
	interval, ok := seconds(resp.Interval)
	if resp.DeviceAuthID == "" || !validUserCode.MatchString(resp.UserCode) || !ok {
		return device{}, errors.New("starting ChatGPT sign-in: OpenAI's reply is incomplete or malformed")
	}
	return device{
		url:      c.base() + "/codex/device",
		code:     resp.UserCode,
		authID:   resp.DeviceAuthID,
		interval: max(interval, minPollInterval),
		expires:  time.Now().Add(deviceCodeLifetime),
	}, nil
}

// finishSignIn waits for the user to approve d on OpenAI's site, then
// returns the new credential. It gives up when the code expires (15
// minutes) or ctx is done.
func (c ChatGPT) finishSignIn(ctx context.Context, d device) (Entry, error) {
	ctx, cancel := context.WithDeadlineCause(ctx, d.expires, errCodeExpired)
	defer cancel()
	interval := d.interval
	for {
		if err := sleep(ctx, interval); err != nil {
			return Entry{}, waitError(ctx, err)
		}
		g, outcome, err := c.pollDevice(ctx, d)
		switch {
		case err != nil:
			return Entry{}, waitError(ctx, err)
		case outcome == pollSlowDown:
			interval += slowDownIncrement
		case outcome == pollApproved:
			e, err := c.exchange(ctx, url.Values{
				"grant_type":    {"authorization_code"},
				"client_id":     {chatGPTClientID},
				"code":          {g.code},
				"code_verifier": {g.verifier},
				"redirect_uri":  {c.base() + "/deviceauth/callback"},
			})
			if err != nil {
				return Entry{}, fmt.Errorf("finishing ChatGPT sign-in: %w", err)
			}
			return e, nil
		}
	}
}

// pollOutcome is what one poll of a device sign-in found.
type pollOutcome int

const (
	pollFailed   pollOutcome = iota // zero: returned with an error
	pollPending                     // the user hasn't approved yet
	pollSlowDown                    // polling too often
	pollApproved
)

// grant is what an approved device sign-in gives, to exchange for an Entry.
type grant struct{ code, verifier string }

// pollDevice checks once whether the user has approved d.
func (c ChatGPT) pollDevice(ctx context.Context, d device) (grant, pollOutcome, error) {
	var resp struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeVerifier      string `json:"code_verifier"`
	}
	err := c.postJSON(ctx, "/api/accounts/deviceauth/token", map[string]string{"device_auth_id": d.authID, "user_code": d.code}, &resp)
	var se *serverError
	switch {
	// OpenAI answers 403 or 404 until the user approves (as pi expects).
	case statusOf(err) == http.StatusForbidden || statusOf(err) == http.StatusNotFound:
		return grant{}, pollPending, nil
	case errors.As(err, &se) && se.code == "deviceauth_authorization_pending":
		return grant{}, pollPending, nil
	case errors.As(err, &se) && se.code == "slow_down":
		return grant{}, pollSlowDown, nil
	case err != nil:
		return grant{}, pollFailed, err
	case resp.AuthorizationCode == "" || resp.CodeVerifier == "":
		return grant{}, pollFailed, errors.New("OpenAI's reply is missing the sign-in code")
	}
	return grant{code: resp.AuthorizationCode, verifier: resp.CodeVerifier}, pollApproved, nil
}

// exchange posts a token request and returns the credential it grants
// (without Added, which the store sets).
func (c ChatGPT) exchange(ctx context.Context, form url.Values) (Entry, error) {
	var resp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"` // seconds
	}
	if err := c.post(ctx, "/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), &resp); err != nil {
		return Entry{}, err
	}
	if resp.AccessToken == "" || resp.RefreshToken == "" || resp.ExpiresIn <= 0 {
		return Entry{}, errors.New("OpenAI's reply is missing the login")
	}
	if resp.ExpiresIn > maxLifetimeSeconds {
		return Entry{}, errors.New("OpenAI's reply has an implausible expiry")
	}
	account, err := claimString(resp.AccessToken, chatGPTAccountClaim, "chatgpt_account_id")
	if err != nil {
		return Entry{}, err
	}
	return Entry{
		Secret:  resp.AccessToken,
		Refresh: resp.RefreshToken,
		Expires: time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second),
		Account: account,
	}, nil
}

// postJSON posts body as JSON; see post.
func (c ChatGPT) postJSON(ctx context.Context, path string, body any, into any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.post(ctx, path, "application/json", bytes.NewReader(data), into)
}

// statusOf returns the HTTP status of a failed reply, or 0 if err isn't one.
func statusOf(err error) int {
	var se *serverError
	if errors.As(err, &se) {
		return se.status
	}
	return 0
}

// post sends a request and decodes a successful reply into into. A failed
// reply becomes a *serverError. Replies hold secrets, so no error ever
// includes a reply's body: only its status and OAuth error code.
func (c ChatGPT) post(ctx context.Context, path, contentType string, body io.Reader, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &serverError{status: resp.StatusCode, code: errorCode(data)}
	}
	if err := json.Unmarshal(data, into); err != nil {
		return errors.New("OpenAI's reply isn't valid JSON") // not err: it can quote the reply
	}
	return nil
}

// serverError is a failed reply from OpenAI.
type serverError struct {
	status int
	code   string // the OAuth error code, if the reply had one
}

func (e *serverError) Error() string {
	if e.code != "" {
		return fmt.Sprintf("OpenAI replied %d (%s)", e.status, e.code)
	}
	return fmt.Sprintf("OpenAI replied %d", e.status)
}

// errorCode returns the OAuth error code in a failed reply: "error" as a
// string, or "error.code" (OpenAI's device endpoints). Only short codes made
// of safe characters are kept, so a reply can't put a secret in an error.
func errorCode(reply []byte) string {
	var body struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(reply, &body) != nil {
		return ""
	}
	var code string
	if json.Unmarshal(body.Error, &code) != nil {
		var nested struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(body.Error, &nested)
		code = nested.Code
	}
	if !validErrorCode.MatchString(code) {
		return ""
	}
	return code
}

// claimString reads a string from an object claim in a JWT's payload. The
// token's signature isn't checked: it came straight from OpenAI over HTTPS,
// and the value is only sent back to OpenAI.
func claimString(jwt, claim, field string) (string, error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return "", errors.New("the access token isn't a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", errors.New("the access token's claims aren't readable")
	}
	// Only the wanted claim is decoded as an object: others, such as exp and
	// iat, are numbers.
	var claims map[string]json.RawMessage
	var object map[string]any
	if json.Unmarshal(payload, &claims) != nil || json.Unmarshal(claims[claim], &object) != nil {
		return "", errors.New("the access token's claims aren't readable")
	}
	value, _ := object[field].(string)
	if value == "" {
		return "", fmt.Errorf("the access token has no %s", field)
	}
	return value, nil
}

// seconds reads a positive duration in seconds given as a number or as a
// number in a string.
func seconds(raw json.RawMessage) (time.Duration, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false // decoding null would quietly give 0
	}
	var n float64
	if json.Unmarshal(raw, &n) != nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return 0, false
		}
		var err error
		if n, err = strconv.ParseFloat(strings.TrimSpace(s), 64); err != nil {
			return 0, false
		}
	}
	if n < 0 || n > maxPollInterval.Seconds() {
		return 0, false
	}
	return time.Duration(n * float64(time.Second)), true
}

// sleep waits d, or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitError explains why waiting for a device sign-in stopped.
func waitError(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), errCodeExpired) {
		return errCodeExpired
	}
	if errors.Is(err, context.Canceled) {
		return err // the caller cancelled; it knows why
	}
	return fmt.Errorf("waiting for ChatGPT sign-in: %w", err)
}

func (c ChatGPT) base() string {
	if c.AuthBase != "" {
		return strings.TrimRight(c.AuthBase, "/")
	}
	return "https://auth.openai.com"
}

// defaultClient bounds every request, so a stalled connection can't hang a
// login or a run, and never follows redirects, so requests carrying codes
// and tokens only ever go to AuthBase. It uses Go's default transport, so
// HTTPS_PROXY and friends are honored, as in other command-line tools.
var defaultClient = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (c ChatGPT) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return defaultClient
}
