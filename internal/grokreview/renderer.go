package grokreview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const rendererSchema = "squad.review-renderer-equivalence.v1"
const maxPatchBytes = 8 << 20

// PatchRendererEvidence pins two complete streams, never a reconstructed old
// review input or a normalized digest. The old raw hash remains authoritative.
type PatchRendererEvidence struct {
	SchemaVersion       string         `json:"schema_version"`
	Identity            ReviewIdentity `json:"identity"`
	OriginalPatchPath   string         `json:"original_patch_path"`
	OriginalPatchSHA256 string         `json:"original_patch_sha256"`
	CurrentPatchPath    string         `json:"current_patch_path"`
	CurrentPatchSHA256  string         `json:"current_patch_sha256"`
}

type PatchRendererProvenance struct {
	Rule           string `json:"rule"`
	OriginalSHA256 string `json:"original_sha256"`
	CurrentSHA256  string `json:"current_sha256"`
	EvidenceSHA256 string `json:"evidence_sha256"`
}

func (p *ProspectiveReadmission) verifyRenderer(current []byte) (*PatchRendererProvenance, error) {
	e := p.RendererEquivalence
	if e == nil {
		return nil, fmt.Errorf("renderer equivalence evidence missing")
	}
	if e.SchemaVersion != rendererSchema || e.Identity.BundleSHA256 != "" || tupleKey(e.Identity) != tupleKey(p.Original.Attempt.Identity) || e.OriginalPatchSHA256 != p.DiffSHA256 || !contentHashValid(e.CurrentPatchSHA256) {
		return nil, fmt.Errorf("renderer evidence tuple or raw hashes mismatched")
	}
	old, err := readPatch(e.OriginalPatchPath, e.OriginalPatchSHA256)
	if err != nil {
		return nil, fmt.Errorf("authentic complete original patch unavailable or changed: %w", err)
	}
	fresh, err := readPatch(e.CurrentPatchPath, e.CurrentPatchSHA256)
	if err != nil {
		return nil, fmt.Errorf("complete current patch unavailable or changed: %w", err)
	}
	if current != nil && !bytes.Equal(fresh, current) {
		return nil, fmt.Errorf("current renderer stream differs from frozen gateway patch")
	}
	if err := verifyPatchEquivalence(old, fresh); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return &PatchRendererProvenance{Rule: rendererSchema, OriginalSHA256: e.OriginalPatchSHA256, CurrentSHA256: e.CurrentPatchSHA256, EvidenceSHA256: receiptHash(raw)}, nil
}

func readPatch(path, hash string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("patch path must be absolute")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() == 0 || before.Size() > maxPatchBytes {
		return nil, fmt.Errorf("patch must be a nonempty bounded regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, fmt.Errorf("patch file identity changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxPatchBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxPatchBytes || receiptHash(raw) != hash {
		return nil, fmt.Errorf("patch raw SHA mismatch or size limit exceeded")
	}
	return raw, nil
}

var hunkHeader = regexp.MustCompile(`^(@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@)(?: [^\r\n]*)?$`)
var patchMetadata = regexp.MustCompile(`^(index [0-9a-f]+\.\.[0-9a-f]+( [0-7]{6})?|(old mode|new mode|deleted file mode|new file mode) [0-7]{6}|(dis)?similarity index (100|[0-9]{1,2})%|(rename|copy) (from|to) .+)$`)
var binaryHeader = regexp.MustCompile(`^(literal|delta) ([0-9]+)$`)
var binaryLine = regexp.MustCompile("^[A-Za-z][0-9A-Za-z!#$%&()*+;<=>?@^_`{|}~-]+$")

// Compare the complete parsed streams directly. Only text after a valid closing
// @@ is omitted from comparison; no source, range, metadata or newline is lost.
func verifyPatchEquivalence(old, current []byte) error {
	a, err := parseRendererPatch(old)
	if err != nil {
		return fmt.Errorf("original patch invalid: %w", err)
	}
	b, err := parseRendererPatch(current)
	if err != nil {
		return fmt.Errorf("current patch invalid: %w", err)
	}
	if !bytes.Equal(a, b) {
		return fmt.Errorf("patches differ outside descriptive hunk context")
	}
	return nil
}

func parseRendererPatch(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > maxPatchBytes || raw[len(raw)-1] != '\n' || bytes.IndexByte(raw, 0) >= 0 {
		return nil, fmt.Errorf("empty, oversized, truncated or NUL patch")
	}
	lines := strings.Split(string(raw[:len(raw)-1]), "\n")
	var out bytes.Buffer
	files := 0
	seenFiles := map[string]bool{}
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "diff --git ") {
			return nil, fmt.Errorf("expected complete Git file header")
		}
		if seenFiles[lines[i]] {
			return nil, fmt.Errorf("duplicate file patch")
		}
		seenFiles[lines[i]] = true
		files++
		out.WriteString(lines[i] + "\n")
		i++
		metadata, headers, hunks := 0, 0, 0
		fields := map[string]bool{}
		oldEnd, newEnd := 0, 0
		for i < len(lines) && !strings.HasPrefix(lines[i], "diff --git ") {
			line := lines[i]
			if strings.HasPrefix(line, "@@") {
				m := hunkHeader.FindStringSubmatch(line)
				if m == nil || headers != 2 {
					return nil, fmt.Errorf("invalid unified hunk header")
				}
				oldStart, oldCount, err := hunkRange(m[2], m[3])
				if err != nil {
					return nil, err
				}
				newStart, newCount, err := hunkRange(m[4], m[5])
				if err != nil {
					return nil, err
				}
				if oldCount == 0 && newCount == 0 {
					return nil, fmt.Errorf("empty hunk")
				}
				if hunks > 0 && (oldStart < oldEnd || newStart < newEnd) {
					return nil, fmt.Errorf("overlapping or reordered hunk ranges")
				}
				oldEnd, newEnd = oldStart+oldCount, newStart+newCount
				hunks++
				out.WriteString(m[1] + "\n")
				i++
				oldLeft, newLeft := oldCount, newCount
				previous, marked := false, false
				for i < len(lines) {
					body := lines[i]
					if body == "\\ No newline at end of file" {
						if !previous || marked {
							return nil, fmt.Errorf("orphan or duplicate newline marker")
						}
						marked = true
					} else {
						if oldLeft == 0 && newLeft == 0 {
							break
						}
						if len(body) == 0 {
							return nil, fmt.Errorf("missing hunk source prefix")
						}
						switch body[0] {
						case ' ':
							oldLeft--
							newLeft--
						case '-':
							oldLeft--
						case '+':
							newLeft--
						default:
							return nil, fmt.Errorf("truncated hunk")
						}
						if oldLeft < 0 || newLeft < 0 {
							return nil, fmt.Errorf("hunk count mismatch")
						}
						previous = true
						marked = false
					}
					out.WriteString(body + "\n")
					i++
				}
				if oldLeft != 0 || newLeft != 0 {
					return nil, fmt.Errorf("truncated hunk source")
				}
				continue
			}
			if line == "GIT binary patch" {
				if headers != 0 || hunks != 0 {
					return nil, fmt.Errorf("ambiguous binary/text patch")
				}
				out.WriteString(line + "\n")
				i++
				for block := 0; block < 2; block++ {
					if i >= len(lines) || !binaryHeader.MatchString(lines[i]) {
						return nil, fmt.Errorf("missing complete binary block")
					}
					size, err := strconv.Atoi(binaryHeader.FindStringSubmatch(lines[i])[2])
					if err != nil || size > maxPatchBytes {
						return nil, fmt.Errorf("binary block exceeds validation bound")
					}
					out.WriteString(lines[i] + "\n")
					i++
					payload := 0
					for i < len(lines) && lines[i] != "" {
						if !binaryLine.MatchString(lines[i]) {
							return nil, fmt.Errorf("invalid binary payload")
						}
						out.WriteString(lines[i] + "\n")
						i++
						payload++
					}
					if payload == 0 || i >= len(lines) {
						return nil, fmt.Errorf("truncated binary block")
					}
					out.WriteString("\n")
					i++
				}
				// Git diff emits an additional separator after the second block.
				for i < len(lines) && lines[i] == "" {
					out.WriteString("\n")
					i++
				}
				if i < len(lines) && !strings.HasPrefix(lines[i], "diff --git ") {
					return nil, fmt.Errorf("trailing binary data")
				}
				hunks++
				continue
			}
			if strings.HasPrefix(line, "--- ") && headers == 0 && hunks == 0 {
				headers = 1
			} else if strings.HasPrefix(line, "+++ ") && headers == 1 && hunks == 0 {
				headers = 2
			} else if patchMetadata.MatchString(line) && headers == 0 && hunks == 0 {
				key := metadataKey(line)
				if fields[key] {
					return nil, fmt.Errorf("duplicate patch metadata")
				}
				fields[key] = true
				metadata++
			} else {
				return nil, fmt.Errorf("unsupported or misplaced patch metadata/source")
			}
			out.WriteString(line + "\n")
			i++
		}
		if fields["old mode"] != fields["new mode"] || fields["rename from"] != fields["rename to"] || fields["copy from"] != fields["copy to"] || (fields["rename from"] && fields["copy from"]) || (fields["new file mode"] && fields["deleted file mode"]) || ((fields["new file mode"] || fields["deleted file mode"]) && (fields["old mode"] || fields["rename from"] || fields["copy from"])) {
			return nil, fmt.Errorf("incomplete or conflicting file metadata")
		}
		if (fields["similarity index"] || fields["dissimilarity index"]) && !fields["rename from"] && !fields["copy from"] {
			return nil, fmt.Errorf("orphan similarity metadata")
		}
		if headers == 0 && hunks == 0 && !fields["old mode"] && !fields["rename from"] && !fields["copy from"] {
			return nil, fmt.Errorf("metadata has no complete change")
		}
		if (headers != 0 && hunks == 0) || (headers == 0 && hunks == 0 && metadata == 0) {
			return nil, fmt.Errorf("incomplete file patch")
		}
	}
	if files == 0 {
		return nil, fmt.Errorf("missing files")
	}
	// Git's parse-only mode validates file identities, metadata and binary
	// encoding without applying the patch or consulting a working repository.
	if err := validateGitPatch(raw); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func metadataKey(line string) string {
	if strings.HasPrefix(line, "new file mode ") {
		return "new file mode"
	}
	if strings.HasPrefix(line, "deleted file mode ") {
		return "deleted file mode"
	}
	if strings.HasPrefix(line, "index ") {
		return "index"
	}
	parts := strings.SplitN(line, " ", 3)
	return parts[0] + " " + parts[1]
}

func hunkRange(start, count string) (int, int, error) {
	s, err := strconv.Atoi(start)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid hunk range")
	}
	n := 1
	if count != "" {
		n, err = strconv.Atoi(count)
	}
	if err != nil || n > maxPatchBytes || s > int(^uint(0)>>1)-n || (s == 0 && n != 0) || (n == 0 && count == "") {
		return 0, 0, fmt.Errorf("invalid hunk count/range")
	}
	return s, n, nil
}

func validateGitPatch(raw []byte) error {
	dir, err := os.MkdirTemp("", "squad-patch-parse-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "apply", "--numstat", "--binary", "--whitespace=nowarn", "-")
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_COUNT=0", "GIT_OPTIONAL_LOCKS=0")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git parse-only full-patch validation failed: %w", err)
	}
	return nil
}
