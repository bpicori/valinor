// Package imagegen generates images through a configurable provider.
package imagegen

import "context"

// MaxReferences is the maximum number of reference images accepted by the
// configured provider.
const MaxReferences = 14

// Reference is an image supplied to guide generation or editing.
type Reference struct {
	Data     []byte
	MimeType string
}

// Params describes one image generation request.
type Params struct {
	// Prompt is the text description of the desired image.
	Prompt string
	// AspectRatio is an optional ratio such as "16:9".
	AspectRatio string
	// Size is an optional resolution tier (e.g. "2K") or explicit pixels
	// (e.g. "1024x1024").
	Size string
	// References are optional images to edit, restyle, or use for composition.
	References []Reference
}

// Provider generates images from text prompts.
type Provider interface {
	Name() string
	Generate(ctx context.Context, params Params) ([]byte, error)
}
