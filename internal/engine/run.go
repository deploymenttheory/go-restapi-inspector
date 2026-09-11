// Package engine executes bounded, journaled experiments against an API.
package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/generate"
	"github.com/deploymenttheory/go-restapi-inspector/internal/graph"
	"github.com/deploymenttheory/go-restapi-inspector/internal/httpclient"
	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

type Snapshot struct {
	DocumentFingerprint string                               `json:"documentFingerprint,omitempty"`
	Fingerprints        map[string]spec.OperationFingerprint `json:"operationFingerprints,omitempty"`
	documentModel       *spec.Document
	plan                *graph.Plan
	Recovered           bool           `json:"-"`
	Config              config.Config  `json:"config"`
	Document            map[string]any `json:"document"`
	Report              model.Report   `json:"report"`
}
type Result struct {
	HTMLReport   bool
	Dir          string
	Document     *spec.Document
	Report       model.Report
	Observations []model.Observation
	Redactor     *journal.Redactor
}
type Runner struct {
	Fingerprints   map[string]spec.OperationFingerprint
	Progress       map[string]*operationProgress
	Effects        []Effect
	cleanupTried   map[string]bool
	Config         config.Config
	Document       *spec.Document
	Plan           *graph.Plan
	Journal        *journal.Journal
	Client         *httpclient.Client
	Report         model.Report
	Observations   []model.Observation
	Log            func(string)
	previousCounts map[string]int
}

func New(c config.Config, d *spec.Document, dir string) (*Runner, error) {
	p, err := graph.Build(d, c)
	if err != nil {
		return nil, err
	}
	return newWithPlan(c, d, dir, p)
}

func newWithPlan(c config.Config, d *spec.Document, dir string, p *graph.Plan) (*Runner, error) {
	fingerprints, err := d.Fingerprints()
	if err != nil {
		return nil, err
	}
	redactor := journal.NewRedactor(c.SensitiveFields)
	j, err := journal.Open(dir, redactor)
	if err != nil {
		return nil, err
	}
	client, err := httpclient.New(c, j, redactor)
	if err != nil {
		j.Close()
		return nil, err
	}
	r := &Runner{Config: c, Document: d, Plan: p, Journal: j, Client: client, Report: model.Report{Version: model.Version, RunID: filepath.Base(dir), BaseURL: c.BaseURL, SpecHash: d.Hash, Started: time.Now().UTC(), State: "running", Requests: map[string]int{}, Warnings: p.Warnings}}
	r.Fingerprints = fingerprints
	identity := d.Identity(c.SpecRelease)
	r.Report.SpecIdentity = &identity
	return r, nil
}
func Inspect(ctx context.Context, c config.Config, log func(string)) (*Result, error) {
	if err := c.Validate(true); err != nil {
		return nil, err
	}
	d, err := spec.Load(ctx, c.Spec)
	if err != nil {
		return nil, err
	}
	prepared, err := PrepareIncremental(c, d)
	if err != nil {
		return nil, err
	}
	c = prepared.Config
	dir := filepath.Join(c.Output, time.Now().UTC().Format("20060102T150405.000000000Z"))
	r, err := newWithPlan(c, d, dir, prepared.Plan)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	r.Log = log
	if err = r.Journal.Append("run", r.snapshot()); err != nil {
		return nil, err
	}
	if err = prepared.inherit(r); err != nil {
		return nil, err
	}
	return r.Run(ctx)
}

func (r *Runner) snapshot() Snapshot {
	return Snapshot{Config: r.Config, Document: r.Document.Raw, Report: r.Report, Fingerprints: r.Fingerprints, DocumentFingerprint: spec.Fingerprint(r.Client.Redactor.Redact(r.Document.Raw))}
}
func (r *Runner) Close() error { r.Client.Close(); return r.Journal.Close() }
func (r *Runner) log(s string) {
	if r.Log != nil {
		r.Log(s)
	}
}
func (r *Runner) Run(ctx context.Context) (*Result, error) {
	if r.Config.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Config.MaxDuration)
		defer cancel()
	}
	completed := map[string]bool{}
	for _, c := range r.Report.Coverage {
		if c.State == "complete" && c.PlanSignature == r.planSignature(c.Operation) {
			completed[c.Operation] = true
		}
	}
	var runErr error
	for _, key := range r.Plan.Order {
		op, _ := r.Plan.Find(key)
		if !r.selected(op) || completed[key] {
			continue
		}
		if err := ctx.Err(); err != nil {
			runErr = err
			break
		}
		r.log("Inspecting " + key)
		// Remove only the incomplete operation's previous analysis. Its evidence is
		// retained; fresh fixtures avoid replaying redacted input as if it were raw.
		r.dropAnalysis(key)
		coverage, rules, err := r.inspectOperation(ctx, op)
		r.Report.Coverage = append(r.Report.Coverage, coverage)
		r.Report.Rules = append(r.Report.Rules, rules...)
		if persist := r.checkpoint(); persist != nil {
			runErr = persist
			break
		}
		if err != nil {
			r.Report.Warnings = append(r.Report.Warnings, key+": "+err.Error())
			leftovers := false
			for _, resource := range r.Report.Resources {
				if resource.State != "deleted" {
					leftovers = true
				}
			}
			if leftovers || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, httpclient.ErrBudget) || errors.Is(err, ErrConfirmationReserve) {
				runErr = err
				break
			}
		}
	}
	// A canceled inspection still receives its own bounded cleanup window.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), r.Config.Cleanup.Timeout)
	cleanupErr := r.Cleanup(cleanupCtx)
	cancel()
	r.Report.State = "complete"
	if runErr != nil || cleanupErr != nil {
		r.Report.State = "partial"
	}
	for _, c := range r.Report.Coverage {
		if c.State != "complete" {
			r.Report.State = "partial"
		}
	}
	for _, resource := range r.Report.Resources {
		if resource.State != "deleted" {
			r.Report.State = "partial"
		}
	}
	r.Report.Finished = time.Now().UTC()
	r.Report.Rules = append(r.Report.Rules, r.effectRules()...)
	r.addOperationRequiredness()
	unique := map[string]int{}
	var deduplicated []model.Rule
	for _, rule := range r.Report.Rules {
		if i, exists := unique[rule.ID]; exists {
			deduplicated[i] = rule
		} else {
			unique[rule.ID] = len(deduplicated)
			deduplicated = append(deduplicated, rule)
		}
	}
	r.Report.Rules = deduplicated
	persist := r.checkpoint()
	return &Result{HTMLReport: r.Config.ReportEnabled(), Dir: r.Journal.Dir, Document: r.Document, Report: r.Report, Observations: r.Observations, Redactor: r.Client.Redactor}, errors.Join(runErr, cleanupErr, persist)
}
func (r *Runner) selected(op model.Operation) bool {
	if len(r.Config.Operations) == 0 && !r.Config.SelectionExplicit {
		return true
	}
	for _, id := range r.Config.Operations {
		if id == op.Key || id == op.ID {
			return true
		}
	}
	return false
}
func (r *Runner) dropAnalysis(key string) {
	var coverage []model.Coverage
	for _, v := range r.Report.Coverage {
		if v.Operation != key {
			coverage = append(coverage, v)
		}
	}
	r.Report.Coverage = coverage
	var rules []model.Rule
	for _, v := range r.Report.Rules {
		if v.Operation != key {
			rules = append(rules, v)
		}
	}
	r.Report.Rules = rules
}
func (r *Runner) checkpoint() error {
	counts := r.Client.Counts()
	for k, n := range r.previousCounts {
		counts[k] += n
	}
	r.Report.Requests = counts
	if err := r.Journal.Append("report", r.Report); err != nil {
		return err
	}
	return journal.WriteJSON(r.Journal.Dir, "report.json", r.Client.Redactor.Redact(r.Report))
}

func (r *Runner) baselines(op model.Operation) []model.Input {
	out := generate.Baselines(op, r.Config)
	for i := range out {
		for _, e := range r.Plan.Bindings(op.Key) {
			if _, ok := out[i].Get(e.Field); ok {
				_ = out[i].Set(e.Field, model.State{Present: true, Value: "@inspector:" + e.Field})
			}
		}
	}
	return out
}

type fixture struct {
	resources map[string]model.Resource
	parents   []string
	stack     map[string]bool
}

func (r *Runner) resolve(ctx context.Context, op model.Operation, logical model.Input, f *fixture) (model.Input, error) {
	in := model.Clone(logical)
	for _, edge := range r.Plan.Bindings(op.Key) {
		value, present := in.Get(edge.Field)
		if !present {
			continue
		}
		if s, ok := value.(string); !ok || !strings.HasPrefix(s, "@inspector:") {
			continue
		}
		resource, exists := f.resources[edge.Producer]
		if !exists {
			if f.stack[edge.Producer] {
				return in, fmt.Errorf("cyclic fixture dependency %s", edge.Producer)
			}
			f.stack[edge.Producer] = true
			producer, _ := r.Plan.Find(edge.Producer)
			var established bool
			for _, candidate := range r.baselines(producer) {
				actual, err := r.resolve(ctx, producer, candidate, f)
				if err != nil {
					return in, err
				}
				obs, err := r.send(ctx, producer, actual, "setup", "", model.ID(time.Now().UnixNano()))
				res, regErr := r.register(producer, obs, f.parents)
				if err != nil {
					return in, errors.Join(err, regErr)
				}
				if regErr != nil {
					return in, regErr
				}
				if obs.Outcome == "accepted" {
					resource = res
					if resource.ID == "" {
						resource = model.Resource{Operation: producer.Key, Input: actual, Response: obs.Response, State: "borrowed"}
					}
					established = true
					break
				}
				if obs.Outcome == "inconclusive" {
					return in, fmt.Errorf("fixture %s inconclusive: %s", producer.Key, obs.Reason)
				}
			}
			f.stack[edge.Producer] = false
			if !established {
				return in, fmt.Errorf("could not establish fixture for %s; supply hints.values", producer.Key)
			}
			f.resources[edge.Producer] = resource
			if resource.ID != "" {
				f.parents = append(f.parents, resource.ID)
			}
		}
		value, exists = graph.Extract(edge, resource.Input, resource.Response, map[string][]string{"Location": {resource.Location}})
		if !exists {
			return in, fmt.Errorf("producer %s did not return %s; configure a binding", edge.Producer, edge.Pointer)
		}
		if err := in.Set(edge.Field, model.State{Present: true, Value: value}); err != nil {
			return in, err
		}
	}
	return in, nil
}

// Experiment provisions a fresh prerequisite graph. The observation used by
// the learner contains symbolic owned IDs; wire observations remain separate.
func (r *Runner) experiment(ctx context.Context, op model.Operation, logical model.Input, phase, control string) (result model.Observation, experimentErr error) {
	start := len(r.Report.Resources)
	f := &fixture{resources: map[string]model.Resource{}, stack: map[string]bool{}}
	contextID := model.ID(time.Now().UnixNano(), op.Key)
	purpose, baseline, group := model.ProbeInfo(ctx)
	if purpose == "" {
		purpose = phase
	}
	plan := model.ExperimentPlan{ID: contextID, Operation: op.Key, Phase: phase, Purpose: purpose, Input: logical, Baseline: baseline, Control: control, ConfirmationGroup: group}
	plan.OperationIdentity = op.Identity()
	plan.ObservationContext = r.observationSignature(op)
	plan.CaseID = model.ID(plan.OperationIdentity, logical, plan.ObservationContext)
	plan.SpecIdentity = r.Report.SpecIdentity
	plan.Dependencies = spec.Keys(r.dependencyContext(op))
	if baseline != nil {
		plan.ChangedFields = changedFields(*baseline, logical, op)
	}
	if err := r.Journal.Append("experiment-plan", plan); err != nil {
		return model.Observation{}, err
	}
	ctx = model.WithExperiment(ctx, contextID)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), r.Config.Cleanup.Timeout)
		defer cancel()
		if err := r.cleanupFrom(cleanupCtx, start); err != nil {
			r.Report.Warnings = append(r.Report.Warnings, err.Error())
			experimentErr = errors.Join(experimentErr, err)
		}
		end := map[string]any{"id": contextID, "observation": result.ID, "outcome": result.Outcome}
		if experimentErr != nil {
			end["error"] = experimentErr.Error()
		}
		if err := r.Journal.Append("experiment-end", end); err != nil {
			experimentErr = errors.Join(experimentErr, err)
		}
	}()
	actual, err := r.resolve(ctx, op, logical, f)
	if err != nil {
		return model.Observation{}, err
	}
	if graph.Role(op, r.Config) == "update" || graph.Role(op, r.Config) == "delete" {
		ownedFixture := false
		for _, resource := range f.resources {
			if resource.State == "owned" {
				ownedFixture = true
			}
		}
		for _, field := range op.Fields {
			if field.In == "path" {
				_, configured := r.Config.Hint(op).Values[field.ID]
				if (configured || generate.Bound(field)) && !ownedFixture {
					return model.Observation{}, fmt.Errorf("%s needs an owned producer fixture; concrete existing IDs cannot establish isolated update/delete trials", op.Key)
				}
			}
		}
	}
	var before any
	var beforeEvidence string
	if graph.Role(op, r.Config) == "update" {
		for _, owned := range f.resources {
			if owned.ReadOperation == r.Plan.Companion(op, "GET", r.Config) && owned.ReadOperation != "" {
				read, _ := r.Plan.Find(owned.ReadOperation)
				input, e := r.Plan.ResourceInput(read, owned)
				if e == nil {
					previous, e := r.send(ctx, read, input, "read-before", "", contextID)
					if e != nil {
						return previous, e
					}
					if previous.Outcome == "accepted" {
						before = previous.Response
						beforeEvidence = previous.ID
					}
				}
				break
			}
		}
	}
	obs, sendErr := r.send(ctx, op, actual, phase, control, contextID)
	resource, err := r.register(op, obs, f.parents)
	if sendErr != nil || err != nil {
		return obs, errors.Join(sendErr, err)
	}
	if graph.Role(op, r.Config) == "delete" && obs.Outcome == "accepted" {
		for _, owned := range f.resources {
			for i := range r.Report.Resources {
				if r.Report.Resources[i].ID == owned.ID && r.Report.Resources[i].DeleteOperation == op.Key {
					r.Report.Resources[i].State = "deleted"
					if err := r.Journal.Append("resource", r.Report.Resources[i]); err != nil {
						return obs, err
					}
				}
			}
		}
	}
	if obs.Outcome == "accepted" {
		if resource.ID == "" {
			for _, owned := range f.resources {
				if owned.ReadOperation == r.Plan.Companion(op, "GET", r.Config) {
					resource = owned
					break
				}
			}
		}
		if resource.ReadOperation != "" {
			read, _ := r.Plan.Find(resource.ReadOperation)
			input, e := r.Plan.ResourceInput(read, resource)
			if e == nil {
				after, e := r.send(ctx, read, input, "readback", obs.ID, contextID)
				if e != nil {
					return obs, e
				}
				if after.Outcome == "accepted" {
					effect := Effect{Operation: op.Key, Experiment: obs.ID, ReadEvidence: after.ID, BeforeEvidence: beforeEvidence, Input: actual, Before: before, After: after.Response}
					r.Effects = append(r.Effects, effect)
					if e = r.Journal.Append("effect", effect); e != nil {
						return obs, e
					}
				}
			}
		}
	}
	obs.Input = model.Clone(logical)
	if err = r.Journal.Append("experiment", obs); err != nil {
		return obs, err
	}
	if p := r.Progress[op.Key]; p != nil {
		p.Experiments = append(p.Experiments, obs)
	}
	return obs, nil
}

func (r *Runner) send(ctx context.Context, op model.Operation, in model.Input, phase, control, contextID string) (model.Observation, error) {
	obs, err := r.Client.Do(ctx, op, in, phase, control, contextID)
	r.Observations = append(r.Observations, obs)
	if err != nil {
		return obs, err
	}
	if obs.Status != 202 {
		return obs, nil
	}
	poll := r.Config.Hint(op).Poll
	if poll == nil {
		return obs, nil
	}
	target, ok := r.Plan.Find(poll.Operation)
	if !ok {
		return obs, fmt.Errorf("unknown polling operation %s", poll.Operation)
	}
	timeout := poll.Timeout
	if timeout <= 0 {
		timeout = r.Config.Timeout
	}
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	input := generate.Baselines(target, r.Config)[0]
	for field, pointer := range poll.Bindings {
		v, ok := model.Get(obs.Response, pointer)
		if !ok {
			return obs, fmt.Errorf("poll binding %s missing", pointer)
		}
		if err := input.Set(field, model.State{Present: true, Value: v}); err != nil {
			return obs, err
		}
	}
	for {
		next, e := r.Client.Do(pollCtx, target, input, "poll", obs.ID, contextID)
		r.Observations = append(r.Observations, next)
		if e != nil {
			obs.Reason = "completion polling: " + e.Error()
			break
		}
		if next.Outcome != "accepted" {
			obs.Reason = "completion polling was inconclusive"
			break
		}
		value, exists := model.Get(next.Response, poll.StatePointer)
		if !exists {
			obs.Reason = "poll response lacks state pointer"
			break
		}
		finished := false
		for _, v := range poll.Success {
			if model.Equal(v, value) {
				obs.Outcome = "accepted"
				obs.Response = next.Response
				obs.Reason = "configured asynchronous completion"
				finished = true
				break
			}
		}
		for _, v := range poll.Failure {
			if model.Equal(v, value) {
				obs.Outcome = "inconclusive"
				obs.Reason = "asynchronous job failed; failure is not attributed to input"
				finished = true
				break
			}
		}
		if finished {
			break
		}
		if r.Config.Wait == 0 {
			if e := httpclient.Sleep(pollCtx, 10*time.Millisecond); e != nil {
				obs.Reason = e.Error()
				break
			}
		}
	}
	if err = r.Journal.Append("completion", obs); err != nil {
		return obs, err
	}
	return obs, nil
}

func (r *Runner) register(op model.Operation, obs model.Observation, parents []string) (model.Resource, error) {
	if graph.Role(op, r.Config) != "create" {
		return model.Resource{}, nil
	}
	if !obs.Sent {
		return model.Resource{}, nil
	}
	// Rejections are not ownership claims. A transport failure or async result
	// may have created something, so retain it as an ambiguous write instead.
	if obs.Outcome == "input-rejected" {
		return model.Resource{}, nil
	}
	res := model.Resource{ID: obs.ID, Operation: op.Key, Input: obs.Input, Response: obs.Response, Parents: append([]string{}, parents...), ReadOperation: r.Plan.Companion(op, "GET", r.Config), DeleteOperation: r.Plan.Companion(op, "DELETE", r.Config), CreationEvidence: obs.ID, State: "owned"}
	res.ExperimentID = obs.ExperimentID
	for k, vs := range obs.Headers {
		if strings.EqualFold(k, "Location") && len(vs) > 0 {
			res.Location = vs[0]
		}
	}
	if obs.Outcome != "accepted" {
		res.State = "ambiguous"
	}
	r.Report.Resources = append(r.Report.Resources, res)
	return res, r.Journal.Append("resource", res)
}

func (r *Runner) Cleanup(ctx context.Context) error {
	return r.cleanupFrom(ctx, 0)
}

func (r *Runner) cleanupFrom(ctx context.Context, start int) error {
	var failures []error
	if r.cleanupTried == nil {
		r.cleanupTried = map[string]bool{}
	}
	for i := len(r.Report.Resources) - 1; i >= start; i-- {
		res := &r.Report.Resources[i]
		if res.State == "deleted" {
			continue
		}
		if r.cleanupTried[res.ID] {
			failures = append(failures, fmt.Errorf("resource %s remains %s after this run's cleanup allowance", res.ID, res.State))
			continue
		}
		r.cleanupTried[res.ID] = true
		blocked := false
		for _, child := range r.Report.Resources {
			if child.State == "deleted" {
				continue
			}
			for _, parent := range child.Parents {
				if parent == res.ID {
					blocked = true
				}
			}
		}
		if blocked {
			res.State = "leftover"
			res.Errors = append(res.Errors, "dependent resource still requires cleanup")
			if err := r.Journal.Append("resource", res); err != nil {
				return err
			}
			failures = append(failures, fmt.Errorf("resource %s has a remaining dependent", res.ID))
			continue
		}
		if res.State == "ambiguous" {
			failures = append(failures, fmt.Errorf("write %s has an unknown outcome; reconcile it before cleanup", res.ID))
			continue
		}
		if res.DeleteOperation == "" {
			res.State = "leftover"
			res.Errors = append(res.Errors, "no unambiguous cleanup operation; configure hints.delete")
			if err := r.Journal.Append("resource", res); err != nil {
				return err
			}
			failures = append(failures, fmt.Errorf("resource %s has no cleanup operation", res.ID))
			continue
		}
		target, ok := r.Plan.Find(res.DeleteOperation)
		if !ok {
			return fmt.Errorf("missing delete operation %s", res.DeleteOperation)
		}
		input, err := r.Plan.ResourceInput(target, *res)
		if err != nil {
			res.State = "leftover"
			res.Errors = append(res.Errors, err.Error())
			if e := r.Journal.Append("resource", res); e != nil {
				return e
			}
			failures = append(failures, err)
			continue
		}
		for attempt := 0; attempt < r.Config.Cleanup.Attempts; attempt++ {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(failures, err)...)
			}
			if attempt > 0 {
				if err := httpclient.Sleep(ctx, r.Config.Cleanup.Backoff*time.Duration(1<<min(attempt-1, 8))); err != nil {
					return err
				}
			}
			res.Attempts++
			obs, err := r.send(model.WithExperiment(ctx, res.ExperimentID), target, input, "cleanup", "", res.ID)
			if err == nil && (obs.Outcome == "accepted" || obs.Status == 404 || obs.Status == 410) {
				res.State = "deleted"
				break
			}
			reason := obs.Reason
			if err != nil {
				reason = err.Error()
			}
			res.Errors = append(res.Errors, reason)
			res.State = "leftover"
		}
		if err := r.Journal.Append("resource", res); err != nil {
			return err
		}
		if res.State != "deleted" {
			failures = append(failures, fmt.Errorf("cleanup failed for %s", res.ID))
		}
	}
	return errors.Join(failures...)
}
