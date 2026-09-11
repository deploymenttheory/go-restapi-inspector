package model

import "context"

// ExperimentPlan records the question and logical input before fixture creation.
// Request identifiers remain separate from the experiment that owns them.
type ExperimentPlan struct {
	ID                string   `json:"id"`
	Operation         string   `json:"operation"`
	Phase             string   `json:"phase"`
	Purpose           string   `json:"purpose"`
	Input             Input    `json:"input"`
	Baseline          *Input   `json:"baseline,omitempty"`
	ChangedFields     []string `json:"changedFields,omitempty"`
	Control           string   `json:"control,omitempty"`
	ConfirmationGroup string   `json:"confirmationGroup,omitempty"`
}

type traceKey struct{}
type trace struct {
	experiment, request, purpose, group string
	baseline                            *Input
}

func WithExperiment(ctx context.Context, id string) context.Context {
	v, _ := ctx.Value(traceKey{}).(trace)
	v.experiment, v.request = id, ""
	return context.WithValue(ctx, traceKey{}, v)
}
func WithRequest(ctx context.Context, id string) context.Context {
	v, _ := ctx.Value(traceKey{}).(trace)
	v.request = id
	return context.WithValue(ctx, traceKey{}, v)
}
func WithProbe(ctx context.Context, purpose string, baseline Input, group string) context.Context {
	v, _ := ctx.Value(traceKey{}).(trace)
	v.purpose, v.baseline, v.group = purpose, &baseline, group
	return context.WithValue(ctx, traceKey{}, v)
}
func ExperimentID(ctx context.Context) string {
	v, _ := ctx.Value(traceKey{}).(trace)
	return v.experiment
}
func RequestID(ctx context.Context) string { v, _ := ctx.Value(traceKey{}).(trace); return v.request }
func ProbeInfo(ctx context.Context) (string, *Input, string) {
	v, _ := ctx.Value(traceKey{}).(trace)
	return v.purpose, v.baseline, v.group
}
