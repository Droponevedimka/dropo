package dropocore

import (
	"fmt"
	"io"
)

const (
	maxAndroidSubscriptionBytes int64 = 8 << 20
	maxAndroidMetadataBytes     int64 = 2 << 20
)

func readHTTPBodyLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("invalid HTTP response limit: %d", maxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("HTTP response exceeds %d bytes", maxBytes)
	}
	return body, nil
}
