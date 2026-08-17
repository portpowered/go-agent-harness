package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
	"github.com/spf13/cobra"
)

// MediaProbeFunc is injected so command tests and composition roots can own
// the source boundary without making the CLI duplicate protocol behavior.
type MediaProbeFunc func(context.Context, string) (rtc.MediaCapabilities, error)

// MediaCommand is the media command group. Root-router registration is owned
// by the CLI composition lane; this type is independently usable in tests.
type MediaCommand struct {
	Probe   MediaProbeFunc
	Timeout time.Duration
}

// NewMediaCommand constructs the media command group. An omitted probe uses
// the gateway's real context-aware source probe.
func NewMediaCommand(probe ...MediaProbeFunc) *MediaCommand {
	command := &MediaCommand{Timeout: rtc.DefaultMediaSourceTimeout}
	if len(probe) > 0 {
		command.Probe = probe[0]
	}
	return command
}

// Generate returns the media command group and its probe subcommand.
func (c *MediaCommand) Generate() *cobra.Command {
	probe := &MediaProbeCommand{Probe: c.Probe, Timeout: c.Timeout}
	cmd := &cobra.Command{
		Use:   "media",
		Short: "Inspect external media sources",
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(probe.Generate())
	return cmd
}

// MediaProbeCommand implements `agent media probe <url>`.
type MediaProbeCommand struct {
	Probe   MediaProbeFunc
	Timeout time.Duration
}

// NewMediaProbeCommand constructs the probe command with an injected source
// probe. The default is used when no dependency is supplied.
func NewMediaProbeCommand(probe ...MediaProbeFunc) *MediaProbeCommand {
	command := &MediaProbeCommand{Timeout: rtc.DefaultMediaSourceTimeout}
	if len(probe) > 0 {
		command.Probe = probe[0]
	}
	return command
}

// Generate returns the Cobra command for `media probe`.
func (c *MediaProbeCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "probe <url>",
		Short: "Probe an external go2rtc or RTSP media source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.Run(cmd.Context(), cmd.OutOrStdout(), args[0])
		},
	}
}

// Run executes a probe and renders a deterministic human-readable report.
func (c *MediaProbeCommand) Run(ctx context.Context, out io.Writer, rawURL string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = rtc.DefaultMediaSourceTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	probe := c.Probe
	if probe == nil {
		probe = rtc.ProbeMediaSource
	}
	caps, err := probe(probeCtx, rawURL)
	if err != nil {
		// The wrapped error remains errors.Is/errors.As compatible with the
		// typed source failure and contains only the source's safe identity.
		return fmt.Errorf("media probe: %w", err)
	}
	codec := caps.AudioCodec
	if codec == "" {
		codec = caps.Codec
	}
	rate := caps.SampleRate
	if rate == 0 {
		rate = caps.AudioSampleRate
	}
	channels := caps.Channels
	if channels == 0 {
		channels = caps.AudioChannels
	}
	video := caps.Video || caps.HasVideo || caps.VideoPresent
	if caps.Source == "" || codec == "" || rate <= 0 || channels <= 0 {
		return fmt.Errorf("media probe returned incomplete negotiated track evidence")
	}
	_, err = fmt.Fprintf(out, "Source: %s\nAudio codec: %s\nSample rate: %d\nChannels: %d\nVideo presence: %t\n", caps.Source, codec, rate, channels, video)
	return err
}

// RunMediaProbe is a small function-shaped entry point for composition roots.
func RunMediaProbe(ctx context.Context, out io.Writer, rawURL string, probe MediaProbeFunc, timeout time.Duration) error {
	return (&MediaProbeCommand{Probe: probe, Timeout: timeout}).Run(ctx, out, rawURL)
}
