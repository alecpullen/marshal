package imagepayload

import "errors"

// Errors, all matchable with errors.Is.
var (
	ErrUnsupportedFormat = errors.New("imagepayload: unsupported image format")
	ErrCorruptImage      = errors.New("imagepayload: corrupt image")
	ErrTooLarge          = errors.New("imagepayload: image exceeds a configured limit")
	ErrNotFound          = errors.New("imagepayload: image not in store")
)
