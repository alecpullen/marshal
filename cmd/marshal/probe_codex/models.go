//go:build probe_c

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// probeModels answers D5 empirically: what model list can a runtime actually
// get? It tries, in order:
//
//	(a) GET {codexChatBaseURL}/models?client_version=<v>  (the endpoint codex
//	    itself uses — source notes §5)
//	(b) GET {codexChatBaseURL}/models with no query string (a guess, in case
//	    the version parameter is what fails)
//	(c) the codex CLI's own on-disk catalog cache at
//	    $CODEX_HOME/models_cache.json (default ~/.codex/models_cache.json)
//
// Every attempt is dumped verbatim; the findings doc quotes whichever worked.
func probeModels() error {
	if err := codexRequired("codexChatBaseURL", codexChatBaseURL); err != nil {
		return err
	}
	tok, err := accessToken()
	if err != nil {
		return err
	}
	accountID := accountIDFromToken()

	url := codexChatBaseURL + codexModelsPath
	attempts := []struct {
		label string
		url   string
	}{
		{"(a) codex models endpoint with client_version", url + "?client_version=" + probeClientVersion},
		{"(b) codex models endpoint without query", url},
	}

	worked := false
	for _, attempt := range attempts {
		fmt.Printf("=== %s ===\n", attempt.label)
		fmt.Printf("GET %s\n", attempt.url)

		ctx, cancel := context.WithTimeout(context.Background(), spikeTimeout)
		req, err := authedRequest(ctx, tok, accountID, http.MethodGet, attempt.url, nil)
		if err != nil {
			cancel()
			return err
		}
		// A GET has no body; the SSE accept header is meaningless here.
		req.Header.Set("Accept", "application/json")

		resp, err := newSpikeClient().Do(req)
		if err != nil {
			cancel()
			fmt.Printf("transport error: %v\n\n", err)
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()

		fmt.Printf("HTTP %d\n", resp.StatusCode)
		printHeaders(os.Stdout, resp.Header)
		fmt.Println("--- body ---")
		fmt.Println(string(raw))
		fmt.Println()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			worked = true
			printModelSlugs(raw)
			break
		}
	}

	if !worked {
		fmt.Println("=== (c) codex CLI on-disk model cache ===")
		if err := dumpCodexModelsCache(); err != nil {
			fmt.Printf("cache read failed: %v\n", err)
		}
	}
	return nil
}

// probeClientVersion is the client_version query value codex sends. Codex
// derives it from its own crate version (models-manager/src/lib.rs:19-26);
// the probe has no equivalent, so it sends a plausible pinned value and
// records whatever the endpoint says.
const probeClientVersion = "0.0.0"

// printModelSlugs extracts and prints the model slugs from a /models
// response, tolerating both {"models": [...]} and a bare array.
func printModelSlugs(raw []byte) {
	var envelope struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Models) > 0 {
		fmt.Printf("model count: %d\n", len(envelope.Models))
		for _, m := range envelope.Models {
			fmt.Printf("  %s  (%s)\n", m.Slug, m.DisplayName)
		}
		return
	}
	var bare []struct {
		Slug string `json:"slug"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(raw, &bare); err == nil && len(bare) > 0 {
		fmt.Printf("model count: %d\n", len(bare))
		for _, m := range bare {
			slug := m.Slug
			if slug == "" {
				slug = m.ID
			}
			fmt.Printf("  %s\n", slug)
		}
		return
	}
	fmt.Println("(response did not parse as a model list)")
}

// dumpCodexModelsCache reads the codex CLI's own catalog cache. It is a
// read-only observation of what codex itself uses; the probe never writes it.
func dumpCodexModelsCache() error {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home dir: %w", err)
		}
		home = filepath.Join(userHome, ".codex")
	}
	path := filepath.Join(home, codexModelsCacheFile)
	fmt.Printf("path: %s\n", path)

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	fmt.Printf("size: %d bytes\n", len(raw))

	var entry struct {
		FetchedAt     string `json:"fetched_at"`
		ClientVersion string `json:"client_version"`
		Models        []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		fmt.Println("--- raw body ---")
		fmt.Println(string(raw))
		return nil
	}
	fmt.Printf("fetched_at: %s\n", entry.FetchedAt)
	fmt.Printf("client_version: %s\n", entry.ClientVersion)
	fmt.Printf("model count: %d\n", len(entry.Models))
	for _, m := range entry.Models {
		fmt.Printf("  %s  (%s)\n", m.Slug, m.DisplayName)
	}
	return nil
}
