package exporter

import (
	"strconv"
	"strings"

	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

// integerRangePattern partitions a finite signed interval into decimal prefix
// blocks. Its size grows with the digit count rather than the interval width.
func integerRangePattern(value any) (string, bool) {
	bounds := spec.Map(value)
	lower, lok := model.Number(bounds["minimum"])
	upper, uok := model.Number(bounds["maximum"])
	if !lok || !uok || !lower.IsInt() || !upper.IsInt() || !lower.Num().IsInt64() || !upper.Num().IsInt64() {
		return "", false
	}
	low, high := lower.Num().Int64(), upper.Num().Int64()
	if low > high || low < -(1<<40) || high > 1<<40 {
		return "", false
	}
	var branches []string
	if high >= 1 {
		branches = append(branches, `\+?0*(?:`+unsignedInterval(max(1, low), high)+`)`)
	}
	if low <= -1 {
		branches = append(branches, `-0*(?:`+unsignedInterval(max(1, -high), -low)+`)`)
	}
	if low <= 0 && high >= 0 {
		branches = append(branches, `[+-]?0+`)
	}
	return `^\s*(?:` + strings.Join(branches, "|") + `)\s*$`, len(branches) > 0
}

func unsignedInterval(low, high int64) string {
	var parts []string
	for low <= high {
		block, digits := int64(1), 0
		for low%(block*10) == 0 && block*10 <= high-low+1 {
			block *= 10
			digits++
		}
		prefix := strconv.FormatInt(low/block, 10)
		if digits > 0 {
			prefix += "[0-9]{" + strconv.Itoa(digits) + "}"
		}
		parts = append(parts, prefix)
		low += block
	}
	return strings.Join(parts, "|")
}
