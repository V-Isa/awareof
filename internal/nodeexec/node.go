// Package nodeexec identifies direct native Node executables without running them.
package nodeexec

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Validate reports whether path names a native executable that can be invoked
// directly as Node on the current platform. It reads only the executable header.
func Validate(path string) error {
	return ValidateForPlatform(path, runtime.GOOS)
}

// ValidateForPlatform is Validate with an explicit platform for tests.
func ValidateForPlatform(path, goos string) error {
	base := strings.ToLower(filepath.Base(path))
	if (goos == "windows" && base != "node.exe") || (goos != "windows" && base != "node") {
		return fmt.Errorf("resolved path is %q, not a native Node entry point", path)
	}
	file, err := os.Open(path) //nolint:gosec // path is a discovered tool entry point.
	if err != nil {
		return err
	}
	var header [4]byte
	_, readErr := io.ReadFull(file, header[:])
	closeErr := file.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	valid := false
	switch goos {
	case "linux":
		valid = bytes.Equal(header[:], []byte{0x7f, 'E', 'L', 'F'})
	case "darwin":
		valid = bytes.Equal(header[:], []byte{0xfe, 0xed, 0xfa, 0xce}) ||
			bytes.Equal(header[:], []byte{0xce, 0xfa, 0xed, 0xfe}) ||
			bytes.Equal(header[:], []byte{0xfe, 0xed, 0xfa, 0xcf}) ||
			bytes.Equal(header[:], []byte{0xcf, 0xfa, 0xed, 0xfe}) ||
			bytes.Equal(header[:], []byte{0xca, 0xfe, 0xba, 0xbe}) ||
			bytes.Equal(header[:], []byte{0xbe, 0xba, 0xfe, 0xca})
	case "windows":
		valid = bytes.Equal(header[:2], []byte{'M', 'Z'})
	default:
		return fmt.Errorf("platform %q is not supported", goos)
	}
	if !valid {
		return fmt.Errorf("entry point is not a recognized native executable for %s", goos)
	}
	return nil
}
