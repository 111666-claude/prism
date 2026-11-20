package meta

import "errors"

var (
	// ErrEmptyImage is returned when an image stream contains no image data.
	ErrEmptyImage = errors.New("empty image")

	// ErrTruncatedImage is returned when an image stream ends before a
	// complete image container could be read.
	ErrTruncatedImage = errors.New("truncated image")

	// ErrUnsupportedFormat is returned when an image stream is not in a
	// format supported by the requested operation.
	ErrUnsupportedFormat = errors.New("unsupported image format")
)
