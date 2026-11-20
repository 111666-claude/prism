package autometa

import (
	"bufio"
	"bytes"
	"fmt"
	"io"

	"github.com/mandykoh/prism/meta"
	"github.com/mandykoh/prism/meta/icc"
	"github.com/mandykoh/prism/meta/jpegmeta"
	"github.com/mandykoh/prism/meta/pngmeta"
)

// WriteOptions controls how WriteICCProfile handles format-specific
// conflicts. It is a superset of the format-specific options.
type WriteOptions struct {
	// PNGConflictPolicy is used only when writing PNG data. The zero value
	// is pngmeta.ReplaceConflicts.
	//
	// JPEG has no equivalent choice: a single ICC profile may be embedded,
	// so existing ICC segments are always replaced.
	PNGConflictPolicy pngmeta.ConflictPolicy
}

// WriteICCProfile copies an image stream from src to dst, embedding
// profileData as its ICC colour profile.
//
// The image format is detected from the stream contents. PNG and JPEG are
// supported; for any other format an error wrapping meta.ErrUnsupportedFormat
// is returned. Pixel data and metadata unrelated to colour are preserved
// unchanged, so the decoded image is identical to the source and reading the
// result back yields exactly the bytes supplied in profileData.
//
// See pngmeta.WriteICCProfileWithOptions and jpegmeta.WriteICCProfile for
// format-specific details and errors.
func WriteICCProfile(dst io.Writer, src io.Reader, profileData []byte) error {
	return WriteICCProfileWithOptions(dst, src, profileData, WriteOptions{})
}

// WriteICCProfileWithOptions is like WriteICCProfile but allows the caller to
// control how existing colour space declarations are handled.
func WriteICCProfileWithOptions(dst io.Writer, src io.Reader, profileData []byte, options WriteOptions) error {
	// Validate the profile first so a bad profile is reported clearly,
	// regardless of whether the image format is supported.
	if err := icc.ValidateProfileData(profileData); err != nil {
		return err
	}

	br := bufio.NewReader(src)
	header, err := br.Peek(8)
	if err != nil {
		if len(header) == 0 {
			return meta.ErrEmptyImage
		}
		return fmt.Errorf("%w: reading image header: %v", meta.ErrTruncatedImage, err)
	}

	switch {
	case bytes.Equal(header, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return pngmeta.WriteICCProfileWithOptions(dst, br, profileData, pngmeta.WriteOptions{
			ConflictPolicy: options.PNGConflictPolicy,
		})

	case header[0] == 0xFF && header[1] == 0xD8:
		return jpegmeta.WriteICCProfile(dst, br, profileData)

	default:
		return meta.ErrUnsupportedFormat
	}
}
