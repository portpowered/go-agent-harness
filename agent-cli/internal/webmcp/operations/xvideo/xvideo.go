// Package xvideo prepares an X video draft through the selected X WebMCP
// router: it transfers one bounded local MP4 in acknowledged chunks under a
// leased page focus and waits for X to finish processing. It never
// publishes; the caller reviews the draft and publishes explicitly.
package xvideo

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

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/operations"
)

const (
	// MaxBytes bounds the transferred video.
	MaxBytes = 64 * 1024 * 1024
	// ChunkBytes is the size of one acknowledged transfer chunk.
	ChunkBytes = 32 * 1024
	// PreparationTimeout is the default end-to-end bound for one preparation.
	PreparationTimeout = 5 * time.Minute

	minBytes         = 12
	maxCaptionRunes  = 280
	focusRestoreTime = 2 * time.Second
	pollInterval     = time.Second

	toolBeginUpload  = "x_begin_video_upload"
	toolAppendChunk  = "x_append_video_chunk"
	toolPreparePost  = "x_prepare_video_post"
	keyUploadToken   = "upload_token"
	transferVersion  = "webmcp.x-video-transfer.v1"
	uploadedFilename = "video.mp4"
)

// validAccount reports whether account is an @handle.
func validAccount(account string) bool {
	return regexp.MustCompile(`^@[A-Za-z0-9_]{1,15}$`).MatchString(account)
}

// xPage reports whether rawURL is an HTTPS X or Twitter page.
func xPage(rawURL string) bool {
	return regexp.MustCompile(`^https://(www\.)?(x\.com|twitter\.com)(/|$)`).MatchString(rawURL)
}

// ReadVideo reads one bounded snapshot. The hash and every transmitted byte
// describe the same snapshot even if the caller replaces the source file
// during transfer.
func ReadVideo(path string) (data []byte, hash string, readErr error) {
	if !strings.EqualFold(filepath.Ext(path), ".mp4") {
		return nil, "", errors.New("video must be an MP4 file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open video: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			readErr = errors.Join(readErr, fmt.Errorf("close video: %w", err))
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Size() < minBytes || info.Size() > MaxBytes {
		return nil, "", errors.New("video must be a regular file between 12 bytes and 64 MiB")
	}
	data, err = io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) < minBytes || len(data) > MaxBytes || string(data[4:8]) != "ftyp" {
		return nil, "", errors.New("invalid or oversized MP4; expected ftyp header")
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

// Reply is the X page adapter's response envelope.
type Reply struct {
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

// DecodeReply decodes an adapter response and classifies a refusal.
func DecodeReply(output json.RawMessage) (Reply, error) {
	var reply Reply
	if err := json.Unmarshal(output, &reply); err != nil {
		return reply, fmt.Errorf("invalid X adapter response: %w", err)
	}
	if !reply.OK {
		return reply, webmcp.NewClassifiedError(webmcp.ErrorInvocationFailed, "X video preparation was refused by the page adapter", map[string]any{"adapter_code": reply.Error.Code, "adapter_message": reply.Error.Message})
	}
	return reply, nil
}

// Request is one preparation. Recovery receives the transfer's recovery
// metadata (never media bytes) before any chunk is sent.
type Request struct {
	Selector operations.Selector
	File     string
	Caption  string
	Account  string
	Reason   string
	Recovery io.Writer
}

// Prepare validates the input, selects an HTTPS X page, leases page focus,
// transfers the video, and returns the prepared draft's final invocation.
// Focus is restored on every path, even after cancellation.
func Prepare(ctx context.Context, broker webmcp.Broker, request Request) (result operations.Invocation, operationErr error) {
	request.Caption = normalizeCaption(request.Caption)
	data, hash, err := request.input()
	if err != nil {
		return operations.Invocation{}, err
	}
	page, err := operations.EnsureSelection(ctx, broker, request.Selector)
	if err != nil {
		return operations.Invocation{}, err
	}
	if !xPage(page.URL) {
		return operations.Invocation{}, direct.InvalidInputError("select an HTTPS X or Twitter page", "/origin")
	}
	focus, ok := broker.(webmcp.PageFocusLeaser)
	if !ok {
		return operations.Invocation{}, errors.New("selected browser does not support bounded media focus")
	}
	release, err := focus.AcquirePageFocus(ctx)
	if release != nil {
		defer func() { operationErr = errors.Join(operationErr, restoreFocus(ctx, release)) }()
	}
	if err != nil {
		return operations.Invocation{}, err
	}
	session := transfer{broker: broker, request: request}
	return session.run(ctx, data, hash)
}

func normalizeCaption(caption string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(caption, "\r\n", "\n"), "\r", "\n"))
}

func (r Request) input() ([]byte, string, error) {
	if !validAccount(r.Account) || r.Caption == "" || len([]rune(r.Caption)) > maxCaptionRunes {
		return nil, "", direct.InvalidInputError("provide --account @handle and --text of 1 through 280 characters", "/account")
	}
	data, hash, err := ReadVideo(r.File)
	if err != nil {
		return nil, "", direct.InvalidInputError(err.Error(), "/file")
	}
	return data, hash, nil
}

// restoreFocus releases the focus lease under a fresh bounded context that
// keeps ctx's values but not its cancellation.
func restoreFocus(ctx context.Context, release func(context.Context) error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), focusRestoreTime)
	defer cancel()
	if err := release(cleanup); err != nil {
		return fmt.Errorf("restore media focus: %w", err)
	}
	return nil
}

// transfer is one upload session through the X adapter tools.
type transfer struct {
	broker  webmcp.Broker
	request Request
}

func (t transfer) run(ctx context.Context, data []byte, hash string) (operations.Invocation, error) {
	_, started, err := t.invoke(ctx, toolBeginUpload, map[string]any{"filename": uploadedFilename, "size": len(data), "sha256": hash, "account": t.request.Account})
	if err != nil {
		return operations.Invocation{}, err
	}
	token := started.Data.UploadToken
	if token == "" {
		return operations.Invocation{}, errors.New("x adapter omitted upload token")
	}
	if err := t.writeRecovery(token, hash, len(data)); err != nil {
		return operations.Invocation{}, err
	}
	if err := t.appendChunks(ctx, token, data); err != nil {
		return operations.Invocation{}, err
	}
	return t.waitPrepared(ctx, token)
}

// writeRecovery preserves recovery metadata without media bytes in case a
// bounded transfer fails.
func (t transfer) writeRecovery(token, hash string, size int) error {
	if t.request.Recovery == nil {
		return errors.New("x video recovery writer is unavailable")
	}
	return json.NewEncoder(t.request.Recovery).Encode(map[string]any{"version": transferVersion, keyUploadToken: token, "video_sha256": hash, "video_bytes": size})
}

func (t transfer) invoke(ctx context.Context, name string, input any) (operations.Invocation, Reply, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return operations.Invocation{}, Reply{}, err
	}
	ref, body, err := operations.ResolveInvocation(ctx, t.broker, operations.InvocationInput{Args: []string{name}, InputJSON: string(encoded)})
	if err != nil {
		return operations.Invocation{}, Reply{}, err
	}
	result, err := t.broker.Invoke(ctx, webmcp.InvokeRequest{ToolRef: ref, Input: body, Reason: t.request.Reason})
	if err != nil {
		return operations.Invocation{}, Reply{}, err
	}
	result, err = operations.WaitInvocation(ctx, t.broker, result)
	if err != nil {
		return operations.Invocation{}, Reply{}, err
	}
	if result.ErrorCode != "" || result.State.Failed() {
		return operations.Invocation{}, Reply{}, operations.InvocationResultError(result, ref)
	}
	out := operations.Invocation{ToolRef: string(ref), Status: string(result.State), Output: result.Output}
	reply, err := DecodeReply(result.Output)
	return out, reply, err
}

func (t transfer) appendChunks(ctx context.Context, token string, data []byte) error {
	for offset := 0; offset < len(data); offset += ChunkBytes {
		end := min(offset+ChunkBytes, len(data))
		_, reply, err := t.invoke(ctx, toolAppendChunk, map[string]any{keyUploadToken: token, "offset": offset, "data_base64": base64.StdEncoding.EncodeToString(data[offset:end])})
		if err != nil {
			return err
		}
		if reply.Data.ReceivedBytes != end {
			return errors.New("x adapter acknowledged an unexpected byte offset")
		}
	}
	return nil
}

func (t transfer) waitPrepared(ctx context.Context, token string) (operations.Invocation, error) {
	for {
		out, reply, err := t.invoke(ctx, toolPreparePost, map[string]any{keyUploadToken: token, "text": t.request.Caption})
		if err != nil {
			return operations.Invocation{}, err
		}
		if !reply.Data.VideoProcessing {
			if reply.Data.DraftToken == "" {
				return operations.Invocation{}, errors.New("x adapter omitted draft token")
			}
			return out, nil
		}
		select {
		case <-ctx.Done():
			return operations.Invocation{}, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
