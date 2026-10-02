package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
	"github.com/spf13/cobra"
)

const (
	authStoreDirName  = "auth"
	authStoreFileName = "chatgpt.json"
)

// AuthCommand is the `auth` command group: sign-in for model providers.
type AuthCommand struct{}

// NewAuthCommand constructs the auth command group.
func NewAuthCommand() *AuthCommand { return &AuthCommand{} }

func (c *AuthCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "auth",
		Short: "Sign in to model providers",
		Long: "Sign in with a ChatGPT account instead of an OpenAI API key.\n\n" +
			"The credential is stored in <config-dir>/auth/chatgpt.json with owner-only permissions " +
			"and refreshed automatically. It authorizes the ChatGPT (Codex) backend, not the OpenAI Platform API.",
		Example: "  yui auth chatgpt\n  yui auth chatgpt --device\n  yui auth status\n  yui auth logout",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
}

// authEffects are the host effects auth commands use. Tests replace them to
// reach a fake issuer on virtual time.
type authEffects struct {
	config      chatgptauth.Config
	openBrowser func(ctx context.Context, url string) error
	sleep       chatgptauth.Sleeper
	ports       []int
}

func defaultAuthEffects() authEffects {
	return authEffects{openBrowser: openSystemBrowser}
}

func authStore(globalFlags *flags.GlobalFlags, effects authEffects) (*chatgptauth.FileStore, error) {
	dir := configDirForGlobalFlags(globalFlags)
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("no config directory: pass --config-dir")
	}
	options := []chatgptauth.StoreOption{}
	if effects.config.Now != nil {
		options = append(options, chatgptauth.WithStoreClock(effects.config.Now))
	}
	if effects.sleep != nil {
		options = append(options, chatgptauth.WithStoreSleeper(effects.sleep))
	}
	return chatgptauth.NewFileStore(filepath.Join(dir, authStoreDirName, authStoreFileName), options...), nil
}

// AuthChatGPTCommand signs in with a ChatGPT account.
type AuthChatGPTCommand struct {
	flags     *flags.GlobalFlags
	effects   authEffects
	device    bool
	noBrowser bool
}

// NewAuthChatGPTCommand constructs `auth chatgpt`.
func NewAuthChatGPTCommand(globalFlags *flags.GlobalFlags) *AuthChatGPTCommand {
	return &AuthChatGPTCommand{flags: globalFlags, effects: defaultAuthEffects()}
}

func (c *AuthChatGPTCommand) Generate() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chatgpt",
		Short: "Sign in with a ChatGPT account",
		Long: "Sign in with a ChatGPT account through the OAuth flow Codex CLI uses.\n\n" +
			"The browser flow listens on http://localhost:1455/auth/callback (or 1457). On a remote host, either " +
			"forward that port (ssh -L 1455:127.0.0.1:1455 host) and use --no-browser, or use --device.",
		Example: "  yui auth chatgpt\n  yui auth chatgpt --no-browser\n  yui auth chatgpt --device",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.run(cmd.Context(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&c.device, "device", false, "Sign in with a one-time device code instead of a browser redirect")
	cmd.Flags().BoolVar(&c.noBrowser, "no-browser", false, "Print the sign-in URL without opening a browser")
	cmd.MarkFlagsMutuallyExclusive("device", "no-browser")
	return cmd
}

func (c *AuthChatGPTCommand) run(ctx context.Context, out io.Writer) error {
	store, err := authStore(c.flags, c.effects)
	if err != nil {
		return err
	}
	client := chatgptauth.NewClient(c.effects.config)
	var cred chatgptauth.Credential
	if c.device {
		cred, err = client.LoginDevice(ctx, chatgptauth.DeviceLogin{Out: out, Sleep: c.effects.sleep})
	} else {
		login := chatgptauth.BrowserLogin{Out: out, OpenBrowser: c.effects.openBrowser, Ports: c.effects.ports}
		if c.noBrowser {
			login.OpenBrowser = nil
		}
		cred, err = client.LoginBrowser(ctx, login)
	}
	if err != nil {
		return err
	}
	if err := saveLocked(ctx, store, cred); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "\nSigned in to ChatGPT as %s.\nCredential saved to %s\n", describeAccount(cred), store.Path())
	return err
}

func saveLocked(ctx context.Context, store *chatgptauth.FileStore, cred chatgptauth.Credential) error {
	unlock, err := store.Lock(ctx)
	if err != nil {
		return err
	}
	return errors.Join(store.Save(cred), unlock())
}

func describeAccount(cred chatgptauth.Credential) string {
	name := cred.Email
	if name == "" {
		name = "account " + cred.AccountID
	}
	if cred.PlanType != "" {
		name += " (" + cred.PlanType + " plan)"
	}
	return name
}

// AuthStatusCommand reports the stored ChatGPT credential without using or
// refreshing it.
type AuthStatusCommand struct {
	flags   *flags.GlobalFlags
	effects authEffects
	json    bool
}

// NewAuthStatusCommand constructs `auth status`.
func NewAuthStatusCommand(globalFlags *flags.GlobalFlags) *AuthStatusCommand {
	return &AuthStatusCommand{flags: globalFlags, effects: defaultAuthEffects()}
}

func (c *AuthStatusCommand) Generate() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "status",
		Short:   "Show the stored ChatGPT sign-in",
		Long:    "Show which ChatGPT account is signed in and when its access token expires. Tokens are never printed.",
		Example: "  yui auth status\n  yui auth status --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.run(cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&c.json, "json", false, "Write the status as JSON")
	return cmd
}

type authStatus struct {
	Provider    string     `json:"provider"`
	SignedIn    bool       `json:"signed_in"`
	Email       string     `json:"email,omitempty"`
	AccountID   string     `json:"account_id,omitempty"`
	PlanType    string     `json:"plan_type,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Expired     bool       `json:"expired"`
	LastRefresh *time.Time `json:"last_refresh,omitempty"`
	Store       string     `json:"store"`
}

func (c *AuthStatusCommand) run(out io.Writer) error {
	store, err := authStore(c.flags, c.effects)
	if err != nil {
		return err
	}
	status := authStatus{Provider: "chatgpt", Store: store.Path()}
	cred, err := store.Load()
	switch {
	case errors.Is(err, chatgptauth.ErrNotLoggedIn):
	case err != nil:
		return err
	default:
		status = c.signedInStatus(status, cred)
	}
	if c.json {
		return json.NewEncoder(out).Encode(status)
	}
	return writeAuthStatus(out, status, c.now())
}

func (c *AuthStatusCommand) now() time.Time {
	if c.effects.config.Now != nil {
		return c.effects.config.Now()
	}
	return time.Now()
}

func (c *AuthStatusCommand) signedInStatus(status authStatus, cred chatgptauth.Credential) authStatus {
	status.SignedIn = true
	status.Email, status.AccountID, status.PlanType = cred.Email, cred.AccountID, cred.PlanType
	status.Expired = cred.Expired(c.now())
	if !cred.ExpiresAt.IsZero() {
		expires := cred.ExpiresAt.UTC()
		status.ExpiresAt = &expires
	}
	if !cred.LastRefresh.IsZero() {
		refreshed := cred.LastRefresh.UTC()
		status.LastRefresh = &refreshed
	}
	return status
}

func writeAuthStatus(out io.Writer, status authStatus, now time.Time) error {
	var b strings.Builder
	if !status.SignedIn {
		fmt.Fprintf(&b, "ChatGPT: not signed in. Run `yui auth chatgpt`.\nStore: %s\n", status.Store)
		_, err := io.WriteString(out, b.String())
		return err
	}
	fmt.Fprintf(&b, "ChatGPT: signed in\n")
	fmt.Fprintf(&b, "  Account:      %s\n", describeAccount(chatgptauth.Credential{Email: status.Email, AccountID: status.AccountID, PlanType: status.PlanType}))
	if status.AccountID != "" {
		fmt.Fprintf(&b, "  Account ID:   %s\n", status.AccountID)
	}
	switch {
	case status.ExpiresAt == nil:
		fmt.Fprintf(&b, "  Access token: expiry unknown\n")
	case status.Expired:
		fmt.Fprintf(&b, "  Access token: expired %s; it is refreshed on next use\n", status.ExpiresAt.Format(time.RFC3339))
	default:
		fmt.Fprintf(&b, "  Access token: expires %s (in %s)\n", status.ExpiresAt.Format(time.RFC3339), status.ExpiresAt.Sub(now).Round(time.Minute))
	}
	if status.LastRefresh != nil {
		fmt.Fprintf(&b, "  Last refresh: %s\n", status.LastRefresh.Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "  Store:        %s\n", status.Store)
	_, err := io.WriteString(out, b.String())
	return err
}

// AuthLogoutCommand revokes and deletes the stored ChatGPT credential.
type AuthLogoutCommand struct {
	flags   *flags.GlobalFlags
	effects authEffects
}

// NewAuthLogoutCommand constructs `auth logout`.
func NewAuthLogoutCommand(globalFlags *flags.GlobalFlags) *AuthLogoutCommand {
	return &AuthLogoutCommand{flags: globalFlags, effects: defaultAuthEffects()}
}

func (c *AuthLogoutCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:     "logout",
		Short:   "Sign out of ChatGPT",
		Long:    "Revoke the stored ChatGPT refresh token at the issuer (best effort), then delete the local credential.",
		Example: "  yui auth logout",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.run(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

func (c *AuthLogoutCommand) run(ctx context.Context, out, errOut io.Writer) error {
	store, err := authStore(c.flags, c.effects)
	if err != nil {
		return err
	}
	unlock, err := store.Lock(ctx)
	if err != nil {
		return err
	}
	message, logoutErr := c.logoutLocked(ctx, store, errOut)
	if err := errors.Join(logoutErr, unlock()); err != nil {
		return err
	}
	_, err = io.WriteString(out, message)
	return err
}

func (c *AuthLogoutCommand) logoutLocked(ctx context.Context, store *chatgptauth.FileStore, errOut io.Writer) (string, error) {
	cred, err := store.Load()
	if errors.Is(err, chatgptauth.ErrNotLoggedIn) {
		return "Not signed in to ChatGPT.\n", nil
	}
	if err == nil {
		if revokeErr := chatgptauth.NewClient(c.effects.config).Revoke(ctx, cred); revokeErr != nil {
			writeAdvisory(errOut, "warning: %v; deleting the local credential anyway\n", revokeErr)
		}
	}
	// An unreadable or insecure store is deleted without revoking: its
	// tokens cannot be trusted to belong to this user.
	if deleteErr := store.Delete(); deleteErr != nil {
		return "", deleteErr
	}
	return "Signed out of ChatGPT. Deleted " + store.Path() + "\n", nil
}
