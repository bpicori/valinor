package dream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	agentcontext "valinor/internal/context"
	"valinor/internal/logger"
	"valinor/internal/session"
)

const skippedTooLarge = "skipped:too-large"

// candidate is one changed session excerpt ready for the digest.
type candidate struct {
	summary session.TranscriptSummary
	digest  string
	hash    string
}

// selection is the result of scanning persisted sessions.
type selection struct {
	candidates []candidate // newest first
	refreshed  map[string]time.Time
	existing   map[string]struct{}
}

// selectCandidates returns changed session excerpts and refreshes bookkeeping.
// It prunes marks for deleted sessions via the returned existing set.
func (r *Runtime) selectCandidates(ctx context.Context, state *state, now time.Time) (selection, error) {
	transcripts, err := r.opts.Sessions.ListTranscripts(ctx)
	if err != nil {
		return selection{existing: map[string]struct{}{}}, fmt.Errorf("list transcripts: %w", err)
	}

	cutoff := now.AddDate(0, 0, -lookbackDays)
	result := selection{
		existing:  make(map[string]struct{}, len(transcripts)),
		refreshed: make(map[string]time.Time),
	}

	for _, transcript := range transcripts {
		meta := transcript.Meta
		result.existing[meta.ID] = struct{}{}
		if !samePath(meta.WorkspaceDir, r.workspaceDir) {
			continue
		}
		if transcript.UpdatedAt.Before(cutoff) {
			continue
		}

		mark, marked := state.Sessions[meta.ID]
		if marked && mark.UpdatedAt.Equal(transcript.UpdatedAt) {
			continue
		}
		if transcript.Bytes > maxTranscriptBytes {
			logger.Warning("dreaming skipped oversized transcript",
				logger.BuildMetadata("session_id", meta.ID, "bytes", transcript.Bytes))
			state.Sessions[meta.ID] = sessionMark{DigestHash: skippedTooLarge, UpdatedAt: transcript.UpdatedAt}
			continue
		}

		messages, err := r.opts.Sessions.LoadTranscript(ctx, meta.ID)
		if err != nil {
			logger.Warning("dreaming transcript read failed",
				logger.BuildMetadata("session_id", meta.ID, "error", err))
			continue
		}

		digest := buildSessionDigest(meta, transcript.UpdatedAt, messages)
		if digest == "" {
			state.Sessions[meta.ID] = sessionMark{DigestHash: "", UpdatedAt: transcript.UpdatedAt}
			continue
		}
		hash := digestHash(digest)
		if marked && mark.DigestHash == hash {
			result.refreshed[meta.ID] = transcript.UpdatedAt
			continue
		}
		result.candidates = append(result.candidates, candidate{
			summary: transcript,
			digest:  digest,
			hash:    hash,
		})
	}

	sort.Slice(result.candidates, func(i, j int) bool {
		return result.candidates[i].summary.UpdatedAt.After(result.candidates[j].summary.UpdatedAt)
	})
	if len(result.candidates) > maxSessions {
		result.candidates = result.candidates[:maxSessions]
	}
	return result, nil
}

// buildSessionDigest renders one session as chronological user/assistant excerpts.
// The newest messages that fit the budget are kept.
func buildSessionDigest(meta session.Metadata, updated time.Time, messages []agentcontext.Message) string {
	budget := maxCharsPerSession
	entries := make([]string, 0, maxTurnsPerSession)

	for i := len(messages) - 1; i >= 0 && len(entries) < maxTurnsPerSession; i-- {
		message := messages[i]
		if message.Role != agentcontext.RoleUser && message.Role != agentcontext.RoleAssistant {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		content = truncate(content, maxMessageChars, " …")

		role := "User"
		if message.Role == agentcontext.RoleAssistant {
			role = "Assistant"
		}
		entry := role + ": " + content
		if len(entry)+2 > budget {
			break
		}
		entries = append(entries, entry)
		budget -= len(entry) + 2
	}
	if len(entries) == 0 {
		return ""
	}
	reverse(entries)

	title := truncate(strings.TrimSpace(meta.Title), 80, " …")
	if title == "" {
		title = "(untitled)"
	}
	id := meta.ID
	if len(id) > 8 {
		id = id[:8]
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "### Session %s — %s (%s)\n", id, title, updated.Format("2006-01-02 15:04"))
	sb.WriteString(strings.Join(entries, "\n\n"))
	sb.WriteString("\n")
	return sb.String()
}

// assembleDigest selects candidates until the byte cap and returns them in
// chronological order. Only returned candidates receive watermarks.
func assembleDigest(candidates []candidate, maxChars int) (string, []candidate) {
	included := make([]candidate, 0, len(candidates))
	total := 0
	for _, item := range candidates {
		if len(included) > 0 && total+len(item.digest) > maxChars {
			break
		}
		included = append(included, item)
		total += len(item.digest)
		if total >= maxChars {
			break
		}
	}
	if len(included) == 0 {
		return "", nil
	}
	reverseCandidates(included)

	var sb strings.Builder
	for _, item := range included {
		sb.WriteString(item.digest)
		sb.WriteString("\n")
	}
	return sb.String(), included
}

func samePath(a, b string) bool {
	if strings.TrimSpace(a) == "" {
		return true
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func digestHash(digest string) string {
	sum := sha256.Sum256([]byte(digest))
	return hex.EncodeToString(sum[:])
}

func reverse(values []string) {
	for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
		values[i], values[j] = values[j], values[i]
	}
}

func reverseCandidates(values []candidate) {
	for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
		values[i], values[j] = values[j], values[i]
	}
}

func truncate(value string, max int, suffix string) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + suffix
}
