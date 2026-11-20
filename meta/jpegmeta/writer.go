package jpegmeta

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/mandykoh/prism/meta"
	"github.com/mandykoh/prism/meta/icc"
)

const (
	// maxSegmentDataLength is the maximum number of payload bytes a JPEG
	// segment can carry (the 16-bit length field includes itself).
	maxSegmentDataLength = 0xFFFF - 2

	// maxICCChunkCount is the largest chunk number representable in the
	// sequence count byte of an ICC_PROFILE APP2 segment.
	maxICCChunkCount = 0xFF

	// iccChunkHeaderLength is the identifier plus the chunk number and total
	// chunk count bytes.
	iccChunkHeaderLength = 14

	markerTEM = 0x01
	markerSOI = 0xD8
	markerEOI = 0xD9
	markerSOS = 0xDA
)

var (
	// ErrDuplicateICC is returned when a source JPEG already contains ICC
	// profile segments that cannot be unambiguously replaced, such as repeated
	// segment numbers, inconsistent segment counts, or an incomplete set.
	ErrDuplicateICC = errors.New("duplicate ICC profile")

	// ErrInvalidJPEG is returned when a JPEG stream is structurally invalid.
	ErrInvalidJPEG = errors.New("invalid JPEG stream")
)

// WriteICCProfile copies a JPEG stream from src to dst, embedding profileData
// in a sequence of APP2 segments as specified by the ICC profile embedding
// convention.
//
// Profiles too large for one segment are split across multiple APP2 segments,
// each carrying the ICC_PROFILE identifier, a 1-based chunk number, and the
// total chunk count. Any ICC_PROFILE segments already in the source are
// removed and replaced, provided they describe one complete, unambiguous
// profile; otherwise ErrDuplicateICC is returned. All other segments, the
// entropy-coded image data, and any bytes following the end-of-image marker
// are copied unchanged, so the decoded pixels are identical to the source and
// reading the result back yields exactly the bytes supplied in profileData.
//
// An error is returned if profileData is not a valid ICC profile (wrapping
// icc.ErrInvalidProfile) or is too large to split into at most 255 segments,
// if the source stream is not a JPEG (wrapping meta.ErrUnsupportedFormat), is
// empty (meta.ErrEmptyImage), or is truncated (meta.ErrTruncatedImage), if its
// existing ICC profile segments are ambiguous (ErrDuplicateICC), or if writing
// to dst fails. Because the stream is copied as it is read, dst may have
// received partial output when an error is returned.
func WriteICCProfile(dst io.Writer, src io.Reader, profileData []byte) error {
	if err := icc.ValidateProfileData(profileData); err != nil {
		return err
	}

	chunks, err := splitICCProfile(profileData)
	if err != nil {
		return err
	}

	br := bufio.NewReader(src)
	bw := bufio.NewWriter(dst)

	// Start-of-image has no length field.
	first, err := br.ReadByte()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return meta.ErrEmptyImage
		}
		return fmt.Errorf("%w: reading start-of-image marker: %v", meta.ErrTruncatedImage, err)
	}
	if first != 0xFF {
		return fmt.Errorf("%w: stream does not begin with a JPEG marker", meta.ErrUnsupportedFormat)
	}
	second, err := br.ReadByte()
	if err != nil {
		return fmt.Errorf("%w: reading start-of-image marker", meta.ErrTruncatedImage)
	}
	if second != markerSOI {
		return fmt.Errorf("%w: stream does not begin with start-of-image", meta.ErrUnsupportedFormat)
	}
	if _, err := bw.Write([]byte{0xFF, markerSOI}); err != nil {
		return wrapWriteError(err)
	}

	profileWritten := false
	existing := iccSegmentTracker{}

	for {
		seg, err := readSegmentForWrite(br)
		if err != nil {
			return err
		}

		switch seg.marker {

		case markerSOS:
			if err := existing.complete(); err != nil {
				return err
			}

			if !profileWritten {
				if err := writeICCChunks(bw, chunks); err != nil {
					return err
				}
				profileWritten = true
			}

			if err := writeSegmentForWrite(bw, seg); err != nil {
				return err
			}

			// Copy the entropy-coded data and all following segments,
			// verifying that the stream reaches its end-of-image marker.
			if err := copyEntropyCodedDataAndRest(bw, br); err != nil {
				return err
			}
			if err := bw.Flush(); err != nil {
				return wrapWriteError(err)
			}
			return nil

		case markerEOI:
			return fmt.Errorf("%w: JPEG contains no image data", meta.ErrEmptyImage)

		case markerSOI:
			return fmt.Errorf("%w: unexpected start-of-image marker", ErrInvalidJPEG)

		case byte(markerTypeApp2):
			isICC, chunkNum, chunkTotal := parseICCChunk(seg.data)
			if isICC {
				// ICC_PROFILE segments in the source are superseded by the
				// profile being written, but the source must still describe a
				// single coherent profile for its removal to be unambiguous.
				if err := existing.add(chunkNum, chunkTotal); err != nil {
					return err
				}
				continue
			}
		}

		if err := writeSegmentForWrite(bw, seg); err != nil {
			return err
		}

		// Insert the new profile immediately after the first segment so it
		// appears in the conventional location near the start of the stream.
		if !profileWritten {
			if err := writeICCChunks(bw, chunks); err != nil {
				return err
			}
			profileWritten = true
		}
	}
}

func splitICCProfile(profileData []byte) ([][]byte, error) {
	maxPayload := maxSegmentDataLength - iccChunkHeaderLength
	chunkCount := (len(profileData) + maxPayload - 1) / maxPayload
	if chunkCount == 0 {
		chunkCount = 1
	}
	if chunkCount > maxICCChunkCount {
		return nil, fmt.Errorf("%w: profile of %d bytes requires %d segments, maximum is %d",
			icc.ErrInvalidProfile, len(profileData), chunkCount, maxICCChunkCount)
	}

	chunks := make([][]byte, 0, chunkCount)
	for offset := 0; offset < len(profileData); offset += maxPayload {
		end := offset + maxPayload
		if end > len(profileData) {
			end = len(profileData)
		}

		chunk := make([]byte, 0, iccChunkHeaderLength+(end-offset))
		chunk = append(chunk, iccProfileIdentifier...)
		chunk = append(chunk, byte(len(chunks)+1), byte(chunkCount))
		chunk = append(chunk, profileData[offset:end]...)
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

func writeICCChunks(dst io.Writer, chunks [][]byte) error {
	for _, chunk := range chunks {
		if err := writeSegmentForWrite(dst, rawSegment{marker: byte(markerTypeApp2), data: chunk}); err != nil {
			return err
		}
	}
	return nil
}

// iccSegmentTracker validates that the ICC_PROFILE segments already present in
// a source stream describe exactly one complete profile, so that removing them
// is unambiguous.
type iccSegmentTracker struct {
	total   byte
	seen    [maxICCChunkCount + 1]bool
	count   int
	started bool
}

func (t *iccSegmentTracker) add(chunkNum, chunkTotal byte) error {
	if chunkTotal == 0 {
		return fmt.Errorf("%w: existing ICC profile declares zero segments", ErrInvalidJPEG)
	}
	if chunkNum == 0 || chunkNum > chunkTotal {
		return fmt.Errorf("%w: existing ICC profile segment number %d of %d is invalid", ErrInvalidJPEG, chunkNum, chunkTotal)
	}
	if t.started && chunkTotal != t.total {
		return fmt.Errorf("%w: existing ICC profile segments disagree on segment count", ErrDuplicateICC)
	}
	if t.seen[chunkNum] {
		return fmt.Errorf("%w: existing ICC profile segment %d is repeated", ErrDuplicateICC, chunkNum)
	}

	t.started = true
	t.total = chunkTotal
	t.seen[chunkNum] = true
	t.count++

	return nil
}

func (t *iccSegmentTracker) complete() error {
	if t.started && t.count != int(t.total) {
		return fmt.Errorf("%w: existing ICC profile is incomplete (%d of %d segments)", ErrDuplicateICC, t.count, t.total)
	}
	return nil
}

func parseICCChunk(data []byte) (isICC bool, chunkNum, chunkTotal byte) {
	if len(data) < iccChunkHeaderLength {
		return false, 0, 0
	}
	if !bytes.Equal(data[:len(iccProfileIdentifier)], iccProfileIdentifier) {
		return false, 0, 0
	}

	return true, data[len(iccProfileIdentifier)], data[len(iccProfileIdentifier)+1]
}

type rawSegment struct {
	marker byte
	data   []byte
}

func isStandaloneMarker(m byte) bool {
	return m == markerTEM || (m >= 0xD0 && m <= markerEOI)
}

// readSegmentForWrite reads the next marker and its payload, without
// interpreting marker types beyond what is needed to find segment boundaries.
func readSegmentForWrite(r *bufio.Reader) (rawSegment, error) {
	b, err := r.ReadByte()
	if err != nil {
		return rawSegment{}, fmt.Errorf("%w: missing end-of-image marker", meta.ErrTruncatedImage)
	}
	if b != 0xFF {
		return rawSegment{}, fmt.Errorf("%w: expected marker but got 0x%02x", ErrInvalidJPEG, b)
	}

	for {
		b, err = r.ReadByte()
		if err != nil {
			return rawSegment{}, fmt.Errorf("%w: reading marker", meta.ErrTruncatedImage)
		}
		if b != 0xFF {
			break
		}
	}

	if isStandaloneMarker(b) {
		return rawSegment{marker: b}, nil
	}

	length, err := readU16Big(r)
	if err != nil {
		return rawSegment{}, fmt.Errorf("%w: reading marker 0x%02x length", meta.ErrTruncatedImage, b)
	}
	if length < 2 {
		return rawSegment{}, fmt.Errorf("%w: marker 0x%02x has invalid length %d", ErrInvalidJPEG, b, length)
	}

	data := make([]byte, int(length)-2)
	if _, err := io.ReadFull(r, data); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return rawSegment{}, fmt.Errorf("%w: incomplete marker 0x%02x segment", meta.ErrTruncatedImage, b)
		}
		return rawSegment{}, err
	}

	return rawSegment{marker: b, data: data}, nil
}

func writeSegmentForWrite(dst io.Writer, seg rawSegment) error {
	if isStandaloneMarker(seg.marker) {
		if _, err := dst.Write([]byte{0xFF, seg.marker}); err != nil {
			return wrapWriteError(err)
		}
		return nil
	}

	if len(seg.data) > maxSegmentDataLength {
		return fmt.Errorf("%w: segment 0x%02x is too large", ErrInvalidJPEG, seg.marker)
	}

	length := len(seg.data) + 2
	header := [4]byte{0xFF, seg.marker, byte(length >> 8), byte(length)}
	if _, err := dst.Write(header[:]); err != nil {
		return wrapWriteError(err)
	}
	if _, err := dst.Write(seg.data); err != nil {
		return wrapWriteError(err)
	}

	return nil
}

// copyEntropyCodedDataAndRest copies the remainder of a JPEG stream, starting
// immediately after a start-of-scan segment, through to its end-of-image
// marker or the end of the stream. Segment framing is honoured so that markers
// inside payloads are not mistaken for the end of image; a stream that ends
// before its end-of-image marker is reported as truncated.
func copyEntropyCodedDataAndRest(dst *bufio.Writer, src *bufio.Reader) error {
	for {
		b, err := src.ReadByte()
		if err != nil {
			return fmt.Errorf("%w: missing end-of-image marker", meta.ErrTruncatedImage)
		}
		if err := dst.WriteByte(b); err != nil {
			return wrapWriteError(err)
		}

		if b != 0xFF {
			continue
		}

		// Read the marker identifier, tolerating fill bytes.
		id, err := src.ReadByte()
		if err != nil {
			return fmt.Errorf("%w: reading marker after entropy-coded data", meta.ErrTruncatedImage)
		}
		for id == 0xFF {
			if err := dst.WriteByte(id); err != nil {
				return wrapWriteError(err)
			}
			id, err = src.ReadByte()
			if err != nil {
				return fmt.Errorf("%w: reading marker after entropy-coded data", meta.ErrTruncatedImage)
			}
		}
		if err := dst.WriteByte(id); err != nil {
			return wrapWriteError(err)
		}

		switch {
		case id == 0x00:
			// Stuffed byte within entropy-coded data.
			continue

		case id >= 0xD0 && id <= 0xD7:
			// Restart marker.
			continue

		case id == markerEOI:
			// Copy anything following the end-of-image marker verbatim.
			if _, err := io.Copy(dst, src); err != nil {
				return fmt.Errorf("copying JPEG trailing data: %w", err)
			}
			return nil

		case id == markerSOI:
			return fmt.Errorf("%w: unexpected start-of-image marker", ErrInvalidJPEG)

		default:
			length, err := readU16Big(src)
			if err != nil {
				return fmt.Errorf("%w: reading marker 0x%02x length", meta.ErrTruncatedImage, id)
			}
			if length < 2 {
				return fmt.Errorf("%w: marker 0x%02x has invalid length %d", ErrInvalidJPEG, id, length)
			}

			var lengthBytes [2]byte
			lengthBytes[0] = byte(length >> 8)
			lengthBytes[1] = byte(length)
			if _, err := dst.Write(lengthBytes[:]); err != nil {
				return wrapWriteError(err)
			}

			if _, err := io.CopyN(dst, src, int64(length)-2); err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					return fmt.Errorf("%w: incomplete marker 0x%02x segment", meta.ErrTruncatedImage, id)
				}
				return fmt.Errorf("copying JPEG marker 0x%02x segment: %w", id, err)
			}
		}
	}
}

func readU16Big(r io.ByteReader) (uint16, error) {
	b1, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	b2, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	return uint16(b1)<<8 | uint16(b2), nil
}

func wrapWriteError(err error) error {
	return fmt.Errorf("writing JPEG: %w", err)
}
