package cli

import (
	"crypto/sha256"
	"fmt"
	"strconv"
)

// VerifyAttachment is shared by CLI and MCP downloads before writing any bytes.
func VerifyAttachment(data []byte, checksum string, size any) error {
	if checksum == "" {
		return fmt.Errorf("attachment has no sha256; server must provide a verified checksum")
	}
	actual := fmt.Sprintf("%x", sha256.Sum256(data))
	expected, err := strconv.ParseInt(fmt.Sprint(size), 10, 64)
	if actual != checksum || err != nil || expected != int64(len(data)) {
		kind := "sha256"
		if actual == checksum {
			kind = "size"
		}
		return fmt.Errorf("attachment %s mismatch: expected sha256=%s size=%v; actual sha256=%s size=%d; downloaded bytes discarded", kind, checksum, size, actual, len(data))
	}
	return nil
}
