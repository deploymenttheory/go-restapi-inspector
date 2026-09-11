package report

import (
	"sort"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
)

func Compare(current, baseline Run, c, b config.Config) Comparison {
	out := Comparison{SameSnapshot: current.Snapshot == baseline.Snapshot, ContextDifferences: []string{}, Findings: []FindingChange{}, Coverage: []CoverageChange{}}
	contexts := []struct {
		name              string
		current, baseline any
	}{
		{"Source specification", current.SpecHash, baseline.SpecHash}, {"Target API", current.Target, baseline.Target},
		{"Recorded authentication configuration", []any{c.Auth, c.AuthProfiles, c.Security}, []any{b.Auth, b.AuthProfiles, b.Security}},
		{"Oracles and operation hints", []any{c.Oracle, c.Hints}, []any{b.Oracle, b.Hints}},
		{"Discovery settings", []any{c.Mode, c.Strategy, c.InteractionOrder, c.ValidationTrials, c.BoundarySteps, c.Rules}, []any{b.Mode, b.Strategy, b.InteractionOrder, b.ValidationTrials, b.BoundarySteps, b.Rules}},
		{"Configured domains", c.Domains, b.Domains}, {"Request and duration budgets", []any{c.MaxRequests, c.MaxDuration, c.ConfirmationReserve}, []any{b.MaxRequests, b.MaxDuration, b.ConfirmationReserve}},
		{"Observed test domains", current.domains, baseline.domains},
	}
	for _, context := range contexts {
		if model.ID(context.current) != model.ID(context.baseline) {
			out.ContextDifferences = append(out.ContextDifferences, context.name)
		}
	}
	old := map[string]Rule{}
	newRules := map[string]Rule{}
	for _, rule := range baseline.Rules {
		old[rule.semantic] = rule
	}
	for _, rule := range current.Rules {
		newRules[rule.semantic] = rule
	}
	used := map[string]bool{}
	for _, rule := range current.Rules {
		previous, ok := old[rule.semantic]
		if ok {
			used[rule.semantic] = true
			if previous.Status != rule.Status {
				status := "Status changed"
				if rule.Status == "supported" {
					status = "Newly supported"
				}
				out.Findings = append(out.Findings, FindingChange{Operation: rule.Operation, Field: rule.Field, Status: status, Before: previous.Status + ": " + previous.Summary, After: rule.Status + ": " + rule.Summary, BeforeRule: previous.ID, AfterRule: rule.ID})
			}
			continue
		}
		// Pair a revision only when the unmatched semantic family is unambiguous.
		candidates := []Rule{}
		newCount := 0
		for _, r := range baseline.Rules {
			if r.family == rule.family && !used[r.semantic] {
				if _, exists := newRules[r.semantic]; !exists {
					candidates = append(candidates, r)
				}
			}
		}
		for _, r := range current.Rules {
			if r.family == rule.family {
				if _, exists := old[r.semantic]; !exists {
					newCount++
				}
			}
		}
		if len(candidates) == 1 && newCount == 1 {
			p := candidates[0]
			used[p.semantic] = true
			out.Findings = append(out.Findings, FindingChange{Operation: rule.Operation, Field: rule.Field, Status: "Revised finding", Before: p.Summary, After: rule.Summary, BeforeRule: p.ID, AfterRule: rule.ID})
			continue
		}
		status := "Added finding"
		if rule.Status == "supported" {
			status = "Newly supported"
		}
		out.Findings = append(out.Findings, FindingChange{Operation: rule.Operation, Field: rule.Field, Status: status, After: rule.Summary, AfterRule: rule.ID})
	}
	for _, rule := range baseline.Rules {
		if !used[rule.semantic] {
			if _, ok := newRules[rule.semantic]; !ok {
				out.Findings = append(out.Findings, FindingChange{Operation: rule.Operation, Field: rule.Field, Status: "No longer reported", Before: rule.Summary, BeforeRule: rule.ID})
			}
		}
	}
	before, after := map[string]string{}, map[string]string{}
	for _, op := range baseline.Operations {
		before[op.Key] = op.State
	}
	for _, op := range current.Operations {
		after[op.Key] = op.State
	}
	all := map[string]bool{}
	for k := range before {
		all[k] = true
	}
	for k := range after {
		all[k] = true
	}
	for _, key := range keys(all) {
		a, b := after[key], before[key]
		if a == "" {
			a = "untested"
		}
		if b == "" {
			b = "untested"
		}
		out.Coverage = append(out.Coverage, CoverageChange{Operation: key, Before: b, After: a})
	}
	sort.Slice(out.Findings, func(i, j int) bool {
		a, b := out.Findings[i], out.Findings[j]
		return a.Operation+a.Field+a.Status+a.After+a.Before < b.Operation+b.Field+b.Status+b.After+b.Before
	})
	return out
}
