package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentcontext "valinor/internal/context"
	"valinor/internal/logger"
)

const (
	blobThresholdBytes = 8192
	blobPreviewBytes   = 2048
	blobTruncationNote = "\n\n[... truncated; full content stored on disk]"
)

func externalizeToolContent(sessionDir, content string) (preview, ref string, err error) {
	if len(content) <= blobThresholdBytes {
		return content, "", nil
	}

	sum := sha256.Sum256([]byte(content))
	ref = hex.EncodeToString(sum[:])

	blobDir := filepath.Join(sessionDir, "blobs")
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		return "", "", fmt.Errorf("create blobs dir: %w", err)
	}

	blobPath := filepath.Join(blobDir, ref+".txt")
	if _, err := os.Stat(blobPath); os.IsNotExist(err) {
		if err := os.WriteFile(blobPath, []byte(content), 0644); err != nil {
			return "", "", fmt.Errorf("write blob: %w", err)
		}
	}

	preview = truncateForPreview(content, blobPreviewBytes)
	return preview, ref, nil
}

func truncateForPreview(content string, maxBytes int) string {
	if len(content) <= maxBytes {
		return content
	}
	preview := content[:maxBytes]
	if idx := strings.LastIndex(preview, "\n"); idx > maxBytes/2 {
		preview = preview[:idx]
	}
	return preview + blobTruncationNote
}

// HydrateMessages loads full tool content for messages with blob references.
func HydrateMessages(sessionDir string, messages []agentcontext.Message) ([]agentcontext.Message, error) {
	if len(messages) == 0 {
		return messages, nil
	}

	out := make([]agentcontext.Message, len(messages))
	copy(out, messages)

	for i := range out {
		if out[i].BlobRef == "" {
			continue
		}

		blobPath := filepath.Join(sessionDir, "blobs", out[i].BlobRef+".txt")
		data, err := os.ReadFile(blobPath)
		if err != nil {
			logger.Warning("session blob hydrate failed",
				logger.BuildMetadata("blob_ref", out[i].BlobRef, "path", blobPath, "error", err))
			continue
		}
		out[i].Content = string(data)
	}

	return out, nil
}

func externalizeMessages(sessionDir string, messages []agentcontext.Message) ([]agentcontext.Message, error) {
	out := agentcontext.CloneMessages(messages)
	for i := range out {
		if out[i].Role != agentcontext.RoleTool || out[i].BlobRef != "" {
			continue
		}
		if len(out[i].Content) <= blobThresholdBytes {
			continue
		}

		preview, ref, err := externalizeToolContent(sessionDir, out[i].Content)
		if err != nil {
			return nil, err
		}
		out[i].Content = preview
		out[i].BlobRef = ref
	}
	return out, nil
}