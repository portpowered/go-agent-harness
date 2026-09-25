package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeRecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	"github.com/spf13/cobra"
)

// SessionReplayCommand presents the offline bundle replay service.
type SessionReplayCommand struct{ service runtimeReplay.StrictService }

func NewSessionReplayCommand(service runtimeReplay.StrictService) *SessionReplayCommand {
	return &SessionReplayCommand{service: service}
}

func (c *SessionReplayCommand) Generate() *cobra.Command {
	return &cobra.Command{
		SilenceUsage: true,
		Use:          "replay <bundle-directory>",
		Short:        "Replay recorded provider and tool events through the agent loop offline",
		Long:         "Replay a --record-dir bundle through the agent loop using recorded provider and tool events. No credentials, live tools, or devices are used. Recorded render audio remains evidence of the original run; this command does not reproduce physical device behavior.",
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if c == nil || c.service == nil {
				return fmt.Errorf("session replay service is required")
			}
			result, err := c.service.Run(cmd.Context(), cmd.OutOrStdout(), runtimeReplay.Request{BundlePath: args[0]})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "Replay verified: %d wire events, %d tool calls. Recorded render audio: %t; render tap unavailable: %t.\n", result.WireEvents, result.ToolCalls, result.Scope.RecordedRender, result.Scope.RenderTapUnavailable)
			return err
		},
	}
}

// runReplayTranscript renders a provider-neutral turn capture replayed
// without caller audio. The replay service owns capture admission and
// ordering and the duration service owns transcript rendering; the host only
// presents the same startup disclosures as a live invocation. A --record-dir
// destination still receives the recording service's evidence bundle.
func (c *SessionCommand) runReplayTranscript(ctx context.Context, out io.Writer, request serviceSession.Request, inspection *runtimeReplay.CaptureInspection) (runErr error) {
	liveRequest, err := c.runtimeLiveRequest(ctx, request, inspection)
	if err != nil {
		return err
	}
	if capabilities := liveRequest.Capabilities; capabilities != nil && capabilities.Close != nil {
		defer func() { runErr = errors.Join(runErr, capabilities.Close()) }()
	}
	if err := writeRuntimeLiveAnnouncements(out, request, liveRequest, inspection); err != nil {
		return err
	}
	drain := func(runCtx context.Context) error {
		return drainReplayTranscript(runCtx, out, c.liveReplayService, inspection.CapturePath)
	}
	if strings.TrimSpace(request.RecordDirectory) != "" && c.recordingService != nil {
		options := runtimeRecording.LiveEvidenceOptions{Destination: request.RecordDirectory}
		if key := strings.TrimSpace(request.APIKey); key != "" {
			options.Credentials = []string{key}
		}
		err = c.recordingService.RunLiveEvidence(ctx, options, func(runCtx context.Context, _ runtimeRecording.LiveEvidence) error {
			return drain(runCtx)
		})
	} else {
		err = drain(ctx)
	}
	if err != nil {
		return fmt.Errorf("replay session capture %s: %w", request.ReplayPath, err)
	}
	return nil
}

func drainReplayTranscript(ctx context.Context, out io.Writer, service runtimeReplay.Service, path string) (runErr error) {
	capture, err := service.Replay(ctx, path)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, capture.Close()) }()
	transcript := durationwire.NewService()
	runErr = capture.Drain(ctx, func(msg messages.StreamMessage) error { return transcript.WriteMessage(out, msg) })
	return errors.Join(runErr, capture.Err())
}
