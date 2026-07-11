//go:build !windows

package main

func maybeOfferPetCertificateTrust(root string) error { return nil }

func confirmCertificateTrust(subject, thumbprint string) bool { return false }
