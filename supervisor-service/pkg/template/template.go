// Package template provides hostname template parsing and generation.
//
// Template syntax:
//   - '#' characters are placeholder for digits (0-9)
//   - The number of consecutive '#' determines the digit width
//   - Example: "vm-###" with index 5 => "vm-005"
//   - Example: "node#" with index 42 => "node42"
//   - Example: "prefix-##-suffix" with index 3 => "prefix-03-suffix"
package template

import (
	"fmt"
	"strings"
)

// HostnameGenerator handles hostname template parsing and generation.
type HostnameGenerator struct {
	template    string
	digitCount  int
	prefix      string
	suffix      string
	counterInit int
}

// NewHostnameGenerator creates a new HostnameGenerator from a template string.
// The template must contain at least one '#' character.
func NewHostnameGenerator(tmpl string, counterStart int) (*HostnameGenerator, error) {
	if tmpl == "" {
		return nil, fmt.Errorf("hostname template cannot be empty")
	}

	hashCount := strings.Count(tmpl, "#")
	if hashCount == 0 {
		return nil, fmt.Errorf("hostname template %q must contain at least one '#' character", tmpl)
	}

	// Find the consecutive # sequence
	hashStart := strings.Index(tmpl, "#")
	hashEnd := hashStart
	for hashEnd < len(tmpl) && tmpl[hashEnd] == '#' {
		hashEnd++
	}
	digitCount := hashEnd - hashStart

	// Verify all # are consecutive
	if strings.Count(tmpl, "#") != digitCount {
		return nil, fmt.Errorf("hostname template %q must have a single, consecutive '#' run", tmpl)
	}

	return &HostnameGenerator{
		template:    tmpl,
		digitCount:  digitCount,
		prefix:      tmpl[:hashStart],
		suffix:      tmpl[hashEnd:],
		counterInit: counterStart,
	}, nil
}

// Generate creates a hostname from the template using the given index.
// The index is zero-based and added to the counter start.
func (g *HostnameGenerator) Generate(index int) string {
	absIndex := index + g.counterInit
	return g.prefix + padNumber(absIndex, g.digitCount) + g.suffix
}

// GenerateFromIndex creates a hostname using the raw index value.
func (g *HostnameGenerator) GenerateFromIndex(index int) string {
	return g.prefix + padNumber(index, g.digitCount) + g.suffix
}

// padNumber pads a number with leading zeros to reach the specified width.
func padNumber(n, width int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) >= width {
		return s
	}
	return strings.Repeat("0", width-len(s)) + s
}

// DigitCount returns the number of digit placeholders in the template.
func (g *HostnameGenerator) DigitCount() int {
	return g.digitCount
}

// Prefix returns the part of the template before the digit placeholders.
func (g *HostnameGenerator) Prefix() string {
	return g.prefix
}

// Suffix returns the part of the template after the digit placeholders.
func (g *HostnameGenerator) Suffix() string {
	return g.suffix
}

// Template returns the original template string.
func (g *HostnameGenerator) Template() string {
	return g.template
}

// CounterStart returns the starting index for counters.
func (g *HostnameGenerator) CounterStart() int {
	return g.counterInit
}

// ExtractIndex extracts the numeric index portion from a generated hostname.
// Returns 0 and false if the hostname doesn't match this template.
func (g *HostnameGenerator) ExtractIndex(hostname string) (int, bool) {
	if !strings.HasPrefix(hostname, g.prefix) || !strings.HasSuffix(hostname, g.suffix) {
		return 0, false
	}

	digitStr := hostname[len(g.prefix) : len(hostname)-len(g.suffix)]
	if len(digitStr) != g.digitCount {
		return 0, false
	}

	// Verify all characters are digits
	for _, c := range digitStr {
		if c < '0' || c > '9' {
			return 0, false
		}
	}

	var n int
	fmt.Sscanf(digitStr, "%d", &n)
	return n, true
}
