package acp

import (
	"testing"

	"marshal/internal/acpclient"
)

// TestACPClientMethodTableMatchesTheServer keeps acpclient.Methods' Exists
// column honest in both directions: a row that claims the ACP method already
// exists must find a handler registered here, and a row that claims it is new
// must not (T4).
func TestACPClientMethodTableMatchesTheServer(t *testing.T) {
	handlers := registeredHandlers(t, false)

	for _, row := range acpclient.Methods {
		if row.ACP == "" {
			continue
		}
		_, registered := handlers[row.ACP]
		switch {
		case row.Exists && !registered:
			t.Errorf("acpclient.Methods marks %s.%s's %s as existing, but acp.Server does not register it",
				row.Iface, row.Name, row.ACP)
		case !row.Exists && registered:
			t.Errorf("acp.Server registers %s now: mark %s.%s as existing in acpclient.Methods",
				row.ACP, row.Iface, row.Name)
		}
	}
}
