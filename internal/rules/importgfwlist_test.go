package rules

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestImportGFWListPlainBody(t *testing.T) {
	input := `[AutoProxy 0.2.9]
! Checksum: xxx
! Title: gfwlist

||twitter.com
||facebook.com^
||fbcdn.net/path
.example.com
*.wildcard.example
|http://direct.example.com/path
@@||whitelist.example^
example-keyword.com
`
	out, rep := Import(input, ImportLegacyOptions{})
	joined := strings.Join(out, "\n")
	// Structural checks — order does not matter for this test.
	for _, want := range []string{
		"DOMAIN-SUFFIX,twitter.com,",
		"DOMAIN-SUFFIX,facebook.com,",
		"DOMAIN-SUFFIX,fbcdn.net,",
		"DOMAIN-SUFFIX,example.com,",
		"DOMAIN-SUFFIX,wildcard.example,",
		"DOMAIN,direct.example.com,",
		"DOMAIN-SUFFIX,whitelist.example,direct",
		"DOMAIN-KEYWORD,example-keyword.com,",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected substring %q in output:\n%s", want, joined)
		}
	}
	if rep.Converted == 0 {
		t.Fatalf("expected Converted > 0, got 0")
	}
	// gfwlist tag should appear on the report so callers can distinguish.
	found := false
	for _, c := range rep.Categories {
		if c == "gfwlist" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'gfwlist' in Categories, got %v", rep.Categories)
	}
}

func TestImportGFWListBase64Wrapped(t *testing.T) {
	body := `[AutoProxy 0.2.9]
! Title: gfwlist
||twitter.com
||facebook.com
||google.com
`
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	// Wrap at 76 cols like gfwlist raw does.
	var wrapped strings.Builder
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		wrapped.WriteString(encoded[i:end])
		wrapped.WriteByte('\n')
	}

	out, rep := Import(wrapped.String(), ImportLegacyOptions{})
	joined := strings.Join(out, "\n")
	for _, want := range []string{"twitter.com", "facebook.com", "google.com"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected %q in decoded gfwlist output:\n%s", want, joined)
		}
	}
	if rep.Converted < 3 {
		t.Errorf("expected >=3 converted, got %d", rep.Converted)
	}
}

func TestImportGFWListSkipsRegex(t *testing.T) {
	input := `[AutoProxy 0.2.9]
||twitter.com
/^https?:\/\/[^/]*\.example\.com\/regex/
`
	out, _ := Import(input, ImportLegacyOptions{})
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "regex") {
		t.Errorf("regex line leaked into output: %s", joined)
	}
	if !strings.Contains(joined, "twitter.com") {
		t.Errorf("valid line dropped alongside regex")
	}
}

func TestImportAutoDetectClashPassthrough(t *testing.T) {
	// Clash input should pass through unchanged and NOT get gfwlist tag.
	input := `DOMAIN-SUFFIX,twitter.com,PROXY
DOMAIN,fbcdn.net,PROXY
IP-CIDR,1.1.1.1/32,DIRECT
`
	out, rep := Import(input, ImportLegacyOptions{})
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "twitter.com") {
		t.Errorf("expected twitter.com in Clash passthrough output")
	}
	for _, c := range rep.Categories {
		if c == "gfwlist" {
			t.Errorf("Clash input incorrectly tagged as gfwlist: categories=%v", rep.Categories)
		}
	}
}
