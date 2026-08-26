//go:build !darwin

package creds

// newKeychainBackend is macOS-only; on other platforms there is no Keychain, so
// the file backend is always used. This stub lets backend.go compile everywhere.
func newKeychainBackend() credBackend { return nil }
