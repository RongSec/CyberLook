package svcid

import (
	"regexp"
	"testing"
)

func TestRegexCompileCoverage(t *testing.T) {
	rs, err := Load("../../data/fingerprint")
	if err != nil { t.Fatal(err) }
	total, ok, fail := 0, 0, 0
	samples := []string{}
	for _, r := range rs.Service {
		for _, it := range r.TCP {
			for _, ex := range it.Extractors {
				for _, p := range ex.Regex {
					total++
					if _, e := regexp.Compile("(?i)" + p); e != nil {
						fail++
						if len(samples) < 5 { samples = append(samples, p[:min2(len(p), 90)]+" | "+e.Error()) }
					} else { ok++ }
				}
			}
		}
	}
	t.Logf("service regex total=%d ok=%d fail=%d", total, ok, fail)
	for _, s := range samples { t.Logf("  FAIL: %s", s) }
}

func min2(a, b int) int { if a < b { return a }; return b }
