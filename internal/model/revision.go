package model

// SpecIdentity separates exact source bytes from the bundled document's meaning.
type SpecIdentity struct {
	Algorithm string `json:"algorithm"`
	SHA256    string `json:"sha256,omitempty"`
	Canonical string `json:"canonical"`
	Dialect   string `json:"dialect"`
	Version   string `json:"version,omitempty"`
	Release   string `json:"release,omitempty"`
}

// EvidenceOrigin always names the run in which the request was actually sent.
type EvidenceOrigin struct {
	RunID     string       `json:"runId"`
	Spec      SpecIdentity `json:"spec"`
	Operation string       `json:"operation"`
}

type OperationDelta struct {
	Identity    string   `json:"identity"`
	Operation   string   `json:"operation"`
	Previous    string   `json:"previous,omitempty"`
	Change      string   `json:"change"`
	Action      string   `json:"action"`
	Selected    bool     `json:"selected"`
	Reasons     []string `json:"reasons,omitempty"`
	ReusedCases int      `json:"reusedCases"`
	ReusedPairs int      `json:"reusedPairs"`
}

type Lineage struct {
	RunID                 string           `json:"runId"`
	JournalHash           string           `json:"journalHash"`
	Spec                  SpecIdentity     `json:"spec"`
	Operations            []OperationDelta `json:"operations"`
	InheritedObservations int              `json:"inheritedObservations"`
	Assumption            string           `json:"assumption"`
}
