package chatgptauth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// BrowserLogin configures LoginBrowser.
type BrowserLogin struct {
	// Out receives the sign-in URL and progress lines.
	Out io.Writer
	// OpenBrowser opens the sign-in URL; nil only prints it. A failure to
	// open is reported on Out and the login keeps waiting for the redirect.
	OpenBrowser func(ctx context.Context, url string) error
	// Ports are the loopback ports to try (default 1455, then 1457).
	Ports []int
	// Random is the entropy source (default crypto/rand).
	Random io.Reader
}

// LoginBrowser runs the PKCE browser login: it binds the loopback callback,
// prints and opens the authorize URL, waits for the redirect, and exchanges
// the code for a credential.
func (c *Client) LoginBrowser(ctx context.Context, opts BrowserLogin) (Credential, error) {
	random := opts.Random
	if random == nil {
		random = rand.Reader
	}
	pkce, err := NewPKCE(random)
	if err != nil {
		return Credential{}, err
	}
	state, err := NewState(random)
	if err != nil {
		return Credential{}, err
	}
	server, err := ListenCallback(ctx, state, opts.Ports...)
	if err != nil {
		return Credential{}, err
	}
	code, waitErr := c.awaitBrowserCode(ctx, server, opts, c.AuthorizeURL(server.RedirectURI(), pkce, state))
	closeErr := server.Close()
	if waitErr != nil {
		return Credential{}, waitErr
	}
	if closeErr != nil {
		return Credential{}, fmt.Errorf("close ChatGPT login callback: %w", closeErr)
	}
	return c.ExchangeCode(ctx, code, pkce.Verifier, server.RedirectURI())
}

func (c *Client) awaitBrowserCode(ctx context.Context, server *CallbackServer, opts BrowserLogin, authorizeURL string) (string, error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	if err := report(out, "Sign in to ChatGPT in your browser:\n\n  %s\n\nWaiting for the redirect to %s ...\n", authorizeURL, server.RedirectURI()); err != nil {
		return "", err
	}
	if opts.OpenBrowser != nil {
		if err := opts.OpenBrowser(ctx, authorizeURL); err != nil {
			if reportErr := report(out, "Could not open a browser (%v); open the URL above yourself.\n", err); reportErr != nil {
				return "", reportErr
			}
		}
	}
	return server.Wait(ctx)
}

// DeviceLogin configures LoginDevice.
type DeviceLogin struct {
	// Out receives the verification URL and the one-time code.
	Out io.Writer
	// Sleep waits between polls (default WaitContext).
	Sleep Sleeper
}

// LoginDevice runs the device-code login for hosts without a browser.
func (c *Client) LoginDevice(ctx context.Context, opts DeviceLogin) (Credential, error) {
	code, err := c.RequestDeviceCode(ctx)
	if err != nil {
		return Credential{}, err
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	if err := report(out, "1. Open %s and sign in to ChatGPT.\n2. Enter the one-time code %s (expires in %s).\n\nOnly continue if you started this login yourself.\nWaiting for approval ...\n",
		code.VerificationURL, code.UserCode, DeviceCodeLifetime); err != nil {
		return Credential{}, err
	}
	return c.CompleteDeviceCode(ctx, code, opts.Sleep)
}

func report(out io.Writer, format string, args ...any) error {
	if _, err := fmt.Fprintf(out, format, args...); err != nil {
		return errors.Join(errors.New("write ChatGPT login prompt"), err)
	}
	return nil
}
