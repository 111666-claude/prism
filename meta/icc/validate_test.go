package icc

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

const testProfilePath = "../../test-profiles/display-p3-v4-with-v2-desc.icc"

func loadTestProfile(t *testing.T) []byte {
	t.Helper()

	profile, err := os.ReadFile(testProfilePath)
	if err != nil {
		t.Fatalf("Reading test profile: %v", err)
	}
	return profile
}

func TestValidateProfileData(t *testing.T) {
	t.Run("accepts a real profile", func(t *testing.T) {
		if err := ValidateProfileData(loadTestProfile(t)); err != nil {
			t.Fatalf("Expected success but got %v", err)
		}
	})

	t.Run("accepts a profile larger than its declared size", func(t *testing.T) {
		profile := append(loadTestProfile(t), 0, 0, 0, 0)
		if err := ValidateProfileData(profile); err != nil {
			t.Fatalf("Expected success but got %v", err)
		}
	})

	t.Run("rejects empty data", func(t *testing.T) {
		if err := ValidateProfileData(nil); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("Expected ErrInvalidProfile but got %v", err)
		}
	})

	t.Run("rejects data shorter than a header", func(t *testing.T) {
		profile := loadTestProfile(t)[:100]
		if err := ValidateProfileData(profile); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("Expected ErrInvalidProfile but got %v", err)
		}
	})

	t.Run("rejects data without the 'acsp' signature", func(t *testing.T) {
		profile := loadTestProfile(t)
		profile[36] = 'x'
		if err := ValidateProfileData(profile); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("Expected ErrInvalidProfile but got %v", err)
		}
	})

	t.Run("rejects a declared size larger than the data", func(t *testing.T) {
		profile := loadTestProfile(t)
		binary.BigEndian.PutUint32(profile[:4], uint32(len(profile))+1)
		if err := ValidateProfileData(profile); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("Expected ErrInvalidProfile but got %v", err)
		}
	})

	t.Run("rejects a declared size below the minimum", func(t *testing.T) {
		profile := loadTestProfile(t)
		binary.BigEndian.PutUint32(profile[:4], 4)
		if err := ValidateProfileData(profile); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("Expected ErrInvalidProfile but got %v", err)
		}
	})

	t.Run("rejects a profile above the maximum size", func(t *testing.T) {
		profile := make([]byte, MaxProfileSize+1)
		binary.BigEndian.PutUint32(profile[:4], uint32(len(profile)))
		copy(profile[36:], "acsp")
		if err := ValidateProfileData(profile); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("Expected ErrInvalidProfile but got %v", err)
		}
	})
}
