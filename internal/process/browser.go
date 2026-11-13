package process

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
)

// OpenBrowser opens a web address in the user's browser, without waiting
// for it. Only http and https addresses are opened, so the opener (open on
// macOS, xdg-open on Linux) can't be made to run a file or an app.
func OpenBrowser(address string) error {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("not a web address: %q", address)
	}
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	cmd := exec.Command(opener, u.String())
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap it whenever it exits
	return nil
}
