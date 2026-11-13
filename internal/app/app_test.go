package app

import "testing"

func TestResolveHome(t *testing.T) {
	t.Setenv("AGENTS_HOME", "/from/env")
	if got, _ := resolveHome("/from/flag"); got != "/from/flag" {
		t.Errorf("flag: got %s", got)
	}
	if got, _ := resolveHome(""); got != "/from/env" {
		t.Errorf("env: got %s", got)
	}

	t.Setenv("AGENTS_HOME", "")
	t.Setenv("HOME", "/users/me")
	if got, _ := resolveHome(""); got != "/users/me/.agents-cli" {
		t.Errorf("default: got %s", got)
	}
	t.Setenv("AGENTS_HOME", "~/x")
	if got, _ := resolveHome(""); got != "/users/me/x" {
		t.Errorf("~ in AGENTS_HOME: got %s", got)
	}
	if got, _ := resolveHome("~/y"); got != "/users/me/y" {
		t.Errorf("~ in --home: got %s", got)
	}
}
