package prism_test

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"testing"

	"github.com/mandykoh/prism"
	"github.com/mandykoh/prism/displayp3"
	"github.com/mandykoh/prism/meta/autometa"
)

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Reading %s: %v", path, err)
	}
	return data
}

func TestWriteICCProfilePreservesColour(t *testing.T) {
	profile := readFile(t, "test-profiles/display-p3-v4-with-v2-desc.icc")

	// Convert a decoded image to XYZ using the Display P3 encoding in the
	// profile. Identical values before and after embedding show that the
	// original colours are untouched.
	toXYZ := func(img image.Image) [][3]float32 {
		nrgbaImg := prism.ConvertImageToNRGBA(img, 1)
		bounds := nrgbaImg.Bounds()
		result := make([][3]float32, 0, bounds.Dx()*bounds.Dy())
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				rgba := nrgbaImg.NRGBAAt(x, y)
				c, _ := displayp3.ColorFromNRGBA(rgba)
				xyz := c.ToXYZ()
				result = append(result, [3]float32{xyz.X, xyz.Y, xyz.Z})
			}
		}
		return result
	}

	for _, testCase := range []struct {
		name   string
		path   string
		decode func(*testing.T, []byte) image.Image
	}{
		{
			name: "PNG fixture",
			path: "test-images/checkerboard-srgb.png",
			decode: func(t *testing.T, data []byte) image.Image {
				img, err := png.Decode(bytes.NewReader(data))
				if err != nil {
					t.Fatalf("Decoding PNG: %v", err)
				}
				return img
			},
		},
		{
			name: "JPEG fixture",
			path: "test-images/pizza-rgb8-displayp3.jpg",
			decode: func(t *testing.T, data []byte) image.Image {
				img, err := jpeg.Decode(bytes.NewReader(data))
				if err != nil {
					t.Fatalf("Decoding JPEG: %v", err)
				}
				return img
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			src := readFile(t, testCase.path)

			out := &bytes.Buffer{}
			if err := prism.WriteICCProfile(out, bytes.NewReader(src), profile); err != nil {
				t.Fatalf("WriteICCProfile: %v", err)
			}

			// The embedded profile must read back byte-for-byte and describe
			// the colour space actually used for the conversion.
			md, _, err := autometa.Load(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("Loading metadata: %v", err)
			}
			embedded, err := md.ICCProfile()
			if err != nil {
				t.Fatalf("Parsing embedded profile: %v", err)
			}
			if embedded.Header.DataColorSpace != 0x52474220 { // 'RGB '
				t.Errorf("Embedded profile is not an RGB profile")
			}
			if description, err := embedded.Description(); err != nil || description != "Display P3" {
				t.Errorf("Expected description \"Display P3\" but got %q (%v)", description, err)
			}

			embeddedData, err := md.ICCProfileData()
			if err != nil {
				t.Fatalf("Extracting embedded profile: %v", err)
			}
			if !bytes.Equal(profile, embeddedData) {
				t.Fatalf("Embedded profile bytes differ from the supplied profile")
			}

			expected := toXYZ(testCase.decode(t, src))
			actual := toXYZ(testCase.decode(t, out.Bytes()))
			if len(expected) != len(actual) {
				t.Fatalf("Pixel count changed: %d vs %d", len(expected), len(actual))
			}
			for i := range expected {
				if expected[i] != actual[i] {
					t.Fatalf("Colour at pixel %d changed: %v vs %v", i, expected[i], actual[i])
				}
			}
		})
	}
}
