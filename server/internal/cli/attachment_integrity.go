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
	if fmt.Sprintf("%x", sha256.Sum256(data)) != checksum {
		return fmt.Errorf("attachment sha256 mismatch; downloaded bytes discarded")
	}
	expected, err := strconv.ParseInt(fmt.Sprint(size), 10, 64)
	if err != nil || expected != int64(len(data)) {
		return fmt.Errorf("attachment size mismatch; downloaded bytes discarded")
	}
	return nil
}
