package creds

// fileBackend keeps the secret VALUE inline in the proxy-credentials.json map —
// the original behavior. Because the value never leaves the JSON, the get/set/
// delete methods are no-ops: Load/WriteCredsMap read and write the value directly
// in the map. storesInline() reports true so those functions don't strip it.
type fileBackend struct{}

func (fileBackend) name() string { return "file" }

func (fileBackend) getValue(scope, host string) (string, bool, error) { return "", false, nil }

func (fileBackend) setValue(scope, host, value string) error { return nil }

func (fileBackend) deleteValue(scope, host string) error { return nil }

func (fileBackend) storesInline() bool { return true }
