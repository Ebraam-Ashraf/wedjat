package daemon

import (
	"os"
	"strings"
)

// ReadBootID reads the system boot ID from /proc/sys/kernel/random/boot_id.
// The returned value has surrounding whitespace stripped.
func ReadBootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
