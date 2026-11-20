package jpegmeta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"os"
	"testing"

	"github.com/mandykoh/prism/meta"
	"github.com/mandykoh/prism/meta/icc"
)

const (
	testJPEGPath          = "../../test-images/pizza-rgb8-adobergb.jpg"
	testJPEGNoProfilePath = "../../test-images/pizza-progressive.jpg"
	testICPProfilePath    = "../../test-profiles/display-p3-v4-with-v2-desc.icc"
	testLargeJPEGPath     = "../../test-images/pizza-cmyk8-usswop.jpg"
	testProgressivePath   = "../../test-images/pizza-progressive.jpg"
)

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Reading %s: %v", path, err)
	}
	return data
}

func readTestProfile(t *testing.T) []byte {
	t.Helper()

	return readTestFile(t, testICPProfilePath)
}

type jpegSegment struct {
	marker byte
	data   []byte
}

// parseJPEGSegments returns all segments in the header section of a JPEG
// stream, stopping at the start of scan.
func parseJPEGSegments(t *testing.T, data []byte) []jpegSegment {
	t.Helper()

	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		t.Fatalf("Stream does not begin with start-of-image")
	}

	var segments []jpegSegment
	offset := 2
	for offset < len(data) {
		if data[offset] != 0xFF {
			t.Fatalf("Expected marker at offset %d", offset)
		}
		marker := data[offset+1]
		offset += 2

		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			segments = append(segments, jpegSegment{marker: marker})
			continue
		}
		if marker == 0xD9 {
			segments = append(segments, jpegSegment{marker: marker})
			return segments
		}
		if marker == 0xDA {
			segments = append(segments, jpegSegment{marker: marker})
			return segments
		}

		length := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		if length < 2 || offset+length > len(data) {
			t.Fatalf("Invalid segment length %d at offset %d", length, offset)
		}
		segments = append(segments, jpegSegment{marker: marker, data: data[offset+2 : offset+length]})
		offset += length
	}

	return segments
}

// extractICCProfile reassembles any ICC_PROFILE APP2 segments in the header
// section, returning the profile bytes and the segments they came from.
func extractICCProfile(t *testing.T, segments []jpegSegment) (profileData []byte, chunks []jpegSegment) {
	t.Helper()

	type chunk struct {
		num   byte
		total byte
		data  []byte
	}

	var found []chunk
	for _, seg := range segments {
		if seg.marker != 0xE2 || len(seg.data) < len(iccProfileIdentifier)+2 {
			continue
		}
		if !bytes.HasPrefix(seg.data, iccProfileIdentifier) {
			continue
		}
		found = append(found, chunk{
			num:   seg.data[len(iccProfileIdentifier)],
			total: seg.data[len(iccProfileIdentifier)+1],
			data:  seg.data[len(iccProfileIdentifier)+2:],
		})
		chunks = append(chunks, seg)
	}

	if len(found) == 0 {
		return nil, nil
	}
	for _, c := range found {
		if int(c.total) != len(found) {
			t.Fatalf("ICC segment chunk total %d does not match %d segments", c.total, len(found))
		}
	}

	ordered := make([][]byte, len(found))
	for _, c := range found {
		if c.num == 0 || int(c.num) > len(ordered) {
			t.Fatalf("Invalid ICC chunk number %d", c.num)
		}
		if ordered[c.num-1] != nil {
			t.Fatalf("Repeated ICC chunk number %d", c.num)
		}
		ordered[c.num-1] = c.data
	}
	for i := range ordered {
		profileData = append(profileData, ordered[i]...)
	}

	return profileData, chunks
}

func decodeJPEG(t *testing.T, data []byte) image.Image {
	t.Helper()

	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decoding JPEG: %v", err)
	}
	return img
}

func assertPixelsEqual(t *testing.T, expected, actual image.Image) {
	t.Helper()

	if !expected.Bounds().Eq(actual.Bounds()) {
		t.Fatalf("Image bounds changed: %v vs %v", expected.Bounds(), actual.Bounds())
	}
	for y := expected.Bounds().Min.Y; y < expected.Bounds().Max.Y; y++ {
		for x := expected.Bounds().Min.X; x < expected.Bounds().Max.X; x++ {
			if expected.At(x, y) != actual.At(x, y) {
				t.Fatalf("Pixel (%d, %d) changed: %v vs %v", x, y, expected.At(x, y), actual.At(x, y))
			}
		}
	}
}

// makeLargeProfile returns a syntactically valid profile of the requested
// size, large enough to span several JPEG APP2 segments.
func makeLargeProfile(size int) []byte {
	profile := make([]byte, size)
	binary.BigEndian.PutUint32(profile[:4], uint32(size))
	profile[8] = 4 // Version 4.0
	copy(profile[12:16], "mntr")
	copy(profile[16:20], "RGB ")
	copy(profile[20:24], "XYZ ")
	copy(profile[36:40], "acsp")
	binary.BigEndian.PutUint32(profile[64:68], 0) // Perceptual
	for i := 132; i < size; i++ {
		profile[i] = byte(i * 31)
	}
	return profile
}

// scanOffset returns the byte offset of the start-of-scan marker, walking the
// header segment lengths rather than searching for the marker bytes.
func scanOffset(t *testing.T, data []byte) int {
	t.Helper()

	offset := 2
	for offset+1 < len(data) {
		if data[offset] != 0xFF {
			t.Fatalf("Expected marker at offset %d", offset)
		}
		marker := data[offset+1]
		if marker == 0xDA {
			return offset
		}
		length := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		offset += 2 + length
	}
	t.Fatalf("Could not locate start-of-scan marker")
	return -1
}

func TestWriteICCProfile(t *testing.T) {
	t.Run("round-trips profile bytes and preserves pixels and metadata", func(t *testing.T) {
		src := readTestFile(t, testJPEGPath)
		profile := readTestProfile(t)

		out := &bytes.Buffer{}
		if err := WriteICCProfile(out, bytes.NewReader(src), profile); err != nil {
			t.Fatalf("WriteICCProfile: %v", err)
		}

		segments := parseJPEGSegments(t, out.Bytes())

		// JFIF metadata must be preserved.
		if len(segments) == 0 || segments[0].marker != 0xE0 || !bytes.HasPrefix(segments[0].data, []byte("JFIF\x00")) {
			t.Errorf("JFIF APP0 segment was not preserved as the first segment")
		}

		// Exactly one ICC profile must be present, with the supplied bytes.
		gotProfile, chunks := extractICCProfile(t, segments)
		if len(chunks) != 1 {
			t.Fatalf("Expected exactly 1 ICC profile segment but got %d", len(chunks))
		}
		if !bytes.Equal(profile, gotProfile) {
			t.Fatalf("ICC profile round-trip mismatch (%d bytes in, %d bytes out)", len(profile), len(gotProfile))
		}

		// Reading through the metadata API must agree.
		md, imgStream, err := Load(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatalf("Loading metadata: %v", err)
		}
		mdProfile, err := md.ICCProfileData()
		if err != nil || !bytes.Equal(profile, mdProfile) {
			t.Fatalf("Metadata profile did not round-trip: %v", err)
		}
		if expected, actual := uint32(1200), md.PixelWidth; actual != expected {
			t.Errorf("Expected width %d but got %d", expected, actual)
		}
		if expected, actual := uint32(1200), md.PixelHeight; actual != expected {
			t.Errorf("Expected height %d but got %d", expected, actual)
		}

		// The decoded pixels must be unchanged.
		assertPixelsEqual(t, decodeJPEG(t, src), decodeJPEG(t, mustReadAll(t, imgStream)))

		// All non-ICC header segments must be preserved in order.
		srcSegments := parseJPEGSegments(t, src)
		assertNonICCSegmentsEqual(t, srcSegments, segments)
	})

	t.Run("preserves scan data and trailing bytes byte-for-byte", func(t *testing.T) {
		src := readTestFile(t, testLargeJPEGPath)
		profile := makeLargeProfile(60000)

		out := &bytes.Buffer{}
		if err := WriteICCProfile(out, bytes.NewReader(src), profile); err != nil {
			t.Fatalf("WriteICCProfile: %v", err)
		}

		// Everything from the start-of-scan marker to the end of the stream
		// (entropy-coded data, restart markers, EOI) must be byte-identical.
		srcScan := scanOffset(t, src)
		outScan := scanOffset(t, out.Bytes())
		if !bytes.Equal(src[srcScan:], out.Bytes()[outScan:]) {
			t.Fatalf("Scan data changed")
		}
	})

	t.Run("splits a profile across two APP2 segments and reassembles it", func(t *testing.T) {
		src := readTestFile(t, testJPEGNoProfilePath)

		// One segment holds 65519 payload bytes after the 14-byte ICC header,
		// so this needs exactly two segments, with the second non-empty.
		profile := makeLargeProfile(65519 + 1000)

		out := &bytes.Buffer{}
		if err := WriteICCProfile(out, bytes.NewReader(src), profile); err != nil {
			t.Fatalf("WriteICCProfile: %v", err)
		}

		segments := parseJPEGSegments(t, out.Bytes())
		gotProfile, chunks := extractICCProfile(t, segments)
		if len(chunks) != 2 {
			t.Fatalf("Expected profile split across 2 APP2 segments but got %d", len(chunks))
		}
		for i, c := range chunks {
			chunkNum := c.data[len(iccProfileIdentifier)]
			chunkTotal := c.data[len(iccProfileIdentifier)+1]
			if expected, actual := byte(i+1), chunkNum; actual != expected {
				t.Errorf("Segment %d has chunk number %d", i, actual)
			}
			if expected, actual := byte(2), chunkTotal; actual != expected {
				t.Errorf("Segment %d has chunk total %d", i, actual)
			}
			if len(c.data) > 0xFFFF-2 {
				t.Errorf("Segment %d exceeds the maximum segment size", i)
			}
		}
		if !bytes.Equal(profile, gotProfile) {
			t.Fatalf("Multi-segment ICC profile did not round-trip")
		}

		// The library's own reader must reassemble it identically too.
		md, _, err := Load(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatalf("Loading metadata: %v", err)
		}
		if mdProfile, err := md.ICCProfileData(); err != nil || !bytes.Equal(profile, mdProfile) {
			t.Fatalf("Metadata profile did not round-trip: %v", err)
		}
	})

	t.Run("round-trips a profile already spread across nine segments", func(t *testing.T) {
		src := readTestFile(t, testLargeJPEGPath)

		srcSegments := parseJPEGSegments(t, src)
		originalProfile, originalChunks := extractICCProfile(t, srcSegments)
		if len(originalChunks) != 9 {
			t.Fatalf("Fixture should have 9 ICC segments but has %d", len(originalChunks))
		}

		// Replace the nine-segment profile with a different one and verify the
		// old segments are gone.
		profile := makeLargeProfile(40000)

		out := &bytes.Buffer{}
		if err := WriteICCProfile(out, bytes.NewReader(src), profile); err != nil {
			t.Fatalf("WriteICCProfile: %v", err)
		}

		segments := parseJPEGSegments(t, out.Bytes())
		gotProfile, chunks := extractICCProfile(t, segments)
		if len(chunks) != 1 {
			t.Fatalf("Expected 1 ICC segment after replacement but got %d", len(chunks))
		}
		if !bytes.Equal(profile, gotProfile) {
			t.Fatalf("Profile did not round-trip")
		}
		if bytes.Equal(originalProfile, gotProfile) {
			t.Errorf("Original profile was not replaced")
		}

		// Then re-embed the original nine-segment profile byte-for-byte.
		out2 := &bytes.Buffer{}
		if err := WriteICCProfile(out2, bytes.NewReader(src), originalProfile); err != nil {
			t.Fatalf("WriteICCProfile with original profile: %v", err)
		}
		gotProfile2, chunks2 := extractICCProfile(t, parseJPEGSegments(t, out2.Bytes()))
		if len(chunks2) != 9 {
			t.Fatalf("Expected 9 ICC segments but got %d", len(chunks2))
		}
		if !bytes.Equal(originalProfile, gotProfile2) {
			t.Fatalf("Nine-segment profile did not round-trip")
		}

		assertPixelsEqual(t, decodeJPEG(t, src), decodeJPEG(t, out2.Bytes()))
	})

	t.Run("writes a profile into an image that had none", func(t *testing.T) {
		src := readTestFile(t, testProgressivePath)
		profile := readTestProfile(t)

		out := &bytes.Buffer{}
		if err := WriteICCProfile(out, bytes.NewReader(src), profile); err != nil {
			t.Fatalf("WriteICCProfile: %v", err)
		}

		gotProfile, chunks := extractICCProfile(t, parseJPEGSegments(t, out.Bytes()))
		if len(chunks) != 1 || !bytes.Equal(profile, gotProfile) {
			t.Fatalf("Profile did not round-trip")
		}

		assertPixelsEqual(t, decodeJPEG(t, src), decodeJPEG(t, out.Bytes()))
	})

	t.Run("preserves trailing bytes after the end-of-image marker", func(t *testing.T) {
		src := append(readTestFile(t, testJPEGPath), []byte("TRAILING")...)

		out := &bytes.Buffer{}
		if err := WriteICCProfile(out, bytes.NewReader(src), readTestProfile(t)); err != nil {
			t.Fatalf("WriteICCProfile: %v", err)
		}

		if !bytes.HasSuffix(out.Bytes(), []byte("TRAILING")) {
			t.Errorf("Trailing data was not preserved")
		}
	})

	t.Run("rejects an invalid profile", func(t *testing.T) {
		src := readTestFile(t, testJPEGPath)

		for name, profile := range map[string][]byte{
			"empty":   nil,
			"garbage": []byte("not a profile"),
			"wrong signature": func() []byte {
				p := append([]byte(nil), readTestProfile(t)...)
				p[36] = 'x'
				return p
			}(),
			"declared size too large": func() []byte {
				p := append([]byte(nil), readTestProfile(t)...)
				binary.BigEndian.PutUint32(p[:4], uint32(len(p))+1)
				return p
			}(),
			"too large": func() []byte {
				p := make([]byte, icc.MaxProfileSize+1)
				binary.BigEndian.PutUint32(p[:4], uint32(len(p)))
				copy(p[36:], "acsp")
				return p
			}(),
		} {
			out := &bytes.Buffer{}
			err := WriteICCProfile(out, bytes.NewReader(src), profile)
			if !errors.Is(err, icc.ErrInvalidProfile) {
				t.Errorf("%s: expected icc.ErrInvalidProfile but got %v", name, err)
			}
			if out.Len() != 0 {
				t.Errorf("%s: expected no output but got %d bytes", name, out.Len())
			}
		}
	})

	t.Run("rejects a stream needing more than 255 segments", func(t *testing.T) {
		src := readTestFile(t, testJPEGPath)

		// 255 segments hold at most 255 * (65533 - 14) bytes, so this profile
		// needs 256 segments.
		profile := makeLargeProfile(255*(0xFFFF-2-14) + 1)

		err := WriteICCProfile(io.Discard, bytes.NewReader(src), profile)
		if !errors.Is(err, icc.ErrInvalidProfile) {
			t.Fatalf("Expected icc.ErrInvalidProfile but got %v", err)
		}
	})

	t.Run("rejects an empty image", func(t *testing.T) {
		err := WriteICCProfile(io.Discard, bytes.NewReader(nil), readTestProfile(t))
		if !errors.Is(err, meta.ErrEmptyImage) {
			t.Fatalf("Expected meta.ErrEmptyImage but got %v", err)
		}
	})

	t.Run("rejects a stream with no image data", func(t *testing.T) {
		src := []byte{0xFF, 0xD8, 0xFF, 0xD9}

		err := WriteICCProfile(io.Discard, bytes.NewReader(src), readTestProfile(t))
		if !errors.Is(err, meta.ErrEmptyImage) {
			t.Fatalf("Expected meta.ErrEmptyImage but got %v", err)
		}
	})

	t.Run("rejects a truncated stream", func(t *testing.T) {
		src := readTestFile(t, testJPEGPath)

		for _, cut := range []int{
			1,            // Partway through start-of-image
			10,           // Partway through a segment length
			200,          // Partway through a segment
			len(src) / 2, // Partway through entropy-coded data
			len(src) - 1, // Missing end-of-image marker
		} {
			err := WriteICCProfile(io.Discard, bytes.NewReader(src[:cut]), readTestProfile(t))
			if !errors.Is(err, meta.ErrTruncatedImage) {
				t.Errorf("cut at %d: expected meta.ErrTruncatedImage but got %v", cut, err)
			}
		}
	})

	t.Run("rejects a non-JPEG stream", func(t *testing.T) {
		err := WriteICCProfile(io.Discard, bytes.NewReader([]byte("GIF89a not a jpeg")), readTestProfile(t))
		if !errors.Is(err, meta.ErrUnsupportedFormat) {
			t.Fatalf("Expected meta.ErrUnsupportedFormat but got %v", err)
		}
	})

	t.Run("rejects an existing profile with repeated segments", func(t *testing.T) {
		src := readTestFile(t, testJPEGPath)
		segments := parseJPEGSegments(t, src)

		withDuplicate := &bytes.Buffer{}
		withDuplicate.Write([]byte{0xFF, 0xD8})
		for _, seg := range segments {
			if bytes.HasPrefix(seg.data, iccProfileIdentifier) {
				writeTestSegment(t, withDuplicate, seg.marker, seg.data)
				writeTestSegment(t, withDuplicate, seg.marker, seg.data)
				continue
			}
			writeTestSegment(t, withDuplicate, seg.marker, seg.data)
		}

		err := WriteICCProfile(io.Discard, bytes.NewReader(withDuplicate.Bytes()), readTestProfile(t))
		if !errors.Is(err, ErrDuplicateICC) {
			t.Fatalf("Expected ErrDuplicateICC but got %v", err)
		}
	})

	t.Run("rejects an existing profile with an incomplete segment set", func(t *testing.T) {
		src := readTestFile(t, testLargeJPEGPath)
		segments := parseJPEGSegments(t, src)

		incomplete := &bytes.Buffer{}
		incomplete.Write([]byte{0xFF, 0xD8})
		skipped := false
		for _, seg := range segments {
			if bytes.HasPrefix(seg.data, iccProfileIdentifier) && !skipped {
				skipped = true
				continue
			}
			writeTestSegment(t, incomplete, seg.marker, seg.data)
		}

		err := WriteICCProfile(io.Discard, bytes.NewReader(incomplete.Bytes()), readTestProfile(t))
		if !errors.Is(err, ErrDuplicateICC) {
			t.Fatalf("Expected ErrDuplicateICC but got %v", err)
		}
	})

	t.Run("returns an error when dst is not writable", func(t *testing.T) {
		src := readTestFile(t, testJPEGPath)

		err := WriteICCProfile(failingWriter{}, bytes.NewReader(src), readTestProfile(t))
		if err == nil {
			t.Fatalf("Expected an error but succeeded")
		}
		if !errors.Is(err, errTestWrite) {
			t.Errorf("Expected the write error to be preserved but got %v", err)
		}
	})
}

var errTestWrite = errors.New("test write failure")

type failingWriter struct{}

func (failingWriter) Write(p []byte) (int, error) {
	return 0, errTestWrite
}

func mustReadAll(t *testing.T, r io.Reader) []byte {
	t.Helper()

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("Reading stream: %v", err)
	}
	return data
}

func writeTestSegment(t *testing.T, dst *bytes.Buffer, marker byte, data []byte) {
	t.Helper()

	dst.WriteByte(0xFF)
	dst.WriteByte(marker)
	if marker == 0xD8 || marker == 0xD9 {
		return
	}
	length := len(data) + 2
	dst.WriteByte(byte(length >> 8))
	dst.WriteByte(byte(length))
	dst.Write(data)
}

// assertNonICCSegmentsEqual checks that every segment other than ICC profile
// and start-of-scan segments matches between the two streams in order.
func assertNonICCSegmentsEqual(t *testing.T, expected, actual []jpegSegment) {
	t.Helper()

	filter := func(segments []jpegSegment) []jpegSegment {
		var result []jpegSegment
		for _, seg := range segments {
			if seg.marker == 0xE2 && bytes.HasPrefix(seg.data, iccProfileIdentifier) {
				continue
			}
			if seg.marker == 0xDA {
				continue
			}
			result = append(result, seg)
		}
		return result
	}

	expected = filter(expected)
	actual = filter(actual)

	if len(expected) != len(actual) {
		t.Fatalf("Expected %d non-ICC segments but got %d", len(expected), len(actual))
	}
	for i := range expected {
		if expected[i].marker != actual[i].marker || !bytes.Equal(expected[i].data, actual[i].data) {
			t.Fatalf("Segment %d (marker 0x%02x) changed", i, expected[i].marker)
		}
	}
}
