package pngmeta

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	"github.com/mandykoh/prism/meta"
	"github.com/mandykoh/prism/meta/icc"
)

// defaultICCProfileName is the profile name written to the iCCP chunk. PNG
// requires a Latin-1 name between 1 and 79 bytes, terminated by a null byte.
const defaultICCProfileName = "ICC Profile"

// maxPNGChunkLength is the largest chunk length permitted by the PNG
// specification.
const maxPNGChunkLength = 0x7FFFFFFF

var (
	// ErrInvalidPNG is returned when a PNG stream is structurally invalid.
	ErrInvalidPNG = errors.New("invalid PNG stream")

	// ErrDuplicateICC is returned when a stream contains more than one iCCP
	// chunk. The PNG specification permits at most one, so the intended
	// profile is ambiguous.
	ErrDuplicateICC = errors.New("duplicate ICC profile")

	// ErrColorSpaceConflict is returned by RejectConflicts when the stream
	// already declares a colour space through iCCP, sRGB, gAMA, or cHRM
	// chunks.
	ErrColorSpaceConflict = errors.New("ICC profile conflicts with existing colour space chunks")
)

// ConflictPolicy determines how WriteICCProfileWithOptions treats colour space
// chunks already present in the source PNG stream.
type ConflictPolicy int

const (
	// ReplaceConflicts (the default) replaces any existing iCCP chunk and
	// removes sRGB, gAMA, and cHRM chunks. Those chunks describe an implicit
	// colour space which would contradict the newly embedded profile (the PNG
	// specification forbids combining iCCP with sRGB). All other ancillary
	// chunks, the image data, and the pixel values themselves are preserved.
	ReplaceConflicts ConflictPolicy = iota

	// RejectConflicts refuses to write the profile if the source stream
	// already contains an iCCP, sRGB, gAMA, or cHRM chunk, returning an error
	// wrapping ErrColorSpaceConflict. This is useful when the caller needs to
	// decide explicitly which colour space declaration wins.
	RejectConflicts
)

// WriteOptions controls how WriteICCProfileWithOptions writes an ICC profile.
type WriteOptions struct {
	// ConflictPolicy determines what happens when the source image already
	// declares a colour space. The zero value is ReplaceConflicts.
	ConflictPolicy ConflictPolicy
}

// WriteICCProfile copies a PNG stream from src to dst, embedding profileData
// in an iCCP chunk.
//
// The image data and all chunks unrelated to colour space are copied
// unchanged, so the decoded pixels are identical to the source. Any existing
// iCCP, sRGB, gAMA, and cHRM chunks are replaced or removed, since those
// declarations conflict with an embedded ICC profile (see ReplaceConflicts).
// The profile is zlib-compressed as required by the PNG specification, and
// reading the result back yields exactly the bytes supplied in profileData.
//
// An error is returned if profileData is not a valid ICC profile (wrapping
// icc.ErrInvalidProfile), if the source stream is not a PNG (wrapping
// meta.ErrUnsupportedFormat), is empty (meta.ErrEmptyImage), or is truncated
// (meta.ErrTruncatedImage), if it contains duplicate iCCP chunks
// (ErrDuplicateICC), or if writing to dst fails. The stream is copied as it
// is read, so dst may have received partial output when an error is returned.
func WriteICCProfile(dst io.Writer, src io.Reader, profileData []byte) error {
	return WriteICCProfileWithOptions(dst, src, profileData, WriteOptions{})
}

// WriteICCProfileWithOptions is like WriteICCProfile but allows the caller to
// control how existing colour space declarations are handled.
func WriteICCProfileWithOptions(dst io.Writer, src io.Reader, profileData []byte, options WriteOptions) error {
	switch options.ConflictPolicy {
	case ReplaceConflicts, RejectConflicts:
	default:
		return fmt.Errorf("%w: unknown conflict policy %d", ErrInvalidPNG, options.ConflictPolicy)
	}

	if err := icc.ValidateProfileData(profileData); err != nil {
		return err
	}

	iccChunkData, err := compressICCProfile(profileData, defaultICCProfileName)
	if err != nil {
		return fmt.Errorf("preparing ICC profile chunk: %w", err)
	}

	br := bufio.NewReader(src)
	if err := readAndCheckSignature(br); err != nil {
		return err
	}

	// IHDR must be first and is copied verbatim. The rest of the stream is
	// forwarded as it is read, so arbitrarily large images do not need to be
	// buffered in memory.
	ihdr, err := readIHDR(br)
	if err != nil {
		return err
	}

	bw := bufio.NewWriter(dst)
	if _, err := bw.Write(pngSignature[:]); err != nil {
		return wrapWriteError(err)
	}
	if err := writeIHDR(bw, ihdr); err != nil {
		return err
	}

	// iCCP must precede PLTE and IDAT, so writing it directly after IHDR is
	// always valid.
	if err := writeChunk(bw, chunkTypeiCCP, iccChunkData); err != nil {
		return err
	}

	seenIDAT := false
	seenICC := false

	for {
		ch, err := readChunkHeaderForWrite(br)
		if err != nil {
			return err
		}

		switch ch.ChunkType {

		case chunkTypeiCCP:
			if options.ConflictPolicy == RejectConflicts {
				return fmt.Errorf("%w: source already contains an iCCP chunk", ErrColorSpaceConflict)
			}
			// The source profile is superseded by the one being written, but
			// more than one in the source is ambiguous.
			if err := skipChunk(br, ch.Length); err != nil {
				return err
			}
			if seenICC {
				return ErrDuplicateICC
			}
			seenICC = true

		case chunkTypesRGB, chunkTypegAMA, chunkTypecHRM:
			if options.ConflictPolicy == RejectConflicts {
				return fmt.Errorf("%w: source contains a %v chunk", ErrColorSpaceConflict, ch.ChunkType)
			}
			if err := skipChunk(br, ch.Length); err != nil {
				return err
			}

		case chunkTypeIDAT:
			seenIDAT = true
			if err := copyChunk(bw, br, ch); err != nil {
				return err
			}

		case chunkTypeIEND:
			if err := copyChunk(bw, br, ch); err != nil {
				return err
			}

			if !seenIDAT {
				return fmt.Errorf("%w: PNG contains no image data", meta.ErrEmptyImage)
			}

			// Preserve any trailing bytes after IEND verbatim.
			if _, err := io.Copy(bw, br); err != nil {
				return fmt.Errorf("copying PNG trailing data: %w", err)
			}
			if err := bw.Flush(); err != nil {
				return wrapWriteError(err)
			}
			return nil

		default:
			if err := copyChunk(bw, br, ch); err != nil {
				return err
			}
		}
	}
}

func readAndCheckSignature(r *bufio.Reader) error {
	header := [8]byte{}
	n, err := io.ReadFull(r, header[:])
	if err != nil {
		if n == 0 && errors.Is(err, io.EOF) {
			return meta.ErrEmptyImage
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("%w: reading PNG signature", meta.ErrTruncatedImage)
		}
		return err
	}
	if header != pngSignature {
		return fmt.Errorf("%w: invalid PNG signature", meta.ErrUnsupportedFormat)
	}
	return nil
}

// readIHDR reads and verifies the IHDR chunk, which must be first and always
// carries 13 bytes of data. The chunk data and CRC are returned as read so
// they can be re-emitted byte-for-byte.
func readIHDR(r *bufio.Reader) ([]byte, error) {
	ch, err := readChunkHeaderForWrite(r)
	if err != nil {
		return nil, err
	}
	if ch.ChunkType != chunkTypeIHDR {
		return nil, fmt.Errorf("%w: first chunk is %v, expected IHDR", ErrInvalidPNG, ch.ChunkType)
	}
	if ch.Length != 13 {
		return nil, fmt.Errorf("%w: IHDR chunk length is %d, expected 13", ErrInvalidPNG, ch.Length)
	}

	raw := make([]byte, ch.Length+4)
	if _, err := io.ReadFull(r, raw); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("%w: incomplete IHDR chunk", meta.ErrTruncatedImage)
		}
		return nil, err
	}
	return raw, nil
}

func readChunkHeaderForWrite(r *bufio.Reader) (chunkHeader, error) {
	length, err := readU32Big(r)
	if err != nil {
		return chunkHeader{}, fmt.Errorf("%w: missing IEND chunk", meta.ErrTruncatedImage)
	}

	ch := chunkHeader{Length: length}
	if _, err := io.ReadFull(r, ch.ChunkType[:]); err != nil {
		return ch, fmt.Errorf("%w: incomplete chunk header", meta.ErrTruncatedImage)
	}
	if ch.Length > maxPNGChunkLength {
		return ch, fmt.Errorf("%w: chunk %v has invalid length %d", ErrInvalidPNG, ch.ChunkType, ch.Length)
	}
	return ch, nil
}

func readU32Big(r io.ByteReader) (uint32, error) {
	var value uint32
	for i := 0; i < 4; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		value = value<<8 | uint32(b)
	}
	return value, nil
}

func writeIHDR(dst *bufio.Writer, raw []byte) error {
	if _, err := dst.Write([]byte{0, 0, 0, 13, 'I', 'H', 'D', 'R'}); err != nil {
		return wrapWriteError(err)
	}
	if _, err := dst.Write(raw); err != nil {
		return wrapWriteError(err)
	}
	return nil
}

func copyChunk(dst *bufio.Writer, src io.Reader, ch chunkHeader) error {
	if _, err := dst.Write([]byte{
		byte(ch.Length >> 24), byte(ch.Length >> 16), byte(ch.Length >> 8), byte(ch.Length),
	}); err != nil {
		return wrapWriteError(err)
	}
	if _, err := dst.Write(ch.ChunkType[:]); err != nil {
		return wrapWriteError(err)
	}
	if _, err := io.CopyN(dst, src, int64(ch.Length)+4); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("%w: incomplete chunk data", meta.ErrTruncatedImage)
		}
		// CopyN does not distinguish source from destination failures, so
		// classify only known truncation errors; anything else is reported as
		// a copy failure with the original error preserved.
		return fmt.Errorf("copying PNG chunk %v: %w", ch.ChunkType, err)
	}
	return nil
}

func skipChunk(src io.Reader, length uint32) error {
	if _, err := io.CopyN(io.Discard, src, int64(length)+4); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("%w: incomplete chunk data", meta.ErrTruncatedImage)
		}
		return err
	}
	return nil
}

func writeChunk(dst io.Writer, chunkType [4]byte, data []byte) error {
	header := [8]byte{
		byte(len(data) >> 24), byte(len(data) >> 16), byte(len(data) >> 8), byte(len(data)),
		chunkType[0], chunkType[1], chunkType[2], chunkType[3],
	}

	if _, err := dst.Write(header[:]); err != nil {
		return wrapWriteError(err)
	}
	if _, err := dst.Write(data); err != nil {
		return wrapWriteError(err)
	}

	crc := crc32.NewIEEE()
	_, _ = crc.Write(chunkType[:])
	_, _ = crc.Write(data)

	var crcBytes [4]byte
	crcValue := crc.Sum32()
	crcBytes[0] = byte(crcValue >> 24)
	crcBytes[1] = byte(crcValue >> 16)
	crcBytes[2] = byte(crcValue >> 8)
	crcBytes[3] = byte(crcValue)
	if _, err := dst.Write(crcBytes[:]); err != nil {
		return wrapWriteError(err)
	}

	return nil
}

func compressICCProfile(profileData []byte, name string) ([]byte, error) {
	if len(name) == 0 || len(name) > 79 {
		return nil, fmt.Errorf("invalid ICC profile name length %d", len(name))
	}

	compressed := &bytes.Buffer{}
	zw := zlib.NewWriter(compressed)
	if _, err := zw.Write(profileData); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}

	data := make([]byte, 0, len(name)+2+compressed.Len())
	data = append(data, name...)
	data = append(data, 0x00, 0x00) // name terminator, compression method 0
	data = append(data, compressed.Bytes()...)

	if len(data) > maxPNGChunkLength {
		return nil, fmt.Errorf("%w: ICC profile chunk is too large", icc.ErrInvalidProfile)
	}

	return data, nil
}

// wrapWriteError annotates an error from writing to dst. The original error is
// preserved (wrapped) so callers can inspect it, for example with
// errors.Is(err, fs.ErrPermission).
func wrapWriteError(err error) error {
	return fmt.Errorf("writing PNG: %w", err)
}
