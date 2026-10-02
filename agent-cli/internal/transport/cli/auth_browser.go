package cli

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

// openSystemBrowser opens url in the user's default browser. The URL is
// passed as one argument, never through a shell. The launchers hand the URL
// to the running browser and exit, so waiting for them is short.
func openSystemBrowser(ctx context.Context, url string) error {
	name, args := browserCommand(runtime.GOOS, url)
	if err := exec.CommandContext(ctx, name, args...).Run(); err != nil {
		return fmt.Errorf("run %s: %w", name, err)
	}
	return nil
}

func browserCommand(goos, url string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return "xdg-open", []string{url}
	}
}
