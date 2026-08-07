package main

import (
	"strings"
	"testing"

	traffic "dropo/trafficorchestrator"
)

func TestBuildTrafficProbeTargetsAcceptsMixedInputs(t *testing.T) {
	targets, err := buildTrafficProbeTargets(TrafficStrategyUtilityRequest{
		ServiceID: "discord",
		Web:       []string{"https://discord.com/api/v10/gateway"},
		TCP:       []TrafficProbeEndpoint{{Address: "discord.media", Port: 443}},
		UDP:       []TrafficProbeEndpoint{{Address: "66.22.200.1", Port: 50000, Kind: "discord-media"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 3 || targets[2].Kind != traffic.ProbeDiscordMedia {
		t.Fatalf("targets = %+v", targets)
	}
}

func TestBuildTrafficProbeTargetsRejectsUnknownUDPKind(t *testing.T) {
	_, err := buildTrafficProbeTargets(TrafficStrategyUtilityRequest{
		ServiceID: "discord",
		UDP:       []TrafficProbeEndpoint{{Address: "66.22.200.1", Port: 50000, Kind: "magic"}},
	})
	if err == nil {
		t.Fatal("expected unsupported UDP kind error")
	}
}

func TestSelectCandidateStrategiesRejectsUnknownID(t *testing.T) {
	if _, err := selectCandidateStrategies("discord", []string{"shell-command"}); err == nil {
		t.Fatal("expected unknown strategy error")
	}
}

func TestSelectCandidateStrategiesAreServiceScoped(t *testing.T) {
	candidates, err := selectCandidateStrategies("discord", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != len(rankedMethodsForService("discord")) {
		t.Fatalf("candidate count = %d, ranked methods = %d", len(candidates), len(rankedMethodsForService("discord")))
	}
	for _, candidate := range candidates {
		if !strings.HasPrefix(candidate.ID, "native-discord-") {
			t.Fatalf("unscoped Discord candidate: %s", candidate.ID)
		}
	}
}
