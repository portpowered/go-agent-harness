package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/spf13/cobra"
)

const xVideoMaxBytes = 64 * 1024 * 1024
const xVideoChunkBytes = 32 * 1024
const xVideoPreparationTimeout = 5 * time.Minute

// Read one bounded snapshot. The hash and every transmitted byte describe the
// same snapshot even if the caller replaces the source file during transfer.
func readXVideo(path string) ([]byte, string, error) {
	if !strings.EqualFold(filepath.Ext(path), ".mp4") {
		return nil, "", fmt.Errorf("video must be an MP4 file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open video: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Size() < 12 || info.Size() > xVideoMaxBytes {
		return nil, "", fmt.Errorf("video must be a regular file between 12 bytes and 64 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, xVideoMaxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) < 12 || len(data) > xVideoMaxBytes || string(data[4:8]) != "ftyp" {
		return nil, "", fmt.Errorf("invalid or oversized MP4; expected ftyp header")
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

type xVideoReply struct {
	OK   bool `json:"ok"`
	Data struct {
		UploadToken     string `json:"upload_token"`
		ReceivedBytes   int    `json:"received_bytes"`
		VideoProcessing bool   `json:"video_processing"`
		DraftToken      string `json:"draft_token"`
	} `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeXVideoReply(output json.RawMessage) (xVideoReply, error) {
	var reply xVideoReply
	if err := json.Unmarshal(output, &reply); err != nil {
		return reply, fmt.Errorf("invalid X adapter response: %w", err)
	}
	if !reply.OK {
		return reply, webmcp.NewClassifiedError(webmcp.ErrorInvocationFailed, "X video preparation was refused by the page adapter", map[string]any{"adapter_code": reply.Error.Code, "adapter_message": reply.Error.Message})
	}
	return reply, nil
}

type xVideoPreparation struct {
	command                *WebMCPOperationsCommand
	values                 *webmcpDirectFlags
	file, caption, account string
}

func (c *WebMCPOperationsCommand) xPrepareVideoCommand() *cobra.Command {
	p := &xVideoPreparation{command: c, values: newWebMCPDirectFlags()}
	cmd := &cobra.Command{
		Use: "x-prepare-video", Short: "Transfer a local MP4 and prepare an X video draft (never publishes)",
		Long: "Transfer one MP4 (up to 64 MiB) through the selected X WebMCP router. Requires --file, --text, and --account. Returns a one-use draft token after X finishes processing. Review the draft, then explicitly invoke x_publish_post to publish. Failure or timeout never authorizes automatic publishing; inspect any remaining composer before retrying.",
		Args: cobra.NoArgs, SilenceUsage: true,
		RunE: p.run,
	}
	registerWebMCPDirectBrowserFlags(cmd, &p.values.browser)
	registerWebMCPDirectCommandTimeoutFlag(cmd, p.values)
	p.values.commandTimeout = xVideoPreparationTimeout
	cmd.Flags().Lookup("command-timeout").DefValue = "5m0s"
	cmd.Flags().StringVar(&p.file, "file", "", "Local MP4 path (read-only, up to 64 MiB)")
	cmd.Flags().StringVar(&p.caption, "text", "", "Exact caption")
	cmd.Flags().StringVar(&p.account, "account", "", "Expected signed-in @handle")
	cmd.Flags().StringVar(&p.values.reason, "reason", "prepare user-requested X video draft", "User-facing reason")
	cmd.Flags().BoolVar(&p.values.json, "json", false, "Write one machine-readable JSON result")
	return cmd
}

func (p *xVideoPreparation) run(cmd *cobra.Command, _ []string) error {
	return p.command.executeDirect(cmd, p.values, "x-prepare-video", webmcp.ErrorInvocationFailed,
		func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
			return p.prepare(ctx, cmd, broker, browser)
		})
}

func (p *xVideoPreparation) input() ([]byte, string, error) {
	p.caption = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(p.caption, "\r\n", "\n"), "\r", "\n"))
	if !regexp.MustCompile(`^@[A-Za-z0-9_]{1,15}$`).MatchString(p.account) || p.caption == "" || len([]rune(p.caption)) > 280 {
		return nil, "", directInvalidInputError("provide --account @handle and --text of 1 through 280 characters", "/account")
	}
	data, hash, err := readXVideo(p.file)
	if err != nil {
		return nil, "", directInvalidInputError(err.Error(), "/file")
	}
	return data, hash, nil
}

func (p *xVideoPreparation) prepare(ctx context.Context, cmd *cobra.Command, broker webmcp.Broker, browser config.BrowserConfig) (outcome any, operationErr error) {
	data, hash, err := p.input()
	if err != nil {
		return nil, err
	}
	page, err := p.command.ensureDirectSelection(ctx, cmd, p.values, broker, browser)
	if err != nil {
		return nil, err
	}
	if !regexp.MustCompile(`^https://(www\.)?(x\.com|twitter\.com)(/|$)`).MatchString(page.URL) {
		return nil, directInvalidInputError("select an HTTPS X or Twitter page", "/origin")
	}
	focus, ok := broker.(webmcp.PageFocusLeaser)
	if !ok {
		return nil, fmt.Errorf("selected browser does not support bounded media focus")
	}
	release, err := focus.AcquirePageFocus(ctx)
	if release != nil {
		defer func() { operationErr = errors.Join(operationErr, restoreXVideoFocus(ctx, release)) }()
	}
	if err != nil {
		return nil, err
	}
	return p.transfer(ctx, cmd.ErrOrStderr(), broker, data, hash)
}

func restoreXVideoFocus(ctx context.Context, release func(context.Context) error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := release(cleanup); err != nil {
		return fmt.Errorf("restore media focus: %w", err)
	}
	return nil
}

func (p *xVideoPreparation) invoke(ctx context.Context, broker webmcp.Broker, name string, input any) (WebMCPDirectInvocation, xVideoReply, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return WebMCPDirectInvocation{}, xVideoReply{}, err
	}
	callValues := *p.values
	callValues.inputJSON = string(encoded)
	ref, body, err := resolveDirectInvocation([]string{name}, &callValues, broker, ctx)
	if err != nil {
		return WebMCPDirectInvocation{}, xVideoReply{}, err
	}
	result, err := broker.Invoke(ctx, webmcp.InvokeRequest{ToolRef: ref, Input: body, Reason: p.values.reason})
	if err != nil {
		return WebMCPDirectInvocation{}, xVideoReply{}, err
	}
	result, err = waitDirectInvocation(ctx, broker, result)
	if err != nil {
		return WebMCPDirectInvocation{}, xVideoReply{}, err
	}
	if result.ErrorCode != "" || directInvocationFailed(result.State) {
		return WebMCPDirectInvocation{}, xVideoReply{}, directInvocationResultError(result, ref)
	}
	out := WebMCPDirectInvocation{ToolRef: string(ref), Status: string(result.State), Output: result.Output}
	reply, err := decodeXVideoReply(result.Output)
	return out, reply, err
}

func (p *xVideoPreparation) transfer(ctx context.Context, recovery io.Writer, broker webmcp.Broker, data []byte, hash string) (any, error) {
	_, started, err := p.invoke(ctx, broker, "x_begin_video_upload", map[string]any{"filename": "video.mp4", "size": len(data), "sha256": hash, "account": p.account})
	if err != nil {
		return nil, err
	}
	token := started.Data.UploadToken
	if token == "" {
		return nil, fmt.Errorf("x adapter omitted upload token")
	}
	// Preserve recovery metadata without media bytes if a bounded transfer fails.
	if err := json.NewEncoder(recovery).Encode(map[string]any{"version": "webmcp.x-video-transfer.v1", "upload_token": token, "video_sha256": hash, "video_bytes": len(data)}); err != nil {
		return nil, err
	}
	if err := p.appendChunks(ctx, broker, token, data); err != nil {
		return nil, err
	}
	return p.waitPrepared(ctx, broker, token)
}

func (p *xVideoPreparation) appendChunks(ctx context.Context, broker webmcp.Broker, token string, data []byte) error {
	for offset := 0; offset < len(data); offset += xVideoChunkBytes {
		end := min(offset+xVideoChunkBytes, len(data))
		_, reply, err := p.invoke(ctx, broker, "x_append_video_chunk", map[string]any{"upload_token": token, "offset": offset, "data_base64": base64.StdEncoding.EncodeToString(data[offset:end])})
		if err != nil {
			return err
		}
		if reply.Data.ReceivedBytes != end {
			return fmt.Errorf("x adapter acknowledged an unexpected byte offset")
		}
	}
	return nil
}

func (p *xVideoPreparation) waitPrepared(ctx context.Context, broker webmcp.Broker, token string) (any, error) {
	for {
		out, reply, err := p.invoke(ctx, broker, "x_prepare_video_post", map[string]any{"upload_token": token, "text": p.caption})
		if err != nil {
			return nil, err
		}
		if !reply.Data.VideoProcessing {
			if reply.Data.DraftToken == "" {
				return nil, fmt.Errorf("x adapter omitted draft token")
			}
			return out, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (c *WebMCPOperationsCommand) loadDirectSelection() (WebMCPSelection, error) {
	store, err := c.selectionStore()
	if err != nil {
		return WebMCPSelection{}, err
	}
	return store.Load()
}

func (c *WebMCPOperationsCommand) saveDirectSelection(selection WebMCPSelection) error {
	store, err := c.selectionStore()
	if err != nil {
		return err
	}
	return store.Save(selection)
}

func (c *WebMCPOperationsCommand) selectionStore() (WebMCPSelectionStore, error) {
	if c != nil && c.SelectionStore != nil {
		return c.SelectionStore, nil
	}
	configDir := ""
	if c != nil && c.globalFlags != nil {
		configDir = c.globalFlags.ConfigDir()
	}
	store := NewFileWebMCPSelectionStore(configDir)
	if store.Path == "" {
		return nil, errors.New("WebMCP selection store is unavailable")
	}
	return store, nil
}
