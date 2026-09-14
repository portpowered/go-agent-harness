package agentruntime

import (
	"context"

	runtimeBrowser "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

type sessionBrowserRecorder = runtimeBrowser.Recorder

func newSessionBrowserRecorder(opts SessionRunOptions, credentials []string) (sessionBrowserRecorder, error) {
	if opts.LoadedConfig == nil || !opts.LoadedConfig.Browser.Recording.Enabled || opts.BrowserEventWatch == nil || opts.BrowserConversation == nil {
		return nil, nil
	}
	settings := opts.LoadedConfig.Browser.Recording
	return opts.BrowserConversation.NewRecorder(runtimeBrowser.RecordingRequest{
		Watch: opts.BrowserEventWatch, IncludeArguments: settings.IncludeArguments, IncludeResults: settings.IncludeResults,
		RedactURLQuery: settings.RedactURLQuery, RedactURLFragment: settings.RedactURLFragment, Credentials: credentials,
	})
}

func (r *sessionDirectoryRecording) startBrowser(ctx context.Context) {
	if r != nil && r.browser != nil {
		r.browser.Start(ctx)
	}
}
