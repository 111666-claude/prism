package autometa

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"testing"

	"github.com/mandykoh/prism/meta"
	"github.com/mandykoh/prism/meta/icc"
	"github.com/mandykoh/prism/meta/pngmeta"
)

const testProfilePath = "../../test-profiles/display-p3-v4-with-v2-desc.icc"

func readTestProfile(t *testing.T) []byte {
	t.Helper()

	profile, err := os.ReadFile(testProfilePath)
	if err != nil {
		t.Fatalf("Reading test profile: %v", err)
	}
	return profile
}

func readTestPNG(t *testing.T) []byte {
	t.Helper()

	data, err := os.ReadFile("../../test-images/checkerboard-srgb.png")
	if err != nil {
		t.Fatalf("Reading test PNG: %v", err)
	}
	return data
}

func readTestJPEG(t *testing.T) []byte {
	t.Helper()

	data, err := os.ReadFile("../../test-images/pizza-rgb8-srgb.jpg")
	if err != nil {
		t.Fatalf("Reading test JPEG: %v", err)
	}
	return data
}

func TestWriteICCProfile(t *testing.T) {
	profile := readTestProfile(t)

	t.Run("detects PNG and round-trips the profile", func(t *testing.T) {
		src := readTestPNG(t)

		out := &bytes.Buffer{}
		if err := WriteICCProfile(out, bytes.NewReader(src), profile); err != nil {
			t.Fatalf("WriteICCProfile: %v", err)
		}

		md, imgStream, err := Load(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatalf("Loading output: %v", err)
		}
		if expected, actual := pngmeta.Format, md.Format; expected != actual {
			t.Errorf("Expected format %v but got %v", expected, actual)
		}

		data, err := md.ICCProfileData()
		if err != nil || !bytes.Equal(profile, data) {
			t.Fatalf("Profile did not round-trip: %v", err)
		}

		embeddedProfile, err := md.ICCProfile()
		if err != nil {
			t.Fatalf("Reading embedded profile: %v", err)
		}
		description, err := embeddedProfile.Description()
		if err != nil {
			t.Fatalf("Reading profile description: %v", err)
		}
		if expected, actual := "Display P3", description; expected != actual {
			t.Errorf("Expected description %q but got %q", expected, actual)
		}

		srcImg, err := png.Decode(bytes.NewReader(src))
		if err != nil {
			t.Fatalf("Decoding source: %v", err)
		}
		outImg, err := png.Decode(imgStream)
		if err != nil {
			t.Fatalf("Decoding output: %v", err)
		}
		assertSamePixels(t, srcImg, outImg)
	})

	t.Run("detects JPEG and round-trips the profile", func(t *testing.T) {
		src := readTestJPEG(t)

		out := &bytes.Buffer{}
		if err := WriteICCProfile(out, bytes.NewReader(src), profile); err != nil {
			t.Fatalf("WriteICCProfile: %v", err)
		}

		md, imgStream, err := Load(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatalf("Loading output: %v", err)
		}
		if expected, actual := "JPEG", string(md.Format); expected != actual {
			t.Errorf("Expected format %s but got %s", expected, actual)
		}

		data, err := md.ICCProfileData()
		if err != nil || !bytes.Equal(profile, data) {
			t.Fatalf("Profile did not round-trip: %v", err)
		}

		srcImg, err := jpeg.Decode(bytes.NewReader(src))
		if err != nil {
			t.Fatalf("Decoding source: %v", err)
		}
		outImg, err := jpeg.Decode(imgStream)
		if err != nil {
			t.Fatalf("Decoding output: %v", err)
		}
		assertSamePixels(t, srcImg, outImg)
	})

	t.Run("passes the conflict policy through to the PNG writer", func(t *testing.T) {
		src := readTestPNG(t)

		err := WriteICCProfileWithOptions(io.Discard, bytes.NewReader(src), profile, WriteOptions{
			PNGConflictPolicy: pngmeta.RejectConflicts,
		})
		if !errors.Is(err, pngmeta.ErrColorSpaceConflict) {
			t.Fatalf("Expected pngmeta.ErrColorSpaceConflict but got %v", err)
		}
	})

	t.Run("rejects unsupported formats", func(t *testing.T) {
		src := []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")

		err := WriteICCProfile(io.Discard, bytes.NewReader(src), profile)
		if !errors.Is(err, meta.ErrUnsupportedFormat) {
			t.Fatalf("Expected meta.ErrUnsupportedFormat but got %v", err)
		}
	})

	t.Run("rejects an empty stream", func(t *testing.T) {
		err := WriteICCProfile(io.Discard, bytes.NewReader(nil), profile)
		if !errors.Is(err, meta.ErrEmptyImage) {
			t.Fatalf("Expected meta.ErrEmptyImage but got %v", err)
		}
	})

	t.Run("rejects an invalid profile before reading the image", func(t *testing.T) {
		err := WriteICCProfile(io.Discard, bytes.NewReader([]byte("not an image")), []byte("not a profile"))
		if !errors.Is(err, icc.ErrInvalidProfile) {
			t.Fatalf("Expected icc.ErrInvalidProfile but got %v", err)
		}
	})

	t.Run("rejects a truncated stream", func(t *testing.T) {
		src := readTestPNG(t)

		err := WriteICCProfile(io.Discard, bytes.NewReader(src[:len(src)/2]), profile)
		if !errors.Is(err, meta.ErrTruncatedImage) {
			t.Fatalf("Expected meta.ErrTruncatedImage but got %v", err)
		}
	})
}

func assertSamePixels(t *testing.T, expected, actual image.Image) {
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
