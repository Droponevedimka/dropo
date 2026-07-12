package main

import (
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const petCertificateSHA1 = "C01E572C301010F06DC7F934A985691EE6C11096"

func petCertificatePath(root string) string {
	return filepath.Join(root, "resources", "cert", "dropo-pet-code-signing.cer")
}

func loadAndValidatePetCertificate(path string) (*x509.Certificate, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	der := data
	if block, _ := pem.Decode(data); block != nil {
		if block.Type != "CERTIFICATE" {
			return nil, nil, fmt.Errorf("unexpected PEM block %q", block.Type)
		}
		der = block.Bytes
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	digest := sha1.Sum(der)
	thumbprint := strings.ToUpper(hex.EncodeToString(digest[:]))
	if thumbprint != petCertificateSHA1 {
		return nil, nil, fmt.Errorf("certificate thumbprint mismatch: %s", thumbprint)
	}
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		return nil, nil, fmt.Errorf("certificate is not validly self-signed: %w", err)
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return nil, nil, fmt.Errorf("certificate is outside its validity period")
	}
	hasCodeSigning := false
	for _, usage := range cert.ExtKeyUsage {
		if usage == x509.ExtKeyUsageCodeSigning {
			hasCodeSigning = true
			break
		}
	}
	if !hasCodeSigning {
		return nil, nil, fmt.Errorf("certificate is not valid for code signing")
	}
	return cert, der, nil
}
