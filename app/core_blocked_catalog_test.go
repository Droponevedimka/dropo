package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestBlockedCatalog(t *testing.T, root string, domains, cidrs string) {
	t.Helper()
	directory := filepath.Join(root, "bin", FiltersFolder)
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, blockedDomainsFileName), []byte(domains), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, blockedIPsFileName), []byte(cidrs), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadBlockedCatalogRejectsPrivateAndNamedEntries(t *testing.T) {
	root := t.TempDir()
	writeTestBlockedCatalog(t, root,
		"blocked-a.example\nblocked-b.example\nblocked-c.example\nblocked-d.example\ndiscord.com\ncdn.discord.com\n",
		"8.8.8.0/24\n10.0.0.0/8\n127.0.0.0/8\n",
	)

	catalog, err := loadBlockedCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Domains) != 4 {
		t.Fatalf("domains = %v, want the four non-named entries", catalog.Domains)
	}
	if len(catalog.IPCIDRs) != 1 || catalog.IPCIDRs[0] != "8.8.8.0/24" {
		t.Fatalf("CIDRs = %v, want only the public test network", catalog.IPCIDRs)
	}
}

func TestRandomBlockedProbeTargetsAreFourDistinctCatalogDomains(t *testing.T) {
	domains := []string{"one.example", "two.example", "three.example", "four.example", "five.example"}
	targets, err := randomBlockedProbeTargets(domains, commonBlockedProbeCount)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != commonBlockedProbeCount {
		t.Fatalf("target count = %d, want %d", len(targets), commonBlockedProbeCount)
	}
	seen := map[string]bool{}
	for _, target := range targets {
		if seen[target.URL] {
			t.Fatalf("duplicate random target %q", target.URL)
		}
		seen[target.URL] = true
	}
}

func TestNativePlanIncludesOneCommonBlockedSelection(t *testing.T) {
	root := t.TempDir()
	writeTestBlockedCatalog(t, root,
		"blocked-a.example\nblocked-b.example\nblocked-c.example\nblocked-d.example\n",
		"8.8.8.0/24\n",
	)
	method := commonBlockedMethods()[0]
	app := &App{basePath: root}
	plan, err := app.buildNativeTrafficPlan(map[string]serviceWinwsSelection{
		commonBlockedServiceTag: {ServiceTag: commonBlockedServiceTag, Method: method},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Services) != 1 || plan.Services[0].ID != commonBlockedServiceTag {
		t.Fatalf("services = %#v", plan.Services)
	}
	if len(plan.Selections) != 1 || plan.Selections[0].StrategyID != method.NativeStrategyID {
		t.Fatalf("selections = %#v", plan.Selections)
	}
}
