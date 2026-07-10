package api

import (
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestMain lowers the bcrypt cost for the entire package to
// bcrypt.MinCost. In production HashPassword runs at cost 12
// (~500ms per hash), which multiplied across every bootstrap+login
// in this suite would push the test wall-clock past any reasonable
// CI budget once -race overhead is added. Correctness is unchanged;
// only the work factor is tuned.
func TestMain(m *testing.M) {
	bcryptCost = bcrypt.MinCost
	os.Exit(m.Run())
}
