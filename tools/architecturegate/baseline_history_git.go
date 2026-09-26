package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// gitBlobType is the git object type of a file.
const gitBlobType = "blob"

// gitTreeBlob is one file recorded in a commit's tree.
type gitTreeBlob struct {
	Path   string
	Object string
	Data   []byte
}

// readGitTreeJSON returns every .json blob under relative at commit, sorted
// by path, with its contents. It costs two git processes regardless of how
// many fragments the tree holds: one ls-tree for the listing and one
// cat-file --batch stream for the contents. The baseline directory holds
// hundreds of fragments, and a git show per fragment dominated the gate.
func readGitTreeJSON(ctx context.Context, gitBinary, repoRoot, commit, relative string) ([]gitTreeBlob, error) {
	listing, err := gitOutput(ctx, gitBinary, repoRoot, "ls-tree", "-r", "-z", commit, "--", relative)
	if err != nil {
		return nil, err
	}
	blobs, err := parseGitTreeJSON(listing)
	if err != nil || len(blobs) == 0 {
		return blobs, err
	}
	if err := readGitBlobs(ctx, gitBinary, repoRoot, blobs); err != nil {
		return nil, err
	}
	return blobs, nil
}

// parseGitTreeJSON decodes `git ls-tree -r -z` records of the form
// "<mode> SP <type> SP <object> TAB <path> NUL" and keeps .json paths.
func parseGitTreeJSON(listing []byte) ([]gitTreeBlob, error) {
	blobs := make([]gitTreeBlob, 0)
	for _, record := range strings.Split(string(listing), "\x00") {
		if record == "" {
			continue
		}
		meta, path, ok := strings.Cut(record, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("git ls-tree returned malformed record %q", record)
		}
		if !strings.HasSuffix(path, ".json") {
			continue
		}
		if fields[1] != gitBlobType {
			return nil, fmt.Errorf("git ls-tree entry %q is a %s, expected a blob", path, fields[1])
		}
		blobs = append(blobs, gitTreeBlob{Path: path, Object: fields[2]})
	}
	sort.Slice(blobs, func(i, j int) bool { return blobs[i].Path < blobs[j].Path })
	return blobs, nil
}

// readGitBlobs fills Data for each blob through one `git cat-file --batch`
// process. Object names go in on stdin; each response is
// "<object> SP <type> SP <size> LF <contents> LF", in request order.
func readGitBlobs(ctx context.Context, gitBinary, repoRoot string, blobs []gitTreeBlob) error {
	if strings.TrimSpace(gitBinary) == "" {
		gitBinary = "git"
	}
	var request strings.Builder
	for _, blob := range blobs {
		request.WriteString(blob.Object)
		request.WriteByte('\n')
	}
	command := exec.CommandContext(ctx, gitBinary, "cat-file", "--batch")
	command.Dir = repoRoot
	command.Stdin = strings.NewReader(request.String())
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("git cat-file --batch: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	reader := bufio.NewReader(bytes.NewReader(output))
	for index := range blobs {
		data, err := readGitBatchObject(reader, blobs[index])
		if err != nil {
			return err
		}
		blobs[index].Data = data
	}
	return nil
}

func readGitBatchObject(reader *bufio.Reader, blob gitTreeBlob) ([]byte, error) {
	header, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("git cat-file --batch: read header for %q: %w", blob.Path, err)
	}
	fields := strings.Fields(header)
	if len(fields) != 3 || fields[0] != blob.Object || fields[1] != gitBlobType {
		return nil, fmt.Errorf("git cat-file --batch: unexpected header %q for %q", strings.TrimSpace(header), blob.Path)
	}
	size, err := strconv.Atoi(fields[2])
	if err != nil || size < 0 {
		return nil, fmt.Errorf("git cat-file --batch: invalid size in header %q for %q", strings.TrimSpace(header), blob.Path)
	}
	data := make([]byte, size+1)
	if _, err := io.ReadFull(reader, data); err != nil {
		return nil, fmt.Errorf("git cat-file --batch: read %q: %w", blob.Path, err)
	}
	if data[size] != '\n' {
		return nil, fmt.Errorf("git cat-file --batch: missing object terminator for %q", blob.Path)
	}
	return data[:size], nil
}

func loadHistoricalBaseline(ctx context.Context, gitBinary, repoRoot, commit, relative string) (Baseline, bool, error) {
	kind, err := gitOutput(ctx, gitBinary, repoRoot, "cat-file", "-t", commit+":"+relative)
	if err == nil {
		switch strings.TrimSpace(string(kind)) {
		case gitBlobType:
			data, showErr := gitOutput(ctx, gitBinary, repoRoot, "show", commit+":"+relative)
			if showErr != nil {
				return Baseline{}, true, showErr
			}
			baseline, decodeErr := decodeHistoricalBaseline(data)
			return baseline, true, decodeErr
		case "tree":
			baseline, loadErr := loadHistoricalBaselineDirectory(ctx, gitBinary, repoRoot, commit, relative)
			return baseline, true, loadErr
		default:
			return Baseline{}, true, fmt.Errorf("merge-base baseline %q is a %s, expected a file or directory", relative, strings.TrimSpace(string(kind)))
		}
	}
	legacy := filepath.ToSlash(filepath.Join(filepath.Dir(relative), "architecture-size-baseline.json"))
	if legacy != relative {
		if data, legacyErr := gitOutput(ctx, gitBinary, repoRoot, "show", commit+":"+legacy); legacyErr == nil {
			baseline, decodeErr := decodeHistoricalBaseline(data)
			return baseline, true, decodeErr
		}
	}
	return Baseline{}, false, nil
}

func loadHistoricalBaselineDirectory(ctx context.Context, gitBinary, repoRoot, commit, relative string) (Baseline, error) {
	blobs, err := readGitTreeJSON(ctx, gitBinary, repoRoot, commit, relative)
	if err != nil {
		return Baseline{}, err
	}
	if len(blobs) == 0 {
		return Baseline{}, fmt.Errorf("merge-base baseline directory %q contains no JSON fragments", relative)
	}
	combined := Baseline{Version: baselineVersion}
	for _, blob := range blobs {
		fragment, err := decodeHistoricalBaseline(blob.Data)
		if err != nil {
			return Baseline{}, fmt.Errorf("fragment %q: %w", blob.Path, err)
		}
		if err := appendBaselineFragment(&combined, fragment, blob.Path); err != nil {
			return Baseline{}, err
		}
	}
	return validateCombinedBaseline(combined, "merge-base "+relative)
}

func decodeHistoricalBaseline(data []byte) (Baseline, error) {
	var previous Baseline
	if err := json.Unmarshal(data, &previous); err != nil {
		return Baseline{}, fmt.Errorf("merge-base baseline is invalid: %w", err)
	}
	if previous.Version != baselineVersion {
		return Baseline{}, fmt.Errorf("merge-base baseline has version %d; expected %d", previous.Version, baselineVersion)
	}
	if err := validateBaseline(previous); err != nil {
		return Baseline{}, fmt.Errorf("merge-base baseline is invalid: %w", err)
	}
	return previous, nil
}

func gitOutput(ctx context.Context, gitBinary, dir string, args ...string) ([]byte, error) {
	if strings.TrimSpace(gitBinary) == "" {
		gitBinary = "git"
	}
	command := exec.CommandContext(ctx, gitBinary, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}
