package config

import "os"

func writeBytes(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}
