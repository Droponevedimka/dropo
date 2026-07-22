package trafficorchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

var ErrNoCommonStrategy = errors.New("no strategy satisfies every required target")

// FailureClass is stable diagnostic output suitable for caching and UI.
type FailureClass string

const (
	FailureNone       FailureClass = ""
	FailureDNS        FailureClass = "dns"
	FailureConnect    FailureClass = "connect"
	FailureReset      FailureClass = "reset"
	FailureTimeout    FailureClass = "timeout"
	FailureTLS        FailureClass = "tls"
	FailureProtocol   FailureClass = "protocol"
	FailureNoResponse FailureClass = "no_response"
)

// ProbeObservation is one bounded target attempt.
type ProbeObservation struct {
	Success bool
	Latency time.Duration
	Failure FailureClass
	Detail  string
}

type ProbeRunner interface {
	Probe(context.Context, ProbeTarget) ProbeObservation
}

// StrategyTrial represents a reversible, service-scoped plan revision.
type StrategyTrial interface {
	Commit() error
	Rollback() error
}

type TrialController interface {
	BeginTrial(context.Context, string, TrafficStrategy) (StrategyTrial, error)
}

type SelectionRequest struct {
	ServiceID         string
	Targets           []ProbeTarget
	Candidates        []TrafficStrategy
	Attempts          int
	RequiredSuccesses int
}

type TargetResult struct {
	TargetID     string
	Attempts     int
	Successes    int
	LastFailure  FailureClass
	BestLatency  time.Duration
	BaselineGood bool
}

type CandidateResult struct {
	StrategyID string
	Passed     bool
	Targets    []TargetResult
}

type SelectionResult struct {
	ServiceID  string
	Strategy   TrafficStrategy
	Baseline   []TargetResult
	Candidates []CandidateResult
}

type Selector struct {
	runner     ProbeRunner
	controller TrialController
}

func NewSelector(runner ProbeRunner, controller TrialController) (*Selector, error) {
	if runner == nil {
		return nil, errors.New("probe runner is required")
	}
	if controller == nil {
		return nil, errors.New("trial controller is required")
	}
	return &Selector{runner: runner, controller: controller}, nil
}

// Select commits only a candidate that passes every non-optional target. Failed
// candidates are rolled back before the next one is installed.
func (s *Selector) Select(ctx context.Context, request SelectionRequest) (SelectionResult, error) {
	if err := validateSelectionRequest(request); err != nil {
		return SelectionResult{}, err
	}
	result := SelectionResult{ServiceID: request.ServiceID}
	result.Baseline = s.runTargets(ctx, request.Targets, request.Attempts, request.RequiredSuccesses, nil)

	candidates := append([]TrafficStrategy(nil), request.Candidates...)
	sort.SliceStable(candidates, func(i, j int) bool {
		return strategyCost(candidates[i]) < strategyCost(candidates[j])
	})
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		trial, err := s.controller.BeginTrial(ctx, request.ServiceID, candidate)
		if err != nil {
			result.Candidates = append(result.Candidates, CandidateResult{StrategyID: candidate.ID})
			continue
		}
		targets := s.runTargets(ctx, request.Targets, request.Attempts, request.RequiredSuccesses, result.Baseline)
		passed := requiredTargetsPassed(request.Targets, targets, request.RequiredSuccesses)
		result.Candidates = append(result.Candidates, CandidateResult{StrategyID: candidate.ID, Passed: passed, Targets: targets})
		if !passed {
			if rollbackErr := trial.Rollback(); rollbackErr != nil {
				return result, fmt.Errorf("rollback strategy %s: %w", candidate.ID, rollbackErr)
			}
			continue
		}
		if err := trial.Commit(); err != nil {
			_ = trial.Rollback()
			return result, fmt.Errorf("commit strategy %s: %w", candidate.ID, err)
		}
		result.Strategy = candidate
		return result, nil
	}
	return result, ErrNoCommonStrategy
}

func (s *Selector) runTargets(ctx context.Context, targets []ProbeTarget, attempts, requiredSuccesses int, baseline []TargetResult) []TargetResult {
	baselineByID := make(map[string]TargetResult, len(baseline))
	for _, item := range baseline {
		baselineByID[item.TargetID] = item
	}
	results := make([]TargetResult, 0, len(targets))
	for _, target := range targets {
		item := TargetResult{TargetID: target.ID, Attempts: attempts}
		if direct, ok := baselineByID[target.ID]; ok {
			item.BaselineGood = direct.Successes >= requiredSuccesses
		}
		for attempt := 0; attempt < attempts; attempt++ {
			if ctx.Err() != nil {
				item.LastFailure = FailureTimeout
				break
			}
			observation := s.runner.Probe(ctx, target)
			if observation.Success {
				item.Successes++
				if item.BestLatency == 0 || (observation.Latency > 0 && observation.Latency < item.BestLatency) {
					item.BestLatency = observation.Latency
				}
			} else {
				item.LastFailure = observation.Failure
			}
		}
		results = append(results, item)
	}
	return results
}

func validateSelectionRequest(request SelectionRequest) error {
	if !validIdentifier(request.ServiceID) {
		return errors.New("invalid service id")
	}
	if len(request.Targets) == 0 {
		return errors.New("at least one target is required")
	}
	if len(request.Candidates) == 0 {
		return errors.New("at least one candidate is required")
	}
	if request.Attempts < 1 || request.Attempts > 10 {
		return errors.New("attempts must be within 1..10")
	}
	if request.RequiredSuccesses < 1 || request.RequiredSuccesses > request.Attempts {
		return errors.New("required successes must be within 1..attempts")
	}
	targetIDs := map[string]struct{}{}
	for _, target := range request.Targets {
		if err := ValidateProbeTarget(target); err != nil {
			return fmt.Errorf("target %q: %w", target.ID, err)
		}
		if _, duplicate := targetIDs[target.ID]; duplicate {
			return fmt.Errorf("duplicate target %q", target.ID)
		}
		targetIDs[target.ID] = struct{}{}
	}
	candidateIDs := map[string]struct{}{}
	for _, candidate := range request.Candidates {
		if err := ValidateStrategy(candidate); err != nil {
			return fmt.Errorf("candidate %q: %w", candidate.ID, err)
		}
		if _, duplicate := candidateIDs[candidate.ID]; duplicate {
			return fmt.Errorf("duplicate candidate %q", candidate.ID)
		}
		candidateIDs[candidate.ID] = struct{}{}
	}
	return nil
}

func requiredTargetsPassed(targets []ProbeTarget, results []TargetResult, threshold int) bool {
	resultByID := make(map[string]TargetResult, len(results))
	for _, result := range results {
		resultByID[result.TargetID] = result
	}
	for _, target := range targets {
		result := resultByID[target.ID]
		// An optional target is diagnostic only when it was already unavailable
		// at baseline. Once direct traffic proved it works, it becomes a
		// regression guard: a candidate may not break a healthy endpoint while
		// fixing the required set.
		if target.Optional && !result.BaselineGood {
			continue
		}
		if result.Successes < threshold {
			return false
		}
	}
	return true
}

func strategyCost(strategy TrafficStrategy) int {
	return strategy.Cost.Risk*1_000_000 + strategy.Cost.SyntheticPackets*10_000 + strategy.Cost.BufferedBytes
}
