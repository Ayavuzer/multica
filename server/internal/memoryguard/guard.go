package memoryguard

import (
	"regexp"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/pkg/redact"
)

type Finding struct {
	Type string `json:"type"`
}

type Report struct {
	Allowed  bool      `json:"allowed"`
	Version  string    `json:"version"`
	Findings []Finding `json:"findings,omitempty"`
}

var (
	emailRe = regexp.MustCompile(`(?i)\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}\b`)
	ssnRe   = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)
	phoneRe = regexp.MustCompile(`(?i)(?:\+90|0)?\s?5\d{2}[\s.-]?\d{3}[\s.-]?\d{2}[\s.-]?\d{2}\b`)
	ibanRe  = regexp.MustCompile(`(?i)\bTR\d{2}[0-9A-Z]{5}[0-9A-Z]{17}\b`)
	tcknRe  = regexp.MustCompile(`\b[1-9]\d{10}\b`)

	jwtRe       = regexp.MustCompile(`\bey[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)
	localPathRe = regexp.MustCompile(`(?i)(?:/Users/[A-Za-z0-9._-]+/|/home/[A-Za-z0-9._-]+/|[A-Z]:\\Users\\[^\\]+\\)`)
)

const Version = "security-kvkk-guard@2026-04-26"

func Inspect(parts ...string) Report {
	text := strings.Join(parts, "\n")
	types := map[string]bool{}

	if redact.Text(text) != text {
		types["secret"] = true
	}
	if emailRe.MatchString(text) {
		types["email"] = true
	}
	if ssnRe.MatchString(text) {
		types["ssn"] = true
	}
	if phoneRe.MatchString(text) {
		types["phone"] = true
	}
	if ibanRe.MatchString(text) {
		types["iban"] = true
	}
	for _, match := range tcknRe.FindAllString(text, -1) {
		if validTCKN(match) {
			types["tckn"] = true
			break
		}
	}
	if containsLuhnNumber(text) {
		types["payment_card"] = true
	}
	if jwtRe.MatchString(text) {
		types["jwt"] = true
	}
	if localPathRe.MatchString(text) {
		types["local_path"] = true
	}
	if looksLikePromptInjection(text) {
		types["prompt_injection"] = true
	}

	findings := make([]Finding, 0, len(types))
	for typ := range types {
		findings = append(findings, Finding{Type: typ})
	}
	sort.Slice(findings, func(i, j int) bool {
		return findings[i].Type < findings[j].Type
	})

	return Report{
		Allowed:  len(findings) == 0,
		Version:  Version,
		Findings: findings,
	}
}

func FindingTypes(findings []Finding) []string {
	types := make([]string, 0, len(findings))
	for _, finding := range findings {
		types = append(types, finding.Type)
	}
	return types
}

func validTCKN(s string) bool {
	if len(s) != 11 || s[0] == '0' {
		return false
	}
	digits := make([]int, 11)
	for i, r := range s {
		if r < '0' || r > '9' {
			return false
		}
		digits[i] = int(r - '0')
	}
	odd := digits[0] + digits[2] + digits[4] + digits[6] + digits[8]
	even := digits[1] + digits[3] + digits[5] + digits[7]
	check10 := ((odd * 7) - even) % 10
	if check10 < 0 {
		check10 += 10
	}
	check11 := 0
	for i := 0; i < 10; i++ {
		check11 += digits[i]
	}
	return digits[9] == check10 && digits[10] == check11%10
}

func containsLuhnNumber(s string) bool {
	var digits []int
	flush := func() bool {
		if len(digits) < 13 || len(digits) > 19 {
			digits = digits[:0]
			return false
		}
		ok := validLuhn(digits)
		digits = digits[:0]
		return ok
	}

	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits = append(digits, int(r-'0'))
		case r == ' ' || r == '-':
			continue
		default:
			if flush() {
				return true
			}
		}
	}
	return flush()
}

func validLuhn(digits []int) bool {
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		n := digits[i]
		if double {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		double = !double
	}
	return sum%10 == 0
}

func looksLikePromptInjection(s string) bool {
	normalized := strings.ToLower(s)
	normalized = strings.Join(strings.Fields(normalized), " ")
	phrases := []string{
		"ignore previous instructions",
		"ignore all previous instructions",
		"disregard previous instructions",
		"disregard all previous instructions",
		"forget previous instructions",
		"reveal your system prompt",
		"show your system prompt",
		"bypass guardrails",
		"disable guardrails",
		"jailbreak",
		"developer message",
	}
	for _, phrase := range phrases {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}
