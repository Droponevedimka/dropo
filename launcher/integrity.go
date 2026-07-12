package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

func verifyFileSHA256(path, expected string) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("ожидаемый SHA-256 отсутствует или имеет неверный формат")
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return fmt.Errorf("ожидаемый SHA-256 имеет неверный формат: %w", err)
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("SHA-256 не совпадает: получен %s", actual)
	}
	return nil
}
