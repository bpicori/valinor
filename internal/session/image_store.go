package session

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	agentcontext "valinor/internal/context"
	"valinor/internal/logger"
	"valinor/internal/media"
)

const maxHydratedImageBytes = 10 << 20

// HydrateImages loads workspace bytes for persisted image attachments. The
// transcript stores only paths; image data stays out of messages.json and is
// re-read for each run.
func HydrateImages(workspaceDir string, messages []agentcontext.Message) []agentcontext.Message {
	if len(messages) == 0 {
		return messages
	}
	if strings.TrimSpace(workspaceDir) == "" {
		logger.Warning("session image hydrate skipped", logger.BuildMetadata("reason", "empty workspace dir"))
		return messages
	}

	out := agentcontext.CloneMessages(messages)
	for i := range out {
		if len(out[i].Images) == 0 {
			continue
		}

		images := make([]media.Image, len(out[i].Images))
		copy(images, out[i].Images)
		for j := range images {
			if len(images[j].Data) > 0 {
				continue
			}

			path, ok := safeImagePath(images[j].Path)
			if !ok {
				logger.Warning("session image hydrate skipped",
					logger.BuildMetadata("path", images[j].Path, "reason", "invalid workspace path"))
				continue
			}

			data, err := os.ReadFile(filepath.Join(workspaceDir, filepath.FromSlash(path)))
			if err != nil {
				logger.Warning("session image hydrate failed",
					logger.BuildMetadata("path", path, "error", err))
				continue
			}
			if len(data) > maxHydratedImageBytes {
				logger.Warning("session image hydrate skipped",
					logger.BuildMetadata("path", path, "bytes", len(data), "reason", "image too large"))
				continue
			}

			images[j].Data = data
			if images[j].MimeType == "" {
				images[j].MimeType = strings.ToLower(strings.Split(http.DetectContentType(data), ";")[0])
			}
		}
		out[i].Images = images
	}
	return out
}

func safeImagePath(path string) (string, bool) {
	cleaned := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	if cleaned == "" || cleaned == "." {
		return "", false
	}
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/..") {
		return "", false
	}
	return cleaned, true
}
