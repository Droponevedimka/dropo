//go:build windows

package main

import (
	"crypto/sha1"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const certificatePromptRegistryPath = `Software\dropo`

func maybeOfferPetCertificateTrust(root string) error {
	path := petCertificatePath(root)
	cert, der, err := loadAndValidatePetCertificate(path)
	if err != nil {
		// Commercial builds do not ship the pet certificate. A missing file is
		// therefore not an installation failure and should not show a prompt.
		if !fileExistsForLauncher(path) {
			return nil
		}
		return err
	}
	thumbprint := sha1.Sum(der)
	trusted, err := certificateInCurrentUserStore("Root", thumbprint[:])
	if err != nil {
		return err
	}
	publisher, err := certificateInCurrentUserStore("TrustedPublisher", thumbprint[:])
	if err != nil {
		return err
	}
	if trusted && publisher {
		return nil
	}
	if certificatePromptWasAnswered() {
		return nil
	}
	if !confirmCertificateTrust(cert.Subject.String(), petCertificateSHA1) {
		markCertificatePromptAnswered()
		return nil
	}
	if err := addCertificateToCurrentUserStore("Root", der); err != nil {
		return err
	}
	if err := addCertificateToCurrentUserStore("TrustedPublisher", der); err != nil {
		return err
	}
	markCertificatePromptAnswered()
	return nil
}

func openCurrentUserCertificateStore(name string) (windows.Handle, error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	return windows.CertOpenStore(
		windows.CERT_STORE_PROV_SYSTEM,
		0,
		0,
		windows.CERT_SYSTEM_STORE_CURRENT_USER|windows.CERT_STORE_OPEN_EXISTING_FLAG,
		uintptr(unsafe.Pointer(namePtr)),
	)
}

func certificateInCurrentUserStore(name string, thumbprint []byte) (bool, error) {
	store, err := openCurrentUserCertificateStore(name)
	if err != nil {
		return false, err
	}
	defer windows.CertCloseStore(store, 0)
	blob := windows.BLOB{Size: uint32(len(thumbprint)), BlobData: &thumbprint[0]}
	ctx, err := windows.CertFindCertificateInStore(
		store,
		windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING,
		0,
		windows.CERT_FIND_SHA1_HASH,
		unsafe.Pointer(&blob),
		nil,
	)
	if err != nil {
		if err == syscall.Errno(windows.CRYPT_E_NOT_FOUND) {
			return false, nil
		}
		return false, err
	}
	defer windows.CertFreeCertificateContext(ctx)
	return true, nil
}

func addCertificateToCurrentUserStore(name string, der []byte) error {
	store, err := openCurrentUserCertificateStore(name)
	if err != nil {
		return err
	}
	defer windows.CertCloseStore(store, 0)
	ctx, err := windows.CertCreateCertificateContext(
		windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING,
		&der[0],
		uint32(len(der)),
	)
	if err != nil {
		return err
	}
	defer windows.CertFreeCertificateContext(ctx)
	if err := windows.CertAddCertificateContextToStore(store, ctx, windows.CERT_STORE_ADD_REPLACE_EXISTING, nil); err != nil {
		return fmt.Errorf("add certificate to CurrentUser/%s: %w", name, err)
	}
	return nil
}

func certificatePromptWasAnswered() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, certificatePromptRegistryPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	value, _, err := key.GetIntegerValue("CertificateTrustPrompted_" + petCertificateSHA1)
	return err == nil && value == 1
}

func markCertificatePromptAnswered() {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, certificatePromptRegistryPath, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer key.Close()
	_ = key.SetDWordValue("CertificateTrustPrompted_"+petCertificateSHA1, 1)
}

func fileExistsForLauncher(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
