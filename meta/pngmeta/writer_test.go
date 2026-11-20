package pngmeta

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/mandykoh/prism/meta"
	"github.com/mandykoh/prism/meta/icc"
)

const (
	testPNGPath        = "../../test-images/checkerboard-srgb.png"
	testICCProfilePath = "../../test-profiles/display-p3-v4-with-v2-desc.icc"
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

	return readTestFile(t, testICCProfilePath)
}

type pngChunk struct {
	chunkType [4]byte
	data      []byte
}

func parsePNGChunks(t *testing.T, data []byte) []pngChunk {
	t.Helper()

	if !bytes.HasPrefix(data, pngSignature[:]) {
		t.Fatalf("Output is not a PNG")
	}

	var chunks []pngChunk
	offset := len(pngSignature)
	for offset < len(data) {
		if offset+8 > len(data) {
			t.Fatalf("Truncated chunk header at offset %d", offset)
		}
		length := binary.BigEndian.Uint32(data[offset : offset+4])
		chunk := pngChunk{}
		copy(chunk.chunkType[:], data[offset+4:offset+8])
		end := offset + 8 + int(length) + 4
		if end > len(data) {
			t.Fatalf("Truncated chunk %v at offset %d", chunk.chunkType, offset)
		}
		chunk.data = data[offset+8 : offset+8+int(length)]

		expectedCRC := crc32.ChecksumIEEE(data[offset+4 : offset+8+int(length)])
		if actual := binary.BigEndian.Uint32(data[offset+8+int(length) : end]); actual != expectedCRC {
			t.Fatalf("Invalid CRC for chunk %v", chunk.chunkType)
		}

		chunks = append(chunks, chunk)
		offset = end
	}

	return chunks
}

func decompressICCChunk(t *testing.T, chunk pngChunk) (name string, profileData []byte) {
	t.Helper()

	nullIndex := bytes.IndexByte(chunk.data, 0x00)
	if nullIndex < 0 {
		t.Fatalf("ICC chunk has no null-terminated name")
	}
	name = string(chunk.data[:nullIndex])
	if chunk.data[nullIndex+1] != 0x00 {
		t.Fatalf("ICC chunk has unsupported compression method %d", chunk.data[nullIndex+1])
	}

	zReader, err := zlib.NewReader(bytes.NewReader(chunk.data[nullIndex+2:]))
	if err != nil {
		t.Fatalf("Opening compressed ICC data: %v", err)
	}
	profileData, err = io.ReadAll(zReader)
	if err != nil {
		t.Fatalf("Decompressing ICC data: %v", err)
	}
	_ = zReader.Close()

	return name, profileData
}

func decodePNG(t *testing.T, data []byte) image.Image {
	t.Helper()

	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decoding PN output: %v", err)
	}
	return img
}

func countChunks(chunks []pngChunk, chunkType [4]byte) int {
	n := 0
	for _, c := range chunks {
		if c.chunkType == chunkType {
			n++
		}
	}
	return n
}

func TestWriteICCProfile(t *testing.T) {
	t.Run("round-trips profile bytes and preserves pixels and metadata", func(t *testing.T) {
		src := readTestFile(t, testPNGPath)
		profile := readTestProfile(t)

		out := &bytes.Buffer{}
		if err := WriteICCProfile(out, bytes.NewReader(src), profile); err != nil {
			t.Fatalf("WriteICCProfile: %v", err)
		}

		// The profile read back must be byte-for-byte identical.
		md, imgStream, err := Load(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatalf("Loading metadata from output: %v", err)
		}
		gotProfile, err := md.ICCProfileData()
		if err != nil {
			t.Fatalf("Extracting ICC profile: %v", err)
		}
		if !bytes.Equal(profile, gotProfile) {
			t.Fatalf("ICC profile round-trip mismatch (%d bytes in, %d bytes out)", len(profile), len(gotProfile))
		}

		// The decoded pixels must be unchanged.
		srcImg := decodePNG(t, src)
		outImg, err := png.Decode(imgStream)
		if err != nil {
			t.Fatalf("Decoding output: %v", err)
		}
		if !srcImg.Bounds().Eq(outImg.Bounds()) {
			t.Fatalf("Image bounds changed: %v vs %v", srcImg.Bounds(), outImg.Bounds())
		}
		for y := srcImg.Bounds().Min.Y; y < srcImg.Bounds().Max.Y; y++ {
			for x := srcImg.Bounds().Min.X; x < srcImg.Bounds().Max.X; x++ {
				if expected, actual := srcImg.At(x, y), outImg.At(x, y); expected != actual {
					t.Fatalf("Pixel (%d, %d) changed: %v vs %v", x, y, expected, actual)
				}
			}
		}

		// Metadata dimensions must be unchanged.
		if expected, actual := uint32(64), md.PixelWidth; actual != expected {
			t.Errorf("Expected width %d but got %d", expected, actual)
		}
		if expected, actual := uint32(64), md.PixelHeight; actual != expected {
			t.Errorf("Expected height %d but got %d", expected, actual)
		}

		// Unrelated ancillary chunks must be preserved.
		srcChunks := parsePNGChunks(t, src)
		outChunks := parsePNGChunks(t, out.Bytes())
		iTXtType := [4]byte{'i', 'T', 'X', 't'}
		if countChunks(srcChunks, iTXtType) != 1 || countChunks(outChunks, iTXtType) != 1 {
			t.Fatalf("Expected exactly one iTXt chunk in both streams")
		}
		findChunkData := func(chunks []pngChunk, chunkType [4]byte) []byte {
			for _, c := range chunks {
				if c.chunkType == chunkType {
					return c.data
				}
			}
			return nil
		}
		if !bytes.Equal(findChunkData(srcChunks, iTXtType), findChunkData(outChunks, iTXtType)) {
			t.Errorf("iTXt metadata was modified")
		}
		pHYsType := [4]byte{'p', 'H', 'Y', 's'}
		if countChunks(outChunks, pHYsType) != countChunks(srcChunks, pHYsType) {
			t.Errorf("pHYs chunk count changed")
		} else if !bytes.Equal(findChunkData(srcChunks, pHYsType), findChunkData(outChunks, pHYsType)) {
			t.Errorf("pHYs metadata was modified")
		}

		// The embedded profile must be parseable as a profile.
		if _, err := md.ICCProfile(); err != nil {
			t.Errorf("Parsing embedded profile: %v", err)
		}
	})

	t.Run("preserves the pixel data bytes themselves", func(t *testing.T) {
		src := readTestFile(t, testPNGPath)
		out := &bytes.Buffer{}
		if err := WriteICCProfile(out, bytes.NewReader(src), readTestProfile(t)); err != nil {
			t.Fatalf("WriteICCProfile: %v", err)
		}

		idat := func(chunks []pngChunk) []byte {
			var combined []byte
			chunkType := [4]byte{'I', 'D', 'A', 'T'}
			for _, c := range chunks {
				if c.chunkType == chunkType {
					combined = append(combined, c.data...)
				}
			}
			return combined
		}

		if expected, actual := idat(parsePNGChunks(t, src)), idat(parsePNGChunks(t, out.Bytes())); !bytes.Equal(expected, actual) {
			t.Fatalf("Compressed image data changed")
		}
	})

	t.Run("replaces existing iCCP and removes sRGB/gAMA/cHRM", func(t *testing.T) {
		src := readTestFile(t, testPNGPath)
		profile := readTestProfile(t)

		out := &bytes.Buffer{}
		if err := WriteICCProfile(out, bytes.NewReader(src), profile); err != nil {
			t.Fatalf("WriteICCProfile: %v", err)
		}

		chunks := parsePNGChunks(t, out.Bytes())
		iCCPType := [4]byte{'i', 'C', 'C', 'P'}
		if expected, actual := 1, countChunks(chunks, iCCPType); expected != actual {
			t.Fatalf("Expected %d iCCP chunk but got %d", expected, actual)
		}
		for _, conflicting := range [][4]byte{{'s', 'R', 'G', 'B'}, {'g', 'A', 'M', 'A'}, {'c', 'H', 'R', 'M'}} {
			if countChunks(chunks, conflicting) != 0 {
				t.Errorf("Expected no %v chunk in output", conflicting)
			}
		}

		// iCCP must be placed before IDAT.
		idatType := [4]byte{'I', 'D', 'A', 'T'}
		iccPos, idatPos := -1, -1
		for i, c := range chunks {
			if c.chunkType == iCCPType {
				iccPos = i
			}
			if c.chunkType == idatType && idatPos < 0 {
				idatPos = i
			}
		}
		if iccPos < 0 || iccPos > idatPos {
			t.Errorf("iCCP chunk is not before IDAT")
		}

		_, roundTripped := decompressICCChunk(t, chunks[iccPos])
		if !bytes.Equal(profile, roundTripped) {
			t.Errorf("iCCP chunk does not contain the supplied profile")
		}
	})

	t.Run("RejectConflicts refuses a stream with a colour space declaration", func(t *testing.T) {
		src := readTestFile(t, testPNGPath)

		err := WriteICCProfileWithOptions(io.Discard, bytes.NewReader(src), readTestProfile(t), WriteOptions{
			ConflictPolicy: RejectConflicts,
		})
		if !errors.Is(err, ErrColorSpaceConflict) {
			t.Fatalf("Expected ErrColorSpaceConflict but got %v", err)
		}
	})

	t.Run("RejectConflicts accepts a stream without colour space declarations", func(t *testing.T) {
		img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
		img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
		img.SetNRGBA(2, 1, color.NRGBA{B: 255, A: 255})
		src := &bytes.Buffer{}
		if err := png.Encode(src, img); err != nil {
			t.Fatalf("Encoding source: %v", err)
		}

		out := &bytes.Buffer{}
		if err := WriteICCProfileWithOptions(out, src, readTestProfile(t), WriteOptions{
			ConflictPolicy: RejectConflicts,
		}); err != nil {
			t.Fatalf("WriteICCProfileWithOptions: %v", err)
		}

		md, _, err := Load(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatalf("Loading metadata: %v", err)
		}
		if data, err := md.ICCProfileData(); err != nil || !bytes.Equal(data, readTestProfile(t)) {
			t.Errorf("Profile did not round-trip: %v", err)
		}
	})

	t.Run("RejectConflicts refuses a stream with only a gAMA chunk", func(t *testing.T) {
		src := makePNGWithChunk(t, chunkTypegAMA, []byte{0, 0, 0, 1})

		err := WriteICCProfileWithOptions(io.Discard, src, readTestProfile(t), WriteOptions{
			ConflictPolicy: RejectConflicts,
		})
		if !errors.Is(err, ErrColorSpaceConflict) {
			t.Fatalf("Expected ErrColorSpaceConflict but got %v", err)
		}
	})

	t.Run("ReplaceConflicts drops gAMA and sRGB chunks", func(t *testing.T) {
		src := makePNGWithChunk(t, chunkTypegAMA, []byte{0, 0, 0, 1})

		out := &bytes.Buffer{}
		if err := WriteICCProfile(out, src, readTestProfile(t)); err != nil {
			t.Fatalf("WriteICCProfile: %v", err)
		}
		if bytes.Contains(out.Bytes(), chunkTypegAMA[:]) {
			t.Errorf("gAMA chunk was not removed")
		}
	})

	t.Run("rejects an oversized chunk length", func(t *testing.T) {
		src := &bytes.Buffer{}
		src.Write(pngSignature[:])
		src.Write([]byte{0x7F, 0xFF, 0xFF, 0xFF})
		src.Write(chunkTypeIHDR[:])

		err := WriteICCProfile(io.Discard, src, readTestProfile(t))
		if !errors.Is(err, ErrInvalidPNG) {
			t.Fatalf("Expected ErrInvalidPNG but got %v", err)
		}
	})

	t.Run("rejects an unknown conflict policy", func(t *testing.T) {
		err := WriteICCProfileWithOptions(io.Discard, bytes.NewReader(readTestFile(t, testPNGPath)), readTestProfile(t), WriteOptions{
			ConflictPolicy: ConflictPolicy(9),
		})
		if !errors.Is(err, ErrInvalidPNG) {
			t.Fatalf("Expected ErrInvalidPNG but got %v", err)
		}
	})

	t.Run("writes into an image that had no profile", func(t *testing.T) {
		img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
		img.SetNRGBA(1, 1, color.NRGBA{G: 255, A: 255})
		src := &bytes.Buffer{}
		if err := png.Encode(src, img); err != nil {
			t.Fatalf("Encoding source: %v", err)
		}
		profile := readTestProfile(t)

		out := &bytes.Buffer{}
		if err := WriteICCProfile(out, src, profile); err != nil {
			t.Fatalf("WriteICCProfile: %v", err)
		}

		md, imgStream, err := Load(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatalf("Loading metadata: %v", err)
		}
		data, err := md.ICCProfileData()
		if err != nil || !bytes.Equal(profile, data) {
			t.Fatalf("Profile did not round-trip: %v", err)
		}

		decoded, err := png.Decode(imgStream)
		if err != nil {
			t.Fatalf("Decoding output: %v", err)
		}
		if expected, actual := img.NRGBAAt(1, 1), decoded.(*image.NRGBA).NRGBAAt(1, 1); expected != actual {
			t.Errorf("Pixel changed: %v vs %v", expected, actual)
		}
	})

	t.Run("returns ErrDuplicateICC for duplicate iCCP chunks", func(t *testing.T) {
		img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		src := &bytes.Buffer{}
		if err := png.Encode(src, img); err != nil {
			t.Fatalf("Encoding source: %v", err)
		}

		// Insert two iCCP chunks immediately after IHDR.
		chunks := parsePNGChunks(t, src.Bytes())
		iccData, err := compressICCProfile(readTestProfile(t), defaultICCProfileName)
		if err != nil {
			t.Fatalf("Compressing profile: %v", err)
		}
		withDuplicates := &bytes.Buffer{}
		withDuplicates.Write(pngSignature[:])
		for i, c := range chunks {
			writeTestChunk(t, withDuplicates, c.chunkType, c.data)
			if i == 0 {
				writeTestChunk(t, withDuplicates, chunkTypeiCCP, iccData)
				writeTestChunk(t, withDuplicates, chunkTypeiCCP, iccData)
			}
		}

		err = WriteICCProfile(io.Discard, bytes.NewReader(withDuplicates.Bytes()), readTestProfile(t))
		if !errors.Is(err, ErrDuplicateICC) {
			t.Fatalf("Expected ErrDuplicateICC but got %v", err)
		}
	})

	t.Run("rejects an invalid profile", func(t *testing.T) {
		src := readTestFile(t, testPNGPath)

		for name, profile := range map[string][]byte{
			"empty":   nil,
			"garbage": []byte("this is not an ICC profile"),
			"wrong signature": func() []byte {
				p := append([]byte(nil), readTestProfile(t)...)
				p[36] = 'x'
				return p
			}(),
			"declared size too large": func() []byte {
				p := append([]byte(nil), readTestProfile(t)...)
				binary.BigEndian.PutUint32(p[:4], uint32(len(p)+1))
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

	t.Run("rejects an empty image", func(t *testing.T) {
		err := WriteICCProfile(io.Discard, bytes.NewReader(nil), readTestProfile(t))
		if !errors.Is(err, meta.ErrEmptyImage) {
			t.Fatalf("Expected meta.ErrEmptyImage but got %v", err)
		}
	})

	t.Run("rejects an image with no IDAT", func(t *testing.T) {
		img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		src := &bytes.Buffer{}
		if err := png.Encode(src, img); err != nil {
			t.Fatalf("Encoding source: %v", err)
		}
		chunks := parsePNGChunks(t, src.Bytes())

		withoutIDAT := &bytes.Buffer{}
		withoutIDAT.Write(pngSignature[:])
		for _, c := range chunks {
			idatType := [4]byte{'I', 'D', 'A', 'T'}
			if c.chunkType != idatType {
				writeTestChunk(t, withoutIDAT, c.chunkType, c.data)
			}
		}

		err := WriteICCProfile(io.Discard, bytes.NewReader(withoutIDAT.Bytes()), readTestProfile(t))
		if !errors.Is(err, meta.ErrEmptyImage) {
			t.Fatalf("Expected meta.ErrEmptyImage but got %v", err)
		}
	})

	t.Run("rejects a truncated stream", func(t *testing.T) {
		src := readTestFile(t, testPNGPath)

		for _, cut := range []int{
			len(pngSignature) + 10, // Partway through IHDR
			len(pngSignature) + 25, // Partway through chunk data
			len(src) - 20,          // Partway through IEND
			len(src) - 1,
		} {
			err := WriteICCProfile(io.Discard, bytes.NewReader(src[:cut]), readTestProfile(t))
			if !errors.Is(err, meta.ErrTruncatedImage) {
				t.Errorf("cut at %d: expected meta.ErrTruncatedImage but got %v", cut, err)
			}
		}
	})

	t.Run("rejects a non-PNG stream", func(t *testing.T) {
		err := WriteICCProfile(io.Discard, bytes.NewReader([]byte("GIF89a not a png")), readTestProfile(t))
		if !errors.Is(err, meta.ErrUnsupportedFormat) {
			t.Fatalf("Expected meta.ErrUnsupportedFormat but got %v", err)
		}
	})

	t.Run("rejects a stream with no IHDR first", func(t *testing.T) {
		src := &bytes.Buffer{}
		src.Write(pngSignature[:])
		writeTestChunk(t, src, chunkTypeIDAT, []byte{1, 2, 3})

		err := WriteICCProfile(io.Discard, src, readTestProfile(t))
		if !errors.Is(err, ErrInvalidPNG) {
			t.Fatalf("Expected ErrInvalidPNG but got %v", err)
		}
	})

	t.Run("returns an error when dst is not writable", func(t *testing.T) {
		src := readTestFile(t, testPNGPath)

		err := WriteICCProfile(failingWriter{}, bytes.NewReader(src), readTestProfile(t))
		if err == nil {
			t.Fatalf("Expected an error but succeeded")
		}
		if !errors.Is(err, errTestWrite) {
			t.Errorf("Expected the write error to be preserved but got %v", err)
		}
	})

	t.Run("returns a write error when the output is read-only", func(t *testing.T) {
		src := readTestFile(t, testPNGPath)

		// A file opened read-only cannot be written to through its handle.
		path := filepath.Join(t.TempDir(), "readonly.png")
		if err := os.WriteFile(path, nil, 0o444); err != nil {
			t.Fatalf("Creating read-only file: %v", err)
		}
		readOnlyFile, err := os.Open(path)
		if err != nil {
			t.Fatalf("Opening read-only file: %v", err)
		}
		defer readOnlyFile.Close()

		err = WriteICCProfile(readOnlyFile, bytes.NewReader(src), readTestProfile(t))
		if err == nil {
			t.Fatalf("Expected a write error but succeeded")
		}
		if !errors.Is(err, syscall.EBADF) && !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("Expected a permission-style write error but got %v", err)
		}
	})

	t.Run("preserves bytes after IEND", func(t *testing.T) {
		src := append(readTestFile(t, testPNGPath), []byte("TRAILING")...)

		out := &bytes.Buffer{}
		if err := WriteICCProfile(out, bytes.NewReader(src), readTestProfile(t)); err != nil {
			t.Fatalf("WriteICCProfile: %v", err)
		}

		if !bytes.HasSuffix(out.Bytes(), []byte("TRAILING")) {
			t.Errorf("Trailing data after IEND was not preserved")
		}
	})
}

// makePNGWithChunk builds a minimal structurally valid PNG (IHDR, the supplied
// chunk, a dummy IDAT, and IEND) without colour space declarations.
func makePNGWithChunk(t *testing.T, chunkType [4]byte, data []byte) *bytes.Buffer {
	t.Helper()

	src := &bytes.Buffer{}
	src.Write(pngSignature[:])
	writeTestChunk(t, src, chunkTypeIHDR, make([]byte, 13))
	writeTestChunk(t, src, chunkType, data)
	writeTestChunk(t, src, chunkTypeIDAT, []byte{1, 2, 3})
	writeTestChunk(t, src, chunkTypeIEND, nil)
	return src
}

var errTestWrite = errors.New("test write failure")

type failingWriter struct{}

func (failingWriter) Write(p []byte) (int, error) {
	return 0, errTestWrite
}

func writeTestChunk(t *testing.T, dst *bytes.Buffer, chunkType [4]byte, data []byte) {
	t.Helper()

	_ = binary.Write(dst, binary.BigEndian, uint32(len(data)))
	dst.Write(chunkType[:])
	dst.Write(data)
	_ = binary.Write(dst, binary.BigEndian, crc32.ChecksumIEEE(append(append([]byte(nil), chunkType[:]...), data...)))
}
