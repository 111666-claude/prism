package prism

import (
	"io"

	"github.com/mandykoh/prism/meta/autometa"
)

// WriteICCProfile copies an encoded image stream from src to dst, embedding
// profileData as the image’s ICC colour profile.
//
// This is the operation needed when an image has been colour-corrected into a
// particular encoding (for example Display P3 or Adobe RGB) and the exported
// file must carry the profile actually used, so that another viewer or display
// interprets the pixel values correctly instead of assuming sRGB.
//
// PNG and JPEG are supported. The image format is detected from the stream.
// Pixel data and metadata unrelated to colour (EXIF, XMP, JFIF, and other
// chunks or segments) are copied unchanged, so the decoded image is identical
// to the source. Reading the profile back from the output yields exactly the
// bytes supplied in profileData.
//
// For PNG, an existing iCCP chunk is replaced and sRGB, gAMA, and cHRM chunks
// are removed, because they declare a conflicting colour space. Use
// meta/autometa.WriteICCProfileWithOptions or the format-specific writers in
// meta/pngmeta and meta/jpegmeta for finer control.
//
// An error is returned if profileData is not a valid ICC profile (wrapping
// icc.ErrInvalidProfile), if the source is not a supported image format
// (meta.ErrUnsupportedFormat), is empty (meta.ErrEmptyImage), or is truncated
// (meta.ErrTruncatedImage), if the source declares conflicting ICC profiles,
// or if writing to dst fails.
func WriteICCProfile(dst io.Writer, src io.Reader, profileData []byte) error {
	return autometa.WriteICCProfile(dst, src, profileData)
}
