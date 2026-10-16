package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func desktopTestConfig(t *testing.T) Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return testConfig(t)
}

func TestDesktopSetupUpdateAndRestorePreservesUnrelatedChanges(t *testing.T) {
	c := desktopTestConfig(t)
	before := map[string]any{
		"permissions":    map[string]any{"deny": []string{"Bash(rm *)"}},
		"enabledPlugins": map[string]bool{"frontend-design@claude-plugins-official": true},
		"env":            map[string]string{"MY_PROJECT_VALUE": "unchanged", "TZ": "UTC"},
	}
	if e := writeJSON(sharedSettingsPath(), before); e != nil {
		t.Fatal(e)
	}
	if e := configureDesktop(c); e != nil {
		t.Fatal(e)
	}
	state, exists, e := loadDesktopState()
	if e != nil || !exists {
		t.Fatalf("missing recovery state: %v", e)
	}
	backup, e := os.ReadFile(state.Backup)
	if e != nil {
		t.Fatal(e)
	}
	fi, _ := os.Stat(state.Backup)
	if fi.Mode().Perm() != 0600 {
		t.Fatal("backup is not private")
	}
	var backed map[string]any
	if json.Unmarshal(backup, &backed) != nil || backed["permissions"] == nil {
		t.Fatal("backup lost settings")
	}
	settingsBefore, _ := os.Stat(sharedSettingsPath())
	stateBefore, _ := os.Stat(desktopStatePath())
	if e = configureDesktop(c); e != nil {
		t.Fatal(e)
	}
	settingsAfter, _ := os.Stat(sharedSettingsPath())
	stateAfter, _ := os.Stat(desktopStatePath())
	if !os.SameFile(settingsBefore, settingsAfter) || !os.SameFile(stateBefore, stateAfter) {
		t.Fatal("unchanged setup rewrote settings or recovery metadata")
	}
	c.Timezone = "Asia/Singapore"
	if e = configureDesktop(c); e != nil {
		t.Fatal(e)
	}
	updated, _, _ := loadDesktopState()
	if updated.Backup != state.Backup {
		t.Fatal("repeat setup replaced original backup")
	}
	s, _ := readDesktopSettings(sharedSettingsPath())
	if !sameJSON(s.Env["TZ"], rawString(c.Timezone)) {
		t.Fatal("timezone not applied")
	}
	// Simulate a user adding a preference after setup. Restore must preserve it.
	s.Root["newPreference"] = rawString("keep")
	if e = saveDesktopSettings(sharedSettingsPath(), s); e != nil {
		t.Fatal(e)
	}
	c.Timezone = ""
	if e = configureDesktop(c); e != nil {
		t.Fatal(e)
	}
	s, _ = readDesktopSettings(sharedSettingsPath())
	if !sameJSON(s.Env["TZ"], rawString("UTC")) {
		t.Fatal("disabling timezone failed to restore original TZ")
	}
	if e = restoreDesktop(); e != nil {
		t.Fatal(e)
	}
	s, _ = readDesktopSettings(sharedSettingsPath())
	if s.Env["HTTPS_PROXY"] != nil || s.Env["ANTHROPIC_BASE_URL"] != nil || s.Root["forceLoginMethod"] != nil {
		t.Fatal("managed values survived restore")
	}
	for _, k := range []string{"permissions", "enabledPlugins", "newPreference"} {
		if s.Root[k] == nil {
			t.Fatalf("restore removed %s", k)
		}
	}
	if !sameJSON(s.Env["MY_PROJECT_VALUE"], rawString("unchanged")) {
		t.Fatal("unrelated env changed")
	}
}

func TestDesktopRefusesCredentialOverridesWithoutPrintingSecrets(t *testing.T) {
	c := desktopTestConfig(t)
	if e := writeJSON(sharedSettingsPath(), map[string]any{"env": map[string]string{"ANTHROPIC_AUTH_TOKEN": "SECRET-NOT-FOR-LOGS", "ANTHROPIC_BASE_URL": "https://thirdparty.invalid"}}); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(sharedSettingsPath())
	e := configureDesktop(c)
	if e == nil || strings.Contains(e.Error(), "SECRET") || strings.Contains(e.Error(), "thirdparty.invalid") {
		t.Fatalf("unsafe error: %v", e)
	}
	after, _ := os.ReadFile(sharedSettingsPath())
	if string(before) != string(after) {
		t.Fatal("conflicting credentials were overwritten")
	}
	if _, e = os.Stat(desktopStatePath()); !os.IsNotExist(e) {
		t.Fatal("unexpected recovery state on conflict")
	}
}

func TestDesktopRefusesToOverwriteExternalProxyEdits(t *testing.T) {
	c := desktopTestConfig(t)
	if e := configureDesktop(c); e != nil {
		t.Fatal(e)
	}
	s, _ := readDesktopSettings(sharedSettingsPath())
	s.Env["HTTPS_PROXY"] = rawString("http://other.invalid:1234")
	if e := saveDesktopSettings(sharedSettingsPath(), s); e != nil {
		t.Fatal(e)
	}
	if configureDesktop(c) == nil || restoreDesktop() == nil {
		t.Fatal("external proxy edit overwritten")
	}
	s, _ = readDesktopSettings(sharedSettingsPath())
	if !sameJSON(s.Env["HTTPS_PROXY"], rawString("http://other.invalid:1234")) {
		t.Fatal("external edit lost")
	}
}

func TestDesktopSettingsConcurrentEditIsPreserved(t *testing.T) {
	_ = desktopTestConfig(t)
	if e := writeJSON(sharedSettingsPath(), map[string]string{"a": "before"}); e != nil {
		t.Fatal(e)
	}
	s, _ := readDesktopSettings(sharedSettingsPath())
	if e := writeJSON(sharedSettingsPath(), map[string]string{"a": "new"}); e != nil {
		t.Fatal(e)
	}
	s.Root["a"] = rawString("stale")
	if saveDesktopSettings(sharedSettingsPath(), s) == nil {
		t.Fatal("stale snapshot accepted")
	}
}

func TestDesktopChildDoesNotReuseCLIIdentityOrDisableTLS(t *testing.T) {
	c := desktopTestConfig(t)
	c.Timezone = "Asia/Singapore"
	env := strings.Join(environment(c, []string{"PATH=/usr/bin", "ANTHROPIC_AUTH_TOKEN=SECRET", "CLAUDE_CODE_OAUTH_TOKEN=SECRET", "CLAUDE_CONFIG_DIR=/old", "TZ=Asia/Shanghai", "NO_PROXY=*", "NODE_TLS_REJECT_UNAUTHORIZED=0"}, ""), "\n")
	for _, bad := range []string{"SECRET", "CLAUDE_CONFIG_DIR=", "TZ=Asia/Shanghai", "NO_PROXY=*", "NODE_TLS_REJECT_UNAUTHORIZED=0"} {
		if strings.Contains(env, bad) {
			t.Fatalf("inherited unsafe value: %s", bad)
		}
	}
	for _, want := range []string{"TZ=Asia/Singapore", "HTTPS_PROXY=http://" + c.Listen, "NODE_TLS_REJECT_UNAUTHORIZED=1"} {
		if !strings.Contains(env, want) {
			t.Fatalf("missing %s", want)
		}
	}
	args := strings.Join(desktopArgs(c), "\n")
	for _, bad := range []string{"remote-debugging", "ignore-certificate", "disable-web-security", "no-sandbox", "DIRECT"} {
		if strings.Contains(args, bad) {
			t.Fatalf("unsafe app flag: %s", bad)
		}
	}
}

func TestOfficialDesktopSettingsDoNotTriggerLegacyCredentialWarning(t *testing.T) {
	c := desktopTestConfig(t)
	if e := configureDesktop(c); e != nil {
		t.Fatal(e)
	}
	if legacyWarning() != "" {
		t.Fatal("official API setting mistaken for a legacy credential")
	}
	s, _ := readDesktopSettings(sharedSettingsPath())
	s.Env["ANTHROPIC_BASE_URL"] = rawString("https://thirdparty.invalid")
	if e := saveDesktopSettings(sharedSettingsPath(), s); e != nil {
		t.Fatal(e)
	}
	if legacyWarning() == "" {
		t.Fatal("custom endpoint not reported")
	}
}

func TestTimezoneConfigValidationAndDefault(t *testing.T) {
	c := desktopTestConfig(t)
	for _, tz := range []string{"Asia/Singapore", "America/New_York", "UTC", ""} {
		c.Timezone = tz
		if e := c.validate(); e != nil {
			t.Fatalf("valid timezone %q: %v", tz, e)
		}
	}
	for _, tz := range []string{"Local", "/etc/passwd", "../secret", "Invalid/Zone"} {
		c.Timezone = tz
		if c.validate() == nil {
			t.Fatalf("invalid timezone accepted: %q", tz)
		}
	}
	c.Timezone = ""
	if _, ok := protectedEnv(c)["TZ"]; ok {
		t.Fatal("default timezone override")
	}
}

func TestDesktopShortcutQuotesPathsAndRefusesUnknownBundle(t *testing.T) {
	_ = desktopTestConfig(t)
	if e := desktopShortcut(); e != nil {
		t.Fatal(e)
	}
	h, _ := os.UserHomeDir()
	app := filepath.Join(h, "Applications/Claude via ccg.app")
	if e := desktopShortcut(); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(app, "Contents/Info.plist"), []byte("another app"), 0600); e != nil {
		t.Fatal(e)
	}
	if desktopShortcut() == nil {
		t.Fatal("unrelated app overwritten")
	}
	if got := shellQuote("/a path/it's/$HOME"); got != "'/a path/it'\"'\"'s/$HOME'" {
		t.Fatalf("invalid shell quoting: %s", got)
	}
}
