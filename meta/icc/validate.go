package icc

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// MaxProfileSize is the largest ICC profile that will be accepted for
	// embedding in an image. This bounds the amount of data written to, and
	// later decompressed from, an image container.
	MaxProfileSize = 16 << 20 // 16 MiB

	// minimumProfileSize is the size of the profile header (128 bytes) plus
	// the 4-byte tag count at the start of the tag table.
	minimumProfileSize = 132

	// profileFileSignatureOffset is the byte offset of the 'acsp' file
	// signature in an ICC profile header.
	profileFileSignatureOffset = 36
)

// ErrInvalidProfile is returned when ICC profile data is malformed or not a
// profile at all.
var ErrInvalidProfile = errors.New("invalid ICC profile")

// ValidateProfileData checks that data is plausibly an ICC profile that can be
// embedded verbatim in an image container.
//
// It checks the basic header invariants: non-empty, within MaxProfileSize, at
// least a header and tag count long, the correct 'acsp' file signature, and a
// declared profile size no larger than the data supplied. It deliberately does
// not attempt to parse the profile's tag data; callers that need a fully
// validated profile can additionally use NewProfileReader.
func ValidateProfileData(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("%w: profile data is empty", ErrInvalidProfile)
	}
	if len(data) > MaxProfileSize {
		return fmt.Errorf("%w: profile is %d bytes, maximum is %d", ErrInvalidProfile, len(data), MaxProfileSize)
	}
	if len(data) < minimumProfileSize {
		return fmt.Errorf("%w: profile is %d bytes, minimum is %d", ErrInvalidProfile, len(data), minimumProfileSize)
	}

	if got, want := string(data[profileFileSignatureOffset:profileFileSignatureOffset+4]), "acsp"; got != want {
		return fmt.Errorf("%w: file signature is %q, expected %q", ErrInvalidProfile, got, want)
	}

	declaredSize := binary.BigEndian.Uint32(data[:4])
	if declaredSize < minimumProfileSize {
		return fmt.Errorf("%w: declared profile size is %d bytes, minimum is %d", ErrInvalidProfile, declaredSize, minimumProfileSize)
	}
	if int64(declaredSize) > int64(len(data)) {
		return fmt.Errorf("%w: declared profile size is %d bytes but only %d bytes supplied", ErrInvalidProfile, declaredSize, len(data))
	}

	return nil
}
