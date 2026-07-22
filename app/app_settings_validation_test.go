package main

import "testing"

func TestSaveAppConfigRejectsUnsupportedValues(t *testing.T) {
	app := newInitializedSettingsScenarioApp(t)
	stubApplyAutoStart(t)

	tests := []struct {
		name     string
		theme    string
		language string
		level    string
		interval int
	}{
		{name: "theme", theme: "neon", language: "ru", level: "info", interval: 24},
		{name: "language", theme: "system", language: "en", level: "info", interval: 24},
		{name: "log level", theme: "system", language: "ru", level: "verbose", interval: 24},
		{name: "short interval", theme: "system", language: "ru", level: "info", interval: 0},
		{name: "long interval", theme: "system", language: "ru", level: "info", interval: 721},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := app.SaveAppConfig(true, true, true, true, true, test.theme, test.language, test.level, test.interval)
			if success, _ := result["success"].(bool); success {
				t.Fatalf("SaveAppConfig accepted invalid %s: %#v", test.name, result)
			}
		})
	}
}

func TestSaveAppConfigAppliesThemeLiveButProtectsActiveLogging(t *testing.T) {
	app := newInitializedSettingsScenarioApp(t)
	stubApplyAutoStart(t)
	app.mu.Lock()
	app.isRunning = true
	app.mu.Unlock()

	result := app.SaveAppConfig(true, true, true, true, true, "dark", "ru", "info", 24)
	requireAPISuccess(t, result)
	if app.storage.GetAppSettings().Theme != ThemeDark {
		t.Fatal("theme was not saved while VPN was active")
	}

	result = app.SaveAppConfig(true, true, true, true, true, "dark", "ru", "debug", 24)
	if success, _ := result["success"].(bool); success {
		t.Fatal("logging level changed while VPN was active")
	}
}

func TestStorageNormalizesUnsupportedVisibleSettings(t *testing.T) {
	storage := NewStorage(t.TempDir())
	if err := storage.Init(); err != nil {
		t.Fatal(err)
	}
	storage.data.App.Theme = Theme("neon")
	storage.data.App.Language = LangEnglish
	storage.data.App.LogLevel = LogLevel("verbose")
	storage.data.App.RoutingMode = RoutingMode("unknown")
	storage.data.App.SubUpdateInterval = 9999
	storage.normalizeAppSettings()

	settings := storage.data.App
	if settings.Theme != ThemeSystem || settings.Language != LangRussian || settings.LogLevel != LogLevelInfo ||
		settings.RoutingMode != DefaultRoutingMode || settings.SubUpdateInterval != 24 {
		t.Fatalf("normalized settings = %+v", settings)
	}
}
