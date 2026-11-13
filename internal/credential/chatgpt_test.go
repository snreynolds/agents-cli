package credential

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeJWT returns an unsigned JWT shaped like OpenAI's: numeric claims (exp,
// iat) as well as the object claim holding the ChatGPT account ID.
func fakeJWT(account string) string {
	claims, _ := json.Marshal(map[string]any{
		"exp": 1791505154, "iat": 1790641154, "sub": "user-1",
		chatGPTAccountClaim: map[string]any{"chatgpt_account_id": account},
	})
	return "x." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

// fakeOpenAI serves OpenAI's sign-in endpoints. The device sign-in is
// approved on the second poll.
func fakeOpenAI(t *testing.T) (*httptest.Server, *url.Values) {
	t.Helper()
	var lastTokenForm url.Values
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/accounts/deviceauth/usercode", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["client_id"] != chatGPTClientID {
			http.Error(w, "bad client", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"device_auth_id": "dev-1", "user_code": "ABCD-1234", "interval": "0"}`)
	})
	mux.HandleFunc("POST /api/accounts/deviceauth/token", func(w http.ResponseWriter, r *http.Request) {
		if polls.Add(1) == 1 {
			w.WriteHeader(http.StatusForbidden) // not approved yet
			return
		}
		_, _ = io.WriteString(w, `{"authorization_code": "auth-code", "code_verifier": "verifier"}`)
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		lastTokenForm = r.PostForm
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  fakeJWT("acct-1"),
			"refresh_token": "rt-" + r.PostForm.Get("grant_type"),
			"expires_in":    864000,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &lastTokenForm
}

func TestChatGPT_finishSignIn(t *testing.T) {
	srv, form := fakeOpenAI(t)
	c := ChatGPT{AuthBase: srv.URL}
	d, err := c.startSignIn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.code != "ABCD-1234" || d.url != srv.URL+"/codex/device" {
		t.Errorf("device = %q at %q", d.code, d.url)
	}
	d.interval = time.Millisecond // keep the test fast
	tok, err := c.finishSignIn(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Refresh != "rt-authorization_code" || tok.Account != "acct-1" || time.Until(tok.Expires) < 9*24*time.Hour {
		t.Errorf("signed in: refresh %q, account %q, expires %v", tok.Refresh, tok.Account, tok.Expires)
	}
	if form.Get("code") != "auth-code" || form.Get("code_verifier") != "verifier" ||
		form.Get("client_id") != chatGPTClientID || form.Get("redirect_uri") != srv.URL+"/deviceauth/callback" {
		t.Errorf("exchange sent %v", *form)
	}
}

func TestChatGPT_Renew(t *testing.T) {
	srv, form := fakeOpenAI(t)
	tok, err := ChatGPT{AuthBase: srv.URL}.Renew(context.Background(), Entry{Refresh: "rt-old"})
	if err != nil {
		t.Fatal(err)
	}
	if tok.Refresh != "rt-refresh_token" || form.Get("refresh_token") != "rt-old" {
		t.Errorf("renewed: refresh %q; sent %v", tok.Refresh, *form)
	}
}

// Replies hold secrets, so an error never quotes one: only the status and
// a short, safe OAuth error code.
func TestChatGPT_errorsNeverQuoteReplies(t *testing.T) {
	for name, reply := range map[string]string{
		"error code":        `{"error": "invalid_grant", "error_description": "token sk-SECRET was used"}`,
		"nested code":       `{"error": {"code": "refresh_token_reused", "message": "sk-SECRET"}}`,
		"code holds secret": `{"error": "sk-SECRET with spaces"}`,
		"not json":          `sk-SECRET`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, reply)
			}))
			defer srv.Close()
			_, err := ChatGPT{AuthBase: srv.URL}.Renew(context.Background(), Entry{Refresh: "rt"})
			if err == nil || strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "400") {
				t.Errorf("err = %v; want the status, never the reply", err)
			}
		})
	}
}

// A successful reply that isn't valid JSON isn't quoted either.
func TestChatGPT_badJSONNotQuoted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"access_token": "sk-SECRET"`)
	}))
	defer srv.Close()
	if _, err := (ChatGPT{AuthBase: srv.URL}).Renew(context.Background(), Entry{Refresh: "rt"}); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("err = %v", err)
	}
}

// Requests carrying codes and tokens never follow a redirect elsewhere.
func TestChatGPT_noRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/oauth/token", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	if _, err := (ChatGPT{AuthBase: srv.URL}).Renew(context.Background(), Entry{Refresh: "rt"}); err == nil {
		t.Error("want an error for a redirect")
	}
	if elsewhere.Load() != 0 {
		t.Error("the request followed the redirect")
	}
}

func TestChatGPT_missingFields(t *testing.T) {
	for name, reply := range map[string]string{
		"no refresh token":   `{"access_token": "` + fakeJWT("a") + `", "expires_in": 10}`,
		"no expiry":          `{"access_token": "` + fakeJWT("a") + `", "refresh_token": "rt"}`,
		"no account":         `{"access_token": "x.e30.y", "refresh_token": "rt", "expires_in": 10}`,
		"implausible expiry": `{"access_token": "` + fakeJWT("a") + `", "refresh_token": "rt", "expires_in": 99999999999}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, reply) }))
			defer srv.Close()
			if _, err := (ChatGPT{AuthBase: srv.URL}).Renew(context.Background(), Entry{Refresh: "rt"}); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestChatGPT_startSignIn_unavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := (ChatGPT{AuthBase: srv.URL}).startSignIn(context.Background()); err == nil || !strings.Contains(err.Error(), "isn't available") {
		t.Errorf("err = %v", err)
	}
}

// Cancelling (Ctrl-C) stops the wait with context.Canceled.
func TestChatGPT_finishSignIn_cancelled(t *testing.T) {
	srv, _ := fakeOpenAI(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ChatGPT{AuthBase: srv.URL}.finishSignIn(ctx, device{authID: "d", code: "c", interval: time.Second, expires: time.Now().Add(time.Minute)})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestChatGPT_pollDevice(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		reply  string
		want   pollOutcome
	}{
		"forbidden: pending": {http.StatusForbidden, ``, pollPending},
		"pending code":       {http.StatusBadRequest, `{"error": {"code": "deviceauth_authorization_pending"}}`, pollPending},
		"slow down":          {http.StatusBadRequest, `{"error": "slow_down"}`, pollSlowDown},
		"approved":           {http.StatusOK, `{"authorization_code": "a", "code_verifier": "v"}`, pollApproved},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, c.reply)
			}))
			defer srv.Close()
			_, got, err := ChatGPT{AuthBase: srv.URL}.pollDevice(context.Background(), device{})
			if err != nil || got != c.want {
				t.Errorf("pollDevice = %v, %v; want %v", got, err, c.want)
			}
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error": "access_denied"}`)
	}))
	defer srv.Close()
	if _, _, err := (ChatGPT{AuthBase: srv.URL}).pollDevice(context.Background(), device{}); err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("a denied sign-in: %v", err)
	}
}

func TestSeconds(t *testing.T) {
	for raw, want := range map[string]time.Duration{`5`: 5 * time.Second, `"5"`: 5 * time.Second, `" 2.5 "`: 2500 * time.Millisecond, `0`: 0} {
		if got, ok := seconds(json.RawMessage(raw)); !ok || got != want {
			t.Errorf("seconds(%s) = %v, %v; want %v", raw, got, ok, want)
		}
	}
	for _, raw := range []string{`-1`, `"x"`, `null`, `99999`, ``} {
		if _, ok := seconds(json.RawMessage(raw)); ok {
			t.Errorf("seconds(%s) accepted", raw)
		}
	}
}

// A code that would put terminal codes on the user's screen is refused.
func TestChatGPT_startSignIn_badCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"device_auth_id": "d", "user_code": "\u001b[2JAB", "interval": 1}`)
	}))
	defer srv.Close()
	if _, err := (ChatGPT{AuthBase: srv.URL}).startSignIn(context.Background()); err == nil {
		t.Error("want an error for an unsafe code")
	}
}

// When the code expires before the user approves, the error says so; a
// deadline of the caller's own isn't reported as the code expiring.
func TestChatGPT_finishSignIn_codeExpires(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // never approved
	}))
	defer srv.Close()
	c := ChatGPT{AuthBase: srv.URL}
	soon := device{authID: "d", code: "c", interval: time.Millisecond, expires: time.Now().Add(50 * time.Millisecond)}
	if _, err := c.finishSignIn(context.Background(), soon); !errors.Is(err, errCodeExpired) {
		t.Errorf("err = %v, want the code expired", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	later := device{authID: "d", code: "c", interval: time.Millisecond, expires: time.Now().Add(time.Minute)}
	if _, err := c.finishSignIn(ctx, later); err == nil || errors.Is(err, errCodeExpired) {
		t.Errorf("err = %v, want the caller's deadline, not the code expiring", err)
	}
}

func TestClaimString(t *testing.T) {
	if got, err := claimString(fakeJWT("acct-9"), chatGPTAccountClaim, "chatgpt_account_id"); err != nil || got != "acct-9" {
		t.Errorf("claimString = %q, %v", got, err)
	}
	for name, jwt := range map[string]string{
		"not a JWT":           "abc",
		"bad base64":          "x.!!!.y",
		"claim missing":       "x." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp": 1}`)) + ".y",
		"claim not an object": "x." + base64.RawURLEncoding.EncodeToString([]byte(`{"`+chatGPTAccountClaim+`": 5}`)) + ".y",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := claimString(jwt, chatGPTAccountClaim, "chatgpt_account_id"); err == nil {
				t.Error("want an error")
			}
		})
	}
}

// fakeUI is a credential.UI for tests: piped answers, no terminal unless
// set, and a record of the sign-in code shown.
type fakeUI struct {
	interactive bool
	answer      string
	shownCode   string
	ran         [][]string // programs RunProgram was asked to run
	runErr      error
}

func (u *fakeUI) Interactive() bool                { return u.interactive }
func (u *fakeUI) AskSecret(string) (string, error) { return u.answer, nil }
func (u *fakeUI) ShowSignInCode(_, code string)    { u.shownCode = code }
func (u *fakeUI) RunProgram(c []string) error      { u.ran = append(u.ran, c); return u.runErr }

// Signing in needs a terminal to show the code on, and a cancel says
// nothing was saved.
func TestChatGPT_SignIn(t *testing.T) {
	srv, _ := fakeOpenAI(t)
	spec := Spec{ID: "chatgpt", Source: ChatGPT{AuthBase: srv.URL}}
	if _, err := spec.Source.SignIn(context.Background(), spec, &fakeUI{}); err == nil || !strings.Contains(err.Error(), "on a terminal") {
		t.Errorf("SignIn without a terminal = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := spec.Source.SignIn(ctx, spec, &fakeUI{interactive: true}); !errors.Is(err, ErrCancelled) {
		t.Errorf("cancelled SignIn = %v, want ErrCancelled", err)
	}
}
