package prism_test

import (
	"bytes"
	"fmt"
	"os"

	"github.com/mandykoh/prism"
	"github.com/mandykoh/prism/meta/autometa"
)

func ExampleWriteICCProfile() {
	inFile, err := os.Open("test-images/checkerboard-srgb.png")
	if err != nil {
		panic(err)
	}
	defer inFile.Close()

	profile, err := os.ReadFile("test-profiles/display-p3-v4-with-v2-desc.icc")
	if err != nil {
		panic(err)
	}

	out := &bytes.Buffer{}
	if err := prism.WriteICCProfile(out, inFile, profile); err != nil {
		panic(err)
	}

	md, _, err := autometa.Load(out)
	if err != nil {
		panic(err)
	}

	embedded, err := md.ICCProfile()
	if err != nil {
		panic(err)
	}
	description, err := embedded.Description()
	if err != nil {
		panic(err)
	}

	embeddedData, err := md.ICCProfileData()
	if err != nil {
		panic(err)
	}

	fmt.Printf("Format: %s\n", md.Format)
	fmt.Printf("Description: %s\n", description)
	fmt.Printf("Profile bytes match source: %v\n", bytes.Equal(profile, embeddedData))

	// Output:
	// Format: PNG
	// Description: Display P3
	// Profile bytes match source: true
}
