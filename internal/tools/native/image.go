package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"strings"

	"golang.org/x/image/webp"

	"marshal/internal/imagepayload"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

// NoticeImageAdjusted marks an image the store changed on the way in: it was
// downscaled to fit the edge limit, re-encoded into another format, or both.
// The ref describes what was stored, so the notice is how the caller learns
// the bytes it sees are not byte-for-byte the file it named.
const NoticeImageAdjusted = "image_adjusted"

type imageViewArgs struct {
	Path    string `json:"path"`
	Ref     string `json:"ref"`
	MaxEdge int    `json:"max_edge"`
	Format  string `json:"format"`
	Quality int    `json:"quality"`
	Label   string `json:"label"`
}

func (t *toolSet) imageViewTool() registry.Tool {
	tool := registry.Tool{
		Name: "image.view",
		Description: "View an image (a screenshot, a diagram, a photo) so it becomes part of the conversation. " +
			"Give exactly one of path or ref. PNG, JPEG, GIF and WebP sources are accepted; the image is stored and sent as PNG or JPEG, " +
			"downscaled to the long-edge limit, and the result carries the hash that later turns can refer to. " +
			"Use it whenever the answer depends on what a picture shows, instead of guessing from a filename.",
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":` +
			t.pathDescription("image file path relative to the workspace; absolute paths that resolve inside an allowed root are accepted") +
			`},"ref":{"type":"string","description":"hash of an image already stored by an earlier image.view"}` +
			`,"max_edge":{"type":"integer","description":"longest edge in pixels; the image is downscaled to fit (defaults to the configured limit)"}` +
			`,"format":{"type":"string","enum":["png","jpeg"],"description":"stored format; defaults to the source format, or JPEG when the image must be re-encoded"}` +
			`,"quality":{"type":"integer","minimum":1,"maximum":100,"description":"JPEG quality (defaults to the configured quality)"}` +
			`,"label":{"type":"string","description":"short label shown with the image, e.g. \"screenshot of /tmp/ui.png\""}` +
			`},"oneOf":[{"required":["path"]},{"required":["ref"]}],"additionalProperties":false}`),
		Risk: registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[imageViewArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if t.images == nil {
			return registry.ToolResult{}, errors.New("image.view is unavailable: this session has no image store")
		}
		// The schema's oneOf already refuses both and neither; the handler
		// repeats the check because a hand-built Tool never went through
		// Register and so has no compiled validator.
		switch {
		case args.Path != "" && args.Ref != "":
			return registry.ToolResult{}, errors.New("image.view takes exactly one of path or ref; both were given")
		case args.Path == "" && args.Ref == "":
			return registry.ToolResult{}, errors.New("image.view requires one of path or ref")
		}
		if args.Ref != "" {
			return t.imageViewRef(ctx, args.Ref)
		}
		return t.imageViewPath(ctx, args)
	}
	return tool
}

// imageViewPath stores the named file and returns its ref. The source's own
// header is read first (and rewound) so the result can say the stored image is
// not the bytes the caller named: dimensions smaller than the source mean a
// downscale, and a different MIME means a re-encode.
func (t *toolSet) imageViewPath(ctx context.Context, args imageViewArgs) (registry.ToolResult, error) {
	// Same resolution file.read uses: policy containment, the named-root
	// aliases and the read-side allowances all apply, so an image cannot be
	// reached through a path a text read would refuse.
	path, err := t.resolveToolPath(args.Path, true)
	if err != nil {
		return registry.ToolResult{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return registry.ToolResult{}, fmt.Errorf("open %s: %w", args.Path, err)
	}
	defer f.Close()

	srcWidth, srcHeight, srcMIME := imageSourceHeader(f)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return registry.ToolResult{}, fmt.Errorf("rewind %s: %w", args.Path, err)
	}

	label := args.Label
	if label == "" {
		// The summary line leads with the label; an empty one would make it
		// start with the arrow. The path the caller named is the honest
		// fallback and needs no invention.
		label = args.Path
	}
	ref, err := t.images.Put(ctx, f, imagepayload.Options{
		MaxEdge: args.MaxEdge,
		Format:  args.Format,
		Quality: args.Quality,
		Label:   label,
	})
	if err != nil {
		return registry.ToolResult{}, fmt.Errorf("image.view %s: %w", args.Path, err)
	}
	return imageViewResult(ref, srcWidth, srcHeight, srcMIME), nil
}

// imageViewRef answers a hash the caller already holds. The ref is returned as
// stored: no source bytes exist to compare against, so nothing can be said
// about downscaling or conversion.
func (t *toolSet) imageViewRef(ctx context.Context, hash string) (registry.ToolResult, error) {
	ref, ok, err := t.images.Stat(ctx, hash)
	if err != nil {
		return registry.ToolResult{}, fmt.Errorf("image.view %s: %w", hash, err)
	}
	if !ok {
		return registry.ToolResult{}, fmt.Errorf("image.view: no stored image with hash %s (it may have been evicted; view the file by path again)", hash)
	}
	return imageViewResult(ref, 0, 0, ""), nil
}

// imageViewResult builds the model-facing result for one stored image.
// srcWidth/srcHeight/srcMIME describe the source file and are zero for a ref
// lookup, where there is no source to compare against.
func imageViewResult(ref schema.ImageRef, srcWidth, srcHeight int, srcMIME string) registry.ToolResult {
	result := registry.ToolResult{
		Summary: fmt.Sprintf("%s → %d×%d %s (%s)", ref.Label, ref.Width, ref.Height, shortMIME(ref.MIME), humanBytes(ref.Bytes)),
		Content: fmt.Sprintf("hash %s", ref.Hash),
		Images:  []schema.ImageRef{ref},
	}
	var notes []string
	if srcWidth > 0 && srcHeight > 0 && (ref.Width < srcWidth || ref.Height < srcHeight) {
		notes = append(notes, fmt.Sprintf("[downscaled from %d×%d]", srcWidth, srcHeight))
	}
	if srcMIME != "" && srcMIME != ref.MIME {
		notes = append(notes, fmt.Sprintf("[converted from %s to %s]", shortMIME(srcMIME), shortMIME(ref.MIME)))
	}
	if len(notes) == 0 {
		return result
	}
	text := strings.Join(notes, " ")
	result.Content += "\n" + text
	result.Notice = &registry.ToolNotice{
		Kind: NoticeImageAdjusted,
		Text: text,
		Data: map[string]any{
			"source_width":  srcWidth,
			"source_height": srcHeight,
			"source_mime":   srcMIME,
			"width":         ref.Width,
			"height":        ref.Height,
			"mime":          ref.MIME,
		},
	}
	return result
}

// imageSourceHeader reports the source image's dimensions and MIME type from
// its header, leaving the reader wherever the decoder stopped. It is
// best-effort: an undecodable header reports zeros, and the caller then makes
// no claim about downscaling or conversion.
func imageSourceHeader(r io.Reader) (width, height int, mime string) {
	cfg, format, err := image.DecodeConfig(r)
	if err == nil {
		return cfg.Width, cfg.Height, mimeOfFormat(format)
	}
	// The standard registry has no WebP decoder, and the store accepts WebP
	// sources, so a screenshot saved as .webp must not silently lose its
	// dimensions.
	if seeker, ok := r.(io.Seeker); ok {
		if _, serr := seeker.Seek(0, io.SeekStart); serr == nil {
			if cfg, werr := webp.DecodeConfig(r); werr == nil {
				return cfg.Width, cfg.Height, "image/webp"
			}
		}
	}
	return 0, 0, ""
}

// mimeOfFormat maps the standard decoders' format names to MIME types. An
// unknown format reports "" so the caller claims nothing about it.
func mimeOfFormat(format string) string {
	switch format {
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	}
	return ""
}

// shortMIME and humanBytes are duplicated from internal/agent on purpose: this
// package is imported by agent, so sharing them would invert the dependency.
// The spellings are kept identical so one summary line reads the same whether
// the tool or the transcript renders it.
func shortMIME(mime string) string {
	switch mime {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpeg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	}
	return mime
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
