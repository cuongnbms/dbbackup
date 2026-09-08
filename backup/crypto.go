package backup

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// EncryptFile writes filePath+".gpg" (symmetric AES256) and removes the plaintext.
func EncryptFile(filePath, passphrase string) error {
	cmd := exec.Command(
		"gpg", "--batch", "--yes", "--passphrase-fd", "0", "--symmetric", "--cipher-algo", "AES256",
		"--output", filePath+".gpg",
		filePath,
	)
	cmd.Stdin = strings.NewReader(passphrase)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("encrypt %s: %w: %s", filePath, err, output)
	}

	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("remove plaintext after encryption: %w", err)
	}
	return nil
}
