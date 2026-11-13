package credential

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSupported_valid(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Supported() {
		if c.ID == "" || c.Name == "" || c.Source == nil {
			t.Errorf("%s: ID, Name and Source are required", c.ID)
		}
		if seen[c.ID] {
			t.Errorf("%s is listed twice", c.ID)
		}
		seen[c.ID] = true
		if s, ok := c.Source.(SetupToken); ok && len(s.Command) == 0 {
			t.Errorf("%s: a SetupToken needs a Command", c.ID)
		}
	}
	if c, _ := Supported().Find("chatgpt"); !c.Renews() {
		t.Error("chatgpt should renew")
	}
	if c, _ := Supported().Find("openai"); c.Renews() {
		t.Error("an API key doesn't renew")
	}
}

func TestCatalog_Lookup(t *testing.T) {
	cat := Supported()
	if _, err := cat.Lookup("opnai"); err == nil || !strings.Contains(err.Error(), "did you mean openai?") {
		t.Errorf("got %v, want a suggestion", err)
	}
	if _, err := cat.Lookup("zzzzzz"); err == nil || strings.Contains(err.Error(), "did you mean") {
		t.Errorf("got %v, want a plain error without a suggestion", err)
	}
	if c, err := cat.Lookup("openai"); err != nil || c.Name != "OpenAI API key" {
		t.Errorf("Lookup(openai) = %v, %v", c.Name, err)
	}
}

func TestEditDistance(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"openai", "openai", 0}, {"opnai", "openai", 1}, {"claud", "claude", 1}, {"", "abc", 3}, {"anthropc", "anthropic", 1}} {
		if got := editDistance(c.a, c.b); got != c.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestEntry_hidesSecrets(t *testing.T) {
	e := Entry{Secret: "sk-secret", Refresh: "rt-secret", Account: "acct", Added: time.Now(), Expires: time.Now()}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		if got := fmt.Sprintf(format, e); strings.Contains(got, "sk-secret") || strings.Contains(got, "rt-secret") {
			t.Errorf("%s printed %q", format, got)
		}
	}
}

func TestAPIKey_SignIn(t *testing.T) {
	c := Spec{ID: "openai", Name: "OpenAI API key", Source: APIKey{}}
	if e, err := c.Source.SignIn(t.Context(), c, &fakeUI{answer: "sk-1"}); err != nil || e.Secret != "sk-1" {
		t.Errorf("SignIn = %q, %v", e.Secret, err)
	}
}

func TestSetupToken_SignIn(t *testing.T) {
	c := Spec{ID: "claude", Name: "Claude token", Source: SetupToken{Command: []string{"claude", "setup-token"}}}

	// On a terminal it runs the program, then reads the pasted token,
	// joining one copied wrapped across lines.
	ui := &fakeUI{interactive: true, answer: "sk-ant-oat01-aaaa\n  bbbb "}
	if e, err := c.Source.SignIn(t.Context(), c, ui); err != nil || e.Secret != "sk-ant-oat01-aaaabbbb" {
		t.Errorf("SignIn = %q, %v; want the wrapped token joined", e.Secret, err)
	}
	if len(ui.ran) != 1 || strings.Join(ui.ran[0], " ") != "claude setup-token" {
		t.Errorf("ran %q, want claude setup-token", ui.ran)
	}

	// Piped, the input is the token, and nothing is run.
	piped := &fakeUI{answer: "sk-ant-oat01-x"}
	if e, err := c.Source.SignIn(t.Context(), c, piped); err != nil || e.Secret != "sk-ant-oat01-x" || len(piped.ran) != 0 {
		t.Errorf("piped SignIn = %q, %v, ran %q", e.Secret, err, piped.ran)
	}

	// A program that fails stops the sign-in before asking for a token.
	failing := &fakeUI{interactive: true, answer: "sk-ant-oat01-x", runErr: errors.New("exit status 1")}
	if _, err := c.Source.SignIn(t.Context(), c, failing); err == nil || !strings.Contains(err.Error(), "running claude setup-token") {
		t.Errorf("SignIn with a failing program = %v", err)
	}

	// Ctrl-C while the program runs cancels the sign-in.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.Source.SignIn(ctx, c, &fakeUI{interactive: true, answer: "sk-ant-oat01-x"}); !errors.Is(err, ErrCancelled) {
		t.Errorf("cancelled SignIn = %v, want ErrCancelled", err)
	}
}
