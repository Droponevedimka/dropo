package main

import "testing"

// stubApplyAutoStart replaces the OS-level autostart applier with a recorder for
// the duration of a test, so no real registry / Scheduled Task is touched.
func stubApplyAutoStart(t *testing.T) *[]bool {
	t.Helper()
	orig := applyAutoStart
	calls := &[]bool{}
	applyAutoStart = func(enable bool) error {
		*calls = append(*calls, enable)
		return nil
	}
	t.Cleanup(func() { applyAutoStart = orig })
	return calls
}

func TestDefaultAutoStartAwaitsPrompt(t *testing.T) {
	app := newInitializedSettingsScenarioApp(t)

	settings := app.storage.GetAppSettings()
	if !settings.AutoStart {
		t.Fatal("default AutoStart = false, want true (enabled pending the first-run prompt)")
	}
	if settings.AutoStartPrompted {
		t.Fatal("default AutoStartPrompted = true, want false so the first-run dialog shows")
	}

	cfg := app.GetAppConfig()
	if prompted, _ := cfg["autoStartPrompted"].(bool); prompted {
		t.Fatalf("GetAppConfig autoStartPrompted = true, want false: %+v", cfg["autoStartPrompted"])
	}
	if auto, _ := cfg["autoStart"].(bool); !auto {
		t.Fatalf("GetAppConfig autoStart = false, want true")
	}
}

func TestEnsureAutoStartRegistrationDefersUntilPrompted(t *testing.T) {
	app := newInitializedSettingsScenarioApp(t)
	calls := stubApplyAutoStart(t)
	originalConsume := consumeInstallerAutoStartPreference
	consumeInstallerAutoStartPreference = func() (bool, bool) { return false, false }
	t.Cleanup(func() { consumeInstallerAutoStartPreference = originalConsume })

	// Not prompted yet: startup must NOT touch the OS autostart entry.
	app.ensureAutoStartRegistration()
	if len(*calls) != 0 {
		t.Fatalf("autostart applied before the prompt was answered: %v", *calls)
	}

	// Once answered, startup syncs the stored preference to the OS.
	settings := app.storage.GetAppSettings()
	settings.AutoStartPrompted = true
	settings.AutoStart = true
	if err := app.storage.UpdateAppSettings(settings); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	app.ensureAutoStartRegistration()
	if len(*calls) != 1 || (*calls)[0] != true {
		t.Fatalf("expected one apply(true) after prompt answered, got %v", *calls)
	}
}

func TestEnsureAutoStartRegistrationImportsInstallerChoice(t *testing.T) {
	app := newInitializedSettingsScenarioApp(t)
	calls := stubApplyAutoStart(t)
	originalConsume := consumeInstallerAutoStartPreference
	consumeInstallerAutoStartPreference = func() (bool, bool) { return false, true }
	t.Cleanup(func() { consumeInstallerAutoStartPreference = originalConsume })

	app.ensureAutoStartRegistration()
	settings := app.storage.GetAppSettings()
	if settings.AutoStart || !settings.AutoStartPrompted {
		t.Fatalf("imported settings = {AutoStart:%v Prompted:%v}", settings.AutoStart, settings.AutoStartPrompted)
	}
	if len(*calls) != 1 || (*calls)[0] {
		t.Fatalf("expected installer choice apply(false), got %v", *calls)
	}
}

func TestResolveAutoStartPromptEnable(t *testing.T) {
	app := newInitializedSettingsScenarioApp(t)
	calls := stubApplyAutoStart(t)

	result := app.ResolveAutoStartPrompt(true)
	requireAPISuccess(t, result)
	if auto, _ := result["autoStart"].(bool); !auto {
		t.Fatalf("result autoStart = false, want true")
	}
	if prompted, _ := result["autoStartPrompted"].(bool); !prompted {
		t.Fatalf("result autoStartPrompted = false, want true")
	}

	settings := app.storage.GetAppSettings()
	if !settings.AutoStart || !settings.AutoStartPrompted {
		t.Fatalf("persisted settings = {AutoStart:%v Prompted:%v}, want both true", settings.AutoStart, settings.AutoStartPrompted)
	}
	if len(*calls) != 1 || (*calls)[0] != true {
		t.Fatalf("expected apply(true), got %v", *calls)
	}
}

func TestResolveAutoStartPromptDisable(t *testing.T) {
	app := newInitializedSettingsScenarioApp(t)
	calls := stubApplyAutoStart(t)

	result := app.ResolveAutoStartPrompt(false)
	requireAPISuccess(t, result)
	if auto, _ := result["autoStart"].(bool); auto {
		t.Fatalf("result autoStart = true, want false")
	}

	settings := app.storage.GetAppSettings()
	if settings.AutoStart {
		t.Fatal("persisted AutoStart = true, want false after declining the prompt")
	}
	if !settings.AutoStartPrompted {
		t.Fatal("persisted AutoStartPrompted = false, want true so the dialog is not shown again")
	}
	if len(*calls) != 1 || (*calls)[0] != false {
		t.Fatalf("expected apply(false), got %v", *calls)
	}

	// Declining must be sticky across a subsequent startup sync.
	app.ensureAutoStartRegistration()
	if len(*calls) != 2 || (*calls)[1] != false {
		t.Fatalf("startup sync after decline should apply(false), got %v", *calls)
	}
}

func TestSaveAppConfigSyncsAndMarksPrompted(t *testing.T) {
	app := newInitializedSettingsScenarioApp(t)
	calls := stubApplyAutoStart(t)

	// Toggle autostart OFF via the settings screen.
	off := app.SaveAppConfig(false, true, true, true, true, "dark", "ru", "info", 24)
	requireAPISuccess(t, off)
	settings := app.storage.GetAppSettings()
	if settings.AutoStart {
		t.Fatal("AutoStart = true after saving it off")
	}
	if !settings.AutoStartPrompted {
		t.Fatal("saving settings should mark AutoStartPrompted = true")
	}

	// Toggle autostart back ON.
	on := app.SaveAppConfig(true, true, true, true, true, "dark", "ru", "info", 24)
	requireAPISuccess(t, on)
	if !app.storage.GetAppSettings().AutoStart {
		t.Fatal("AutoStart = false after saving it on")
	}

	if len(*calls) != 2 || (*calls)[0] != false || (*calls)[1] != true {
		t.Fatalf("expected apply(false) then apply(true), got %v", *calls)
	}
}

func TestResolveAutoStartPromptPersistsChoiceOnApplyError(t *testing.T) {
	app := newInitializedSettingsScenarioApp(t)
	orig := applyAutoStart
	applyAutoStart = func(enable bool) error { return errStubAutoStart }
	t.Cleanup(func() { applyAutoStart = orig })

	result := app.ResolveAutoStartPrompt(true)
	if success, _ := result["success"].(bool); success {
		t.Fatal("expected success=false when applying autostart fails")
	}
	// The choice must still be recorded so the first-run dialog does not reappear
	// on the next launch even though the OS apply failed.
	settings := app.storage.GetAppSettings()
	if !settings.AutoStartPrompted {
		t.Fatal("AutoStartPrompted = false after a failed apply, want true (choice remembered)")
	}
}

var errStubAutoStart = stubAutoStartError("stub apply failure")

type stubAutoStartError string

func (e stubAutoStartError) Error() string { return string(e) }
