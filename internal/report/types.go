// Package report renders portable HTML from recorded inspection evidence.
package report

import (
	"time"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
)

const Filename = "report.html"
const ComparisonFilename = "comparison.html"

type View struct {
	Version    int         `json:"version"`
	Current    Run         `json:"current"`
	Baseline   *Run        `json:"baseline,omitempty"`
	Comparison *Comparison `json:"comparison,omitempty"`
}
type Run struct {
	ID            string              `json:"id"`
	Snapshot      string              `json:"snapshot"`
	Target        string              `json:"target"`
	SpecHash      string              `json:"specHash"`
	State         string              `json:"state"`
	Started       string              `json:"started"`
	Finished      string              `json:"finished"`
	LatestSession string              `json:"latestSession"`
	Metrics       Metrics             `json:"metrics"`
	Phases        map[string]int      `json:"phases"`
	LatestPhases  map[string]int      `json:"latestPhases"`
	Operations    []Operation         `json:"operations"`
	Rules         []Rule              `json:"rules"`
	Experiments   []Experiment        `json:"experiments"`
	Requests      []Request           `json:"requests"`
	Evidence      map[string]Evidence `json:"evidence"`
	Changes       []Change            `json:"changes"`
	Resources     []Resource          `json:"resources"`
	Warnings      []string            `json:"warnings"`
	Contract      string              `json:"contract"`
	Source        string              `json:"source"`
	domains       string
}
type Metrics struct {
	Inventory     int  `json:"inventory"`
	Selected      int  `json:"selected"`
	Discovery     *int `json:"discovery"`
	Confirmations int  `json:"confirmations"`
	Controls      int  `json:"controls"`
	HTTP          int  `json:"http"`
	Supported     int  `json:"supported"`
	Unresolved    int  `json:"unresolved"`
	Deleted       int  `json:"deleted"`
	Resources     int  `json:"resources"`
	Unlinked      int  `json:"unlinked"`
}
type Operation struct {
	Key                 string   `json:"key"`
	Method              string   `json:"method"`
	Path                string   `json:"path"`
	State               string   `json:"state"`
	Role                string   `json:"role"`
	Related             []string `json:"related"`
	Coverage            string   `json:"coverage"`
	ModelConverged      bool     `json:"modelConverged"`
	InteractionComplete bool     `json:"interactionComplete"`
	InputComplete       bool     `json:"inputComplete"`
	Reasons             []string `json:"reasons"`
	Fields              []Field  `json:"fields"`
	Before              string   `json:"before"`
	After               string   `json:"after"`
}
type Field struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Declared string   `json:"declared"`
	Schema   string   `json:"schema"`
	Rules    []string `json:"rules"`
	Samples  []Sample `json:"samples"`
}
type Sample struct {
	Value    string   `json:"value"`
	Outcome  string   `json:"outcome"`
	Evidence []string `json:"evidence"`
}
type Rule struct {
	ID        string   `json:"id"`
	Operation string   `json:"operation"`
	Field     string   `json:"field"`
	Fields    []string `json:"fields"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Summary   string   `json:"summary"`
	Trials    string   `json:"trials"`
	Evidence  []string `json:"evidence"`
	JSON      string   `json:"json"`
	Implied   bool     `json:"implied"`
	Relation  string   `json:"relation"`
	semantic  string
	family    string
}
type Experiment struct {
	ID          string   `json:"id"`
	Operation   string   `json:"operation"`
	Phase       string   `json:"phase"`
	Purpose     string   `json:"purpose"`
	Outcome     string   `json:"outcome"`
	Observation string   `json:"observation"`
	Control     string   `json:"control"`
	Group       string   `json:"group"`
	Changed     []string `json:"changed"`
	Input       string   `json:"input"`
	Baseline    string   `json:"baseline"`
	Requests    []string `json:"requests"`
	Provenance  string   `json:"provenance"`
	Error       string   `json:"error"`
}
type Request struct {
	ID         string `json:"id"`
	Evidence   string `json:"evidence"`
	Experiment string `json:"experiment"`
	Operation  string `json:"operation"`
	Method     string `json:"method"`
	Phase      string `json:"phase"`
	Started    string `json:"started"`
	Outcome    string `json:"outcome"`
	Status     int    `json:"status"`
}
type Evidence struct {
	ID             string `json:"id"`
	Operation      string `json:"operation"`
	Phase          string `json:"phase"`
	Outcome        string `json:"outcome"`
	Status         int    `json:"status"`
	Sent           bool   `json:"sent"`
	Control        string `json:"control"`
	Input          string `json:"input"`
	Response       string `json:"response"`
	Headers        string `json:"headers"`
	Completion     string `json:"completion,omitempty"`
	Reason         string `json:"reason"`
	Started        string `json:"started"`
	Duration       string `json:"duration"`
	Before         string `json:"before,omitempty"`
	After          string `json:"after,omitempty"`
	BeforeEvidence string `json:"beforeEvidence,omitempty"`
	AfterEvidence  string `json:"afterEvidence,omitempty"`
}
type Change struct {
	Operation   string   `json:"operation"`
	Field       string   `json:"field"`
	Status      string   `json:"status"`
	Description string   `json:"description"`
	Rule        string   `json:"rule"`
	Evidence    []string `json:"evidence"`
}
type Resource struct {
	ID        string   `json:"id"`
	Operation string   `json:"operation"`
	State     string   `json:"state"`
	Attempts  int      `json:"attempts"`
	Errors    []string `json:"errors"`
	Evidence  string   `json:"evidence"`
}
type Comparison struct {
	ContextDifferences []string         `json:"contextDifferences"`
	Findings           []FindingChange  `json:"findings"`
	Coverage           []CoverageChange `json:"coverage"`
	SameSnapshot       bool             `json:"sameSnapshot"`
}
type FindingChange struct {
	Operation  string `json:"operation"`
	Field      string `json:"field"`
	Status     string `json:"status"`
	Before     string `json:"before"`
	After      string `json:"after"`
	BeforeRule string `json:"beforeRule"`
	AfterRule  string `json:"afterRule"`
}
type CoverageChange struct {
	Operation string `json:"operation"`
	Before    string `json:"before"`
	After     string `json:"after"`
}
type recording struct {
	Config    config.Config
	Document  map[string]any
	Report    model.Report
	wire      map[string]model.Observation
	completed map[string]model.Observation
	logical   map[string]model.Observation
	plans     map[string]model.ExperimentPlan
	ends      map[string]struct{ Observation, Outcome, Error string }
	effects   map[string]struct {
		Before, After                any
		BeforeEvidence, ReadEvidence string
	}
	resources           map[string]model.Resource
	requests            []Request
	lastSession         time.Time
	lastSessionSequence int
	latestPhases        map[string]int
	identity            string
	hasReport           bool
	reportSequence      int
}
