package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/strutil"
)

const (
	secretKey    = "sk-test-SECRET-1234"
	secretSearch = "search-SECRET-2345"
	secretCDP    = "ws://user:cdp-SECRET-pw@127.0.0.1:9222"
)

// secretConfig is a config carrying a literal value in each secret-bearing
// field: a provider's API key, the web search key and the desktop CDP URL's
// userinfo.
func secretConfig() config.Config {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"probe": {Type: "openai_compatible", BaseURL: "https://probe.test/v1", APIKey: secretKey},
	}
	cfg.Web.SearchKey = secretSearch
	cfg.Desktop.CDPURL = secretCDP
	return cfg
}

// TestConfigReadsCarryNoSecret pins T7: neither read hands a literal secret
// across the seam, and the mask the settings browser shows is what arrives in
// its place.
func TestConfigReadsCarryNoSecret(t *testing.T) {
	state := newConfigState(t, "s1", secretConfig(), t.TempDir())
	state.SetLayers(config.Layers{
		Default: secretConfig(),
		User:    secretConfig(),
		Merged:  secretConfig(),
	})
	_, sess := New(Deps{State: state})

	encoded, err := json.Marshal(sess.Config())
	if err != nil {
		t.Fatalf("encode Config(): %v", err)
	}
	layers, err := json.Marshal(sess.Layers())
	if err != nil {
		t.Fatalf("encode Layers(): %v", err)
	}
	for name, data := range map[string][]byte{"Config": encoded, "Layers": layers} {
		for _, secret := range []string{"SECRET-1234", "SECRET-2345", "cdp-SECRET-pw"} {
			if strings.Contains(string(data), secret) {
				t.Errorf("%s carries the literal %q: %s", name, secret, data)
			}
		}
		if !strings.Contains(string(data), strutil.MaskKey(secretKey)) {
			t.Errorf("%s does not carry the key's mask: %s", name, data)
		}
	}
}

// TestInstallsRestoreTheStoredKey pins the installs: a config the client hands
// back is restored before it is installed or handed to the reloader.
func TestInstallsRestoreTheStoredKey(t *testing.T) {
	ctx := context.Background()
	state := newConfigState(t, "s1", secretConfig(), t.TempDir())
	var reloaded config.Config
	_, sess := New(Deps{
		State:        state,
		ReloadConfig: func(cfg config.Config) error { reloaded = cfg; return nil },
	})

	if err := sess.ApplyConfig(ctx, sess.Config()); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	if got := state.ConfigSnapshot().Providers["probe"].APIKey; got != secretKey {
		t.Errorf("installed api_key = %q, want the literal %q", got, secretKey)
	}
	if got := state.ConfigSnapshot().Web.SearchKey; got != secretSearch {
		t.Errorf("installed search_key = %q, want the literal %q", got, secretSearch)
	}

	ok, err := sess.ReloadConfig(ctx, sess.Config())
	if !ok || err != nil {
		t.Fatalf("ReloadConfig = (%v, %v), want (true, nil)", ok, err)
	}
	if got := reloaded.Providers["probe"].APIKey; got != secretKey {
		t.Errorf("reloader received api_key %q, want the literal %q", got, secretKey)
	}
}

// TestSaveProjectConfigRestoresTheSearchKey pins the project save: a masked
// search key is restored before the diff, so no mask reaches the project file.
func TestSaveProjectConfigRestoresTheSearchKey(t *testing.T) {
	dir := t.TempDir()
	state := newConfigState(t, "s1", secretConfig(), dir)
	state.SetLayers(config.Layers{Default: config.Default(), User: secretConfig(), Merged: secretConfig()})
	_, sess := New(Deps{State: state})

	if err := sess.SaveProjectConfig(context.Background(), sess.Config()); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	data, err := os.ReadFile(config.ProjectConfigPath(dir))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(data), "search_key") {
		t.Errorf("project config carries a search_key:\n%s", data)
	}
	if strings.Contains(string(data), strutil.MaskKey(secretSearch)) {
		t.Errorf("project config carries a mask:\n%s", data)
	}
}

// TestSaveUserProvidersRestoresKeys pins the provider write: a masked key is
// restored, a rename carries the old name's key to the new name, and a cleared
// key stays cleared.
func TestSaveUserProvidersRestoresKeys(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := t.TempDir()
	path := config.UserConfigPath(home)
	state := newConfigState(t, "s1", secretConfig(), dir)
	host, sess := New(Deps{State: state, HomeDir: home, UserConfigPath: path})

	disk := map[string]config.ProviderConfig{
		"probe": {Type: "openai_compatible", BaseURL: "https://probe.test/v1", APIKey: secretKey},
	}
	if err := config.SaveUserConfigProviders(path, disk); err != nil {
		t.Fatalf("seed providers: %v", err)
	}

	masked := sess.Config().Providers
	if got := masked["probe"].APIKey; got != strutil.MaskKey(secretKey) {
		t.Fatalf("the read does not carry the mask: %q", got)
	}
	if err := host.SaveUserProviders(ctx, masked, masked, nil); err != nil {
		t.Fatalf("SaveUserProviders: %v", err)
	}
	if got := loadUserConfig(t, home, dir).Providers["probe"].APIKey; got != secretKey {
		t.Errorf("saved api_key = %q, want the literal %q", got, secretKey)
	}

	// A provider renamed in the client carries the old name's key.
	renamed := map[string]config.ProviderConfig{
		"moved": {Type: "openai_compatible", BaseURL: "https://probe.test/v1", APIKey: strutil.MaskKey(secretKey)},
	}
	baseline := map[string]config.ProviderConfig{"probe": disk["probe"]}
	if err := host.SaveUserProviders(ctx, renamed, baseline, map[string]string{"moved": "probe"}); err != nil {
		t.Fatalf("SaveUserProviders after a rename: %v", err)
	}
	saved := loadUserConfig(t, home, dir)
	if got := saved.Providers["moved"].APIKey; got != secretKey {
		t.Errorf("renamed api_key = %q, want the literal %q", got, secretKey)
	}
	if _, ok := saved.Providers["probe"]; ok {
		t.Errorf("providers = %v, want the old name dropped", saved.Providers)
	}

	// A cleared key is saved empty, not restored.
	cleared := map[string]config.ProviderConfig{
		"moved": {Type: "openai_compatible", BaseURL: "https://probe.test/v1"},
	}
	if err := host.SaveUserProviders(ctx, cleared, saved.Providers, nil); err != nil {
		t.Fatalf("SaveUserProviders with a cleared key: %v", err)
	}
	if got := loadUserConfig(t, home, dir).Providers["moved"].APIKey; got != "" {
		t.Errorf("cleared api_key = %q, want empty", got)
	}
}

// TestSaveUserValueRestoresAMaskedKey pins the user-value write: at a key's
// path the mask written back is restored, and a typed value is written as
// given.
func TestSaveUserValueRestoresAMaskedKey(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := t.TempDir()
	state := newConfigState(t, "s1", secretConfig(), dir)
	host, _ := New(Deps{State: state, HomeDir: home, UserConfigPath: config.UserConfigPath(home)})

	if err := host.SaveUserValue(ctx, "providers.probe.api_key", strutil.MaskKey(secretKey)); err != nil {
		t.Fatalf("SaveUserValue with a mask: %v", err)
	}
	if got := loadUserConfig(t, home, dir).Providers["probe"].APIKey; got != secretKey {
		t.Errorf("saved api_key = %q, want the literal %q", got, secretKey)
	}

	if err := host.SaveUserValue(ctx, "providers.probe.api_key", "sk-typed-9999"); err != nil {
		t.Fatalf("SaveUserValue with a literal: %v", err)
	}
	if got := loadUserConfig(t, home, dir).Providers["probe"].APIKey; got != "sk-typed-9999" {
		t.Errorf("saved api_key = %q, want the typed %q", got, "sk-typed-9999")
	}

	// The other two secret paths are restored the same way.
	if err := host.SaveUserValue(ctx, "web.search_key", strutil.MaskKey(secretSearch)); err != nil {
		t.Fatalf("SaveUserValue with a masked search key: %v", err)
	}
	if got := loadUserConfig(t, home, dir).Web.SearchKey; got != secretSearch {
		t.Errorf("saved search_key = %q, want the literal %q", got, secretSearch)
	}

	maskedCDP := config.MaskSecrets(config.Config{Desktop: config.DesktopConfig{CDPURL: secretCDP}}).Desktop.CDPURL
	if err := host.SaveUserValue(ctx, "desktop.cdp_url", maskedCDP); err != nil {
		t.Fatalf("SaveUserValue with a masked cdp url: %v", err)
	}
	if got := loadUserConfig(t, home, dir).Desktop.CDPURL; got != secretCDP {
		t.Errorf("saved cdp_url = %q, want the literal %q", got, secretCDP)
	}
}

// TestSaveUserValueRejectsAnUnmatchedMask pins the other half of decision 5:
// at a secret's path a mask with nothing behind it fails the write and leaves
// the file byte-identical.
func TestSaveUserValueRejectsAnUnmatchedMask(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := t.TempDir()
	path := config.UserConfigPath(home)
	state := newConfigState(t, "s1", secretConfig(), dir)
	host, _ := New(Deps{State: state, HomeDir: home, UserConfigPath: path})

	if err := config.SaveUserConfigValue(path, "web.search_key", secretSearch); err != nil {
		t.Fatalf("seed search key: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	want := "config: web.search_key: no stored secret matches its mask"
	if err := host.SaveUserValue(ctx, "web.search_key", "••••zzzz"); err == nil || err.Error() != want {
		t.Fatalf("SaveUserValue error = %v, want %q", err, want)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("a failed write changed the user config:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestSaveProviderKeyRejectsAMask pins decision 5 for the doctor's key write:
// a mask is never persisted, where it would become the stored key every later
// restore matches.
func TestSaveProviderKeyRejectsAMask(t *testing.T) {
	home := t.TempDir()
	state := newConfigState(t, "s1", secretConfig(), t.TempDir())
	host, _ := New(Deps{State: state, HomeDir: home, UserConfigPath: config.UserConfigPath(home)})

	err := host.SaveProviderKey(context.Background(), "probe", strutil.MaskKey(secretKey))
	want := "config: providers.probe.api_key: no stored secret matches its mask"
	if err == nil || err.Error() != want {
		t.Fatalf("SaveProviderKey error = %v, want %q", err, want)
	}
	if _, statErr := os.Stat(config.UserConfigPath(home)); !os.IsNotExist(statErr) {
		t.Errorf("a rejected key wrote the user config: %v", statErr)
	}
}

// TestAnUnmatchedMaskChangesNothing pins decision 5: a mask with no stored
// secret behind it fails the read's inverse and writes nothing, and a probe
// drops the key instead.
func TestAnUnmatchedMaskChangesNothing(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := t.TempDir()
	path := config.UserConfigPath(home)
	state := newConfigState(t, "s1", secretConfig(), dir)
	host, sess := New(Deps{State: state, HomeDir: home, UserConfigPath: path})

	if err := config.SaveUserConfigProviders(path, map[string]config.ProviderConfig{
		"probe": {Type: "openai_compatible", BaseURL: "https://probe.test/v1", APIKey: secretKey},
	}); err != nil {
		t.Fatalf("seed providers: %v", err)
	}
	userBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}

	client := config.RedactSecrets(state.ConfigSnapshot())
	client.Providers["probe"] = config.ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: "https://probe.test/v1",
		APIKey:  "••••zzzz",
	}
	want := "config: providers.probe.api_key: no stored secret matches its mask"

	if err := sess.ApplyConfig(ctx, client); err == nil || err.Error() != want {
		t.Fatalf("ApplyConfig error = %v, want %q", err, want)
	}
	if got := state.ConfigSnapshot().Providers["probe"].APIKey; got != secretKey {
		t.Errorf("api_key = %q after a failed install, want the unchanged %q", got, secretKey)
	}
	if err := sess.SaveProjectConfig(ctx, client); err == nil || err.Error() != want {
		t.Fatalf("SaveProjectConfig error = %v, want %q", err, want)
	}
	if _, err := os.Stat(config.ProjectConfigPath(dir)); !os.IsNotExist(err) {
		t.Errorf("project config stat = %v, want it not to exist", err)
	}
	if err := host.SaveUserProviders(ctx, client.Providers, nil, nil); err == nil || err.Error() != want {
		t.Fatalf("SaveUserProviders error = %v, want %q", err, want)
	}
	userAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if string(userAfter) != string(userBefore) {
		t.Errorf("a failed write changed the user config:\nbefore:\n%s\nafter:\n%s", userBefore, userAfter)
	}
}

// TestProbesUseTheStoredKey pins the probes: a masked key is restored, so the
// request carries the stored key, and a mask with nothing behind it probes
// without one.
func TestProbesUseTheStoredKey(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"m1","owned_by":"test"}]}`)
	}))
	defer srv.Close()

	state := newConfigState(t, "s1", secretConfig(), t.TempDir())
	h, _ := New(Deps{State: state, Auth: newTestAuth()})

	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL, APIKey: strutil.MaskKey(secretKey)}
	if _, err := h.ProbeProvider(context.Background(), "probe", pc); err != nil {
		t.Fatalf("ProbeProvider: %v", err)
	}
	if want := "Bearer " + secretKey; auth != want {
		t.Errorf("Authorization = %q, want %q", auth, want)
	}

	auth = "unset"
	pc.APIKey = "••••zzzz"
	if _, err := h.ProbeProvider(context.Background(), "probe", pc); err != nil {
		t.Fatalf("ProbeProvider with an unmatched mask: %v", err)
	}
	if auth != "" {
		t.Errorf("Authorization = %q, want none at all", auth)
	}
}
