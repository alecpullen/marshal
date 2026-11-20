// Package tunnels keeps one SSH forward per fleet.toml host, from this
// machine's daemon directory to that host's daemon socket, and reports each
// one's state to the daemon (one-daemon D7).
package tunnels
