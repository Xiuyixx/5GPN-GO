// Benchmark environment note:
//   AC-N5 gates BuildTable([]rules.Rule) with 100k rules under 500ms on the
//   reference runner. "Reference runner" per plan §6.4 baseline discipline:
//   1 vCPU / 1 GiB RAM VPS class, Go 1.25+. Bench runs record GOMAXPROCS +
//   Go version so a fail on a beefy CI box can be distinguished from a
//   fail on the intended 1-core deployment target.

package resolver

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// build100kRules synthesizes a realistic filter-list-sized ruleset: 60k
// suffix rules (adblock-style), 30k exact-domain rules, 10k keyword rules.
// Priority is spread across the range so the sort in BuildTable is exercised.
func build100kRules() []rules.Rule {
	out := make([]rules.Rule, 0, 100_000)
	for i := 0; i < 60_000; i++ {
		out = append(out, rules.Rule{
			ID: fmt.Sprintf("sfx-%d", i), Kind: rules.KindDomainSuffix,
			Pattern: fmt.Sprintf("ad-%d.example.com", i), Action: "block",
			Priority: 100 + i, Enabled: true,
		})
	}
	for i := 0; i < 30_000; i++ {
		out = append(out, rules.Rule{
			ID: fmt.Sprintf("exact-%d", i), Kind: rules.KindDomain,
			Pattern: fmt.Sprintf("host%d.corp.example.net", i), Action: "direct",
			Priority: 200 + i, Enabled: true,
		})
	}
	for i := 0; i < 10_000; i++ {
		out = append(out, rules.Rule{
			ID: fmt.Sprintf("kw-%d", i), Kind: rules.KindDomainKeyword,
			Pattern: fmt.Sprintf("tracker%d", i), Action: "block",
			Priority: 300 + i, Enabled: true,
		})
	}
	return out
}

// BenchmarkBuildTable100k gates AC-N5. Log GOMAXPROCS + Go version so a
// failure on unexpected hardware is investigable from the bench output.
func BenchmarkBuildTable100k(b *testing.B) {
	rs := build100kRules()
	b.Logf("runtime: %s, GOMAXPROCS=%d, GOOS=%s/%s", runtime.Version(), runtime.GOMAXPROCS(0), runtime.GOOS, runtime.GOARCH)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := BuildTable(rs)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkClassify measures the hot-path lookup with a fully-loaded
// table, so the AC-N1 QPS ≥ 500 target on 1 core is provable from this
// number alone (500 QPS = 2ms per query budget, of which classify() should
// consume << 100µs).
func BenchmarkClassify(b *testing.B) {
	tbl, err := BuildTable(build100kRules())
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		classify(tbl, "ad-42.example.com")
	}
}
