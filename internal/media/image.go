// Package media holds non-text payloads that travel with conversation messages.
package media

import "encoding/base64"

// Image is an image attachment. Data is runtime-only; only the path and MIME
// type are persisted, and the bytes are re-read from the workspace on resume.
type Image struct {
	Path     string `json:"path,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Data     []byte `json:"-"`
}

// Base64 returns the image bytes encoded for provider APIs.
func (i Image) Base64() string {
	return base64.StdEncoding.EncodeToString(i.Data)
}

// DataURI returns a data URI suitable for OpenAI image content parts.
func (i Image) DataURI() string {
	mimeType := i.MimeType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return "data:" + mimeType + ";base64," + i.Base64()
}
