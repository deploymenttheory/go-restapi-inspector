package learn

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/crillab/gophersat/solver"
	"github.com/deploymenttheory/go-restapi-inspector/internal/generate"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
)

var ErrOutsideModel = errors.New("observations cannot be explained by the candidate language")

type Learner struct {
	Rules        []model.Rule
	Observations []model.Observation
	// A selector means that a candidate belongs to the unknown constraint network.
	clauses  [][]int
	disabled map[int]bool
}

func New(rules []model.Rule) *Learner { return &Learner{Rules: rules, disabled: map[int]bool{}} }
func (l *Learner) Observe(o model.Observation) error {
	l.Observations = append(l.Observations, o)
	if o.Outcome != "accepted" && o.Outcome != "input-rejected" {
		return nil
	}
	var violated []int
	for i, r := range l.Rules {
		if !r.Holds(o.Input) {
			violated = append(violated, i+1)
			if o.Outcome == "accepted" {
				l.disabled[i] = true
				l.clauses = append(l.clauses, []int{-(i + 1)})
			}
		}
	}
	if o.Outcome == "input-rejected" {
		l.clauses = append(l.clauses, violated)
	}
	if !l.Consistent() {
		return ErrOutsideModel
	}
	return nil
}
func (l *Learner) Consistent() bool {
	for _, clause := range l.clauses {
		if len(clause) == 0 {
			return false
		}
		if clause[0] < 0 {
			continue
		}
		possible := false
		for _, v := range clause {
			if !l.disabled[v-1] {
				possible = true
				break
			}
		}
		if !possible {
			return false
		}
	}
	return true
}
func (l *Learner) Survives(i int) bool { return !l.disabled[i] }

// Contrast generates a matched pair when model discrimination did not happen
// to observe one. The control is accepted by a consistent network; the other
// input violates the entailed rule. Only conclusion fields may differ.
func (l *Learner) Contrast(ctx context.Context, index int, base model.Input, domains []model.Domain, limit int) (model.Input, model.Input, bool, error) {
	var empty model.Input
	extended := *l
	extended.Rules = append([]model.Rule{}, l.Rules...)
	rule := l.Rules[index]
	if rule.When != nil {
		extended.Rules = append(extended.Rules, model.Rule{Assert: *rule.When})
	}
	good, gs, gt, err := extended.encoding(ctx, base, domains, limit)
	if err != nil {
		return empty, empty, false, err
	}
	bad, bs, bt, err := extended.encoding(ctx, base, domains, limit)
	if err != nil {
		return empty, empty, false, err
	}
	network := l.network(good)
	for i, v := range network {
		good.add(-v, gt[i])
	}
	if rule.When != nil {
		good.add(gt[len(gt)-1])
	}
	offset := good.n
	good.n += bad.n
	if good.n > limit {
		return empty, empty, false, fmt.Errorf("contrast encoding exceeds planning guard")
	}
	shift := func(v int) int {
		if v < 0 {
			return v - offset
		}
		return v + offset
	}
	for _, cl := range bad.clauses {
		mapped := make([]int, len(cl))
		for i, v := range cl {
			mapped[i] = shift(v)
		}
		good.add(mapped...)
	}
	good.add(-shift(bt[index]))
	allowed := map[string]bool{}
	for _, field := range rule.Assert.Fields() {
		allowed[field] = true
	}
	for i, d := range domains {
		if allowed[d.Field.ID] {
			continue
		}
		for j, v := range gs[i] {
			b := shift(bs[i][j])
			good.add(-v, b)
			good.add(v, -b)
		}
	}
	solution, found, err := good.solve(ctx)
	if err != nil || !found {
		return empty, empty, found, err
	}
	grow, brow := make([]int, len(domains)), make([]int, len(domains))
	for i := range domains {
		for j, v := range gs[i] {
			if solution[v-1] {
				grow[i] = j
			}
		}
		for j, v := range bs[i] {
			if solution[shift(v)-1] {
				brow[i] = j
			}
		}
	}
	gin, err := generate.Materialize(base, domains, grow)
	if err != nil {
		return empty, empty, false, nil
	}
	bin, err := generate.Materialize(base, domains, brow)
	if err != nil {
		return empty, empty, false, nil
	}
	return gin, bin, true, nil
}

// Witness encodes two networks consistent with all classified observations and
// a single full request accepted by the first and rejected by the second. UNSAT
// means agreement only over these domains and this candidate language.
func (l *Learner) Witness(ctx context.Context, base model.Input, domains []model.Domain, blocked [][]int, limit int) ([]int, bool, error) {
	if !l.Consistent() {
		return nil, false, ErrOutsideModel
	}
	enc, states, truth, err := l.encoding(ctx, base, domains, limit)
	if err != nil {
		return nil, false, err
	}
	a := l.network(enc)
	b := l.network(enc)
	var rejected []int
	for i, t := range truth {
		enc.add(-a[i], t)
		rejected = append(rejected, enc.and(b[i], -t))
	}
	enc.add(rejected...)
	for _, row := range blocked {
		if len(row) != len(states) {
			continue
		}
		cl := make([]int, len(row))
		valid := true
		for i, v := range row {
			if v < 0 || v >= len(states[i]) {
				valid = false
				break
			}
			cl[i] = -states[i][v]
		}
		if valid {
			enc.add(cl...)
		}
	}
	solution, sat, err := enc.solve(ctx)
	if err != nil || !sat {
		return nil, sat, err
	}
	row := make([]int, len(states))
	for i, vs := range states {
		for j, v := range vs {
			if solution[v-1] {
				row[i] = j
				break
			}
		}
	}
	return row, true, nil
}

// Entailed asks whether any remaining network accepts a request violating r.
// Unlike testing a selector's value, this also recognizes redundant rules.
func (l *Learner) Entailed(ctx context.Context, index int, base model.Input, domains []model.Domain, limit int) (bool, error) {
	if !l.Consistent() {
		return false, ErrOutsideModel
	}
	if l.disabled[index] {
		return false, nil
	}
	enc, _, truth, err := l.encoding(ctx, base, domains, limit)
	if err != nil {
		return false, err
	}
	vars := l.network(enc)
	for i, t := range truth {
		enc.add(-vars[i], t)
	}
	enc.add(-truth[index])
	_, sat, err := enc.solve(ctx)
	return !sat, err
}
func (l *Learner) network(e *encoding) []int {
	vs := make([]int, len(l.Rules))
	for i := range vs {
		vs[i] = e.variable()
	}
	for _, cl := range l.clauses {
		mapped := make([]int, len(cl))
		for j, v := range cl {
			if v < 0 {
				mapped[j] = -vs[-v-1]
			} else {
				mapped[j] = vs[v-1]
			}
		}
		e.add(mapped...)
	}
	return vs
}

type encoding struct {
	n, limit int
	clauses  [][]int
	err      error
	cache    map[string]int
}

func (e *encoding) variable() int {
	e.n++
	if e.n > e.limit && e.err == nil {
		e.err = fmt.Errorf("SAT encoding exceeds planning guard (%d variables)", e.limit)
	}
	return e.n
}
func (e *encoding) add(cl ...int) {
	e.clauses = append(e.clauses, append([]int{}, cl...))
	if len(e.clauses) > e.limit && e.err == nil {
		e.err = fmt.Errorf("SAT encoding exceeds planning guard (%d clauses)", e.limit)
	}
}
func (e *encoding) and(args ...int) int {
	v := e.variable()
	cl := []int{v}
	for _, a := range args {
		e.add(-v, a)
		cl = append(cl, -a)
	}
	e.add(cl...)
	return v
}
func (e *encoding) or(args ...int) int {
	v := e.variable()
	cl := []int{-v}
	for _, a := range args {
		e.add(v, -a)
		cl = append(cl, a)
	}
	e.add(cl...)
	return v
}
func (e *encoding) constant(b bool) int {
	v := e.variable()
	if b {
		e.add(v)
	} else {
		e.add(-v)
	}
	return v
}
func (e *encoding) solve(ctx context.Context) ([]bool, bool, error) {
	if e.err != nil {
		return nil, false, e.err
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s := solver.New(solver.ParseSliceNb(e.clauses, e.n))
	status := s.Solve()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if status == solver.Unsat {
		return nil, false, nil
	}
	if status != solver.Sat {
		return nil, false, fmt.Errorf("solver returned %v, not a convergence proof", status)
	}
	return s.Model(), true, nil
}

func (l *Learner) encoding(ctx context.Context, base model.Input, domains []model.Domain, limit int) (*encoding, [][]int, []int, error) {
	e := &encoding{limit: limit, cache: map[string]int{}}
	states := make([][]int, len(domains))
	index := map[string]int{}
	for i, d := range domains {
		index[d.Field.ID] = i
		for range d.States {
			states[i] = append(states[i], e.variable())
		}
		e.add(states[i]...)
		for a, v := range states[i] {
			for _, w := range states[i][a+1:] {
				e.add(-v, -w)
			}
		}
	}
	// A child cannot be present under an omitted, null or scalar parent.
	for i, d := range domains {
		for j, p := range domains {
			if d.Field.In != "body" || p.Field.In != "body" || !strings.HasPrefix(d.Field.Pointer, p.Field.Pointer+"/") {
				continue
			}
			for a, s := range d.States {
				if !s.Present {
					continue
				}
				for b, t := range p.States {
					if !t.Present || model.Type(t.Value) != "object" && model.Type(t.Value) != "array" {
						e.add(-states[i][a], -states[j][b])
					}
				}
			}
		}
	}
	var predicate func(model.Predicate) (int, error)
	predicate = func(p model.Predicate) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		key := model.ID(p)
		if v, ok := e.cache[key]; ok {
			return v, nil
		}
		var v int
		switch p.Op {
		case "not":
			if len(p.Args) != 1 {
				return 0, fmt.Errorf("not predicate needs one argument")
			}
			a, err := predicate(p.Args[0])
			if err != nil {
				return 0, err
			}
			v = -a
		case "all", "any", "one", "atMostOne", "allOrNone":
			var args []int
			for _, a := range p.Args {
				x, err := predicate(a)
				if err != nil {
					return 0, err
				}
				args = append(args, x)
			}
			switch p.Op {
			case "all":
				v = e.and(args...)
			case "any":
				v = e.or(args...)
			case "allOrNone":
				neg := make([]int, len(args))
				for i, a := range args {
					neg[i] = -a
				}
				v = e.or(e.and(args...), e.and(neg...))
			default:
				var pairs []int
				for i, a := range args {
					for _, b := range args[i+1:] {
						pairs = append(pairs, e.or(-a, -b))
					}
				}
				if p.Op == "one" {
					pairs = append(pairs, e.or(args...))
				}
				v = e.and(pairs...)
			}
		default:
			fields := p.Fields()
			var dims []int
			var sizes []int
			for _, f := range fields {
				if i, ok := index[f]; ok {
					dims = append(dims, i)
					sizes = append(sizes, len(domains[i].States))
				}
			}
			var terms []int
			count := 0
			err := generate.Cartesian(ctx, sizes, func(row []int) error {
				count++
				if count > limit {
					return fmt.Errorf("predicate truth table exceeds planning guard")
				}
				in := model.Clone(base)
				var conjunction []int
				for j, i := range dims {
					s := domains[i].States[row[j]]
					if err := in.Set(domains[i].Field.ID, s); err != nil {
						return nil
					}
					conjunction = append(conjunction, states[i][row[j]])
				}
				if p.Eval(in) {
					terms = append(terms, e.and(conjunction...))
				}
				return e.err
			})
			if err != nil {
				return 0, err
			}
			v = e.or(terms...)
		}
		e.cache[key] = v
		return v, e.err
	}
	truth := make([]int, len(l.Rules))
	for i, r := range l.Rules {
		if l.disabled[i] {
			truth[i] = e.constant(true)
			continue
		}
		v, err := predicate(r.Assert)
		if err != nil {
			return nil, nil, nil, err
		}
		if r.When != nil {
			w, err := predicate(*r.When)
			if err != nil {
				return nil, nil, nil, err
			}
			v = e.or(-w, v)
		}
		truth[i] = v
	}
	return e, states, truth, e.err
}
