package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

// FieldDef describes one column/field from the config file
type FieldDef struct {
	Name  string // example: "ID", "Name", "List1"
	Type  string // example: "int", "string", "[]string"
	Label string // display label (currently same as Name)
}

func loadConfig(path string) ([]FieldDef, error) {
	// If a JSON file exists, prefer that (easy, reliable)
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var fields []FieldDef
		if err := json.Unmarshal(b, &fields); err != nil {
			return nil, err
		}
		// normalize types and labels
		for i := range fields {
			if fields[i].Label == "" {
				fields[i].Label = fields[i].Name
			}
			switch fields[i].Type {
			case "int", "string", "[]string", "link":
				// ok - accept link explicitly
			default:
				fields[i].Type = "string"
			}
		}
		return fields, nil
	}

	// Fallback: parse Go-like struct text (existing behavior, but more tolerant)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := string(b)
	lines := strings.Split(s, "\n")
	inside := false
	var fields []FieldDef

	fieldRe := regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s+((?:\[\])?[A-Za-z_][A-Za-z0-9_]*)`)

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if !inside {
			if strings.HasPrefix(line, "type") && strings.Contains(line, "DataEntry") && strings.Contains(line, "struct") {
				inside = true
			}
			continue
		}
		if strings.HasPrefix(line, "}") {
			break
		}
		// strip tags and inline comments
		if idx := strings.Index(line, "`"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		m := fieldRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		typ := m[2]
		switch typ {
		case "int", "string", "[]string":
		default:
			if strings.HasPrefix(typ, "[]") {
				// keep slice type as-is
			} else {
				typ = "string"
			}
		}
		fields = append(fields, FieldDef{Name: name, Type: typ, Label: name})
	}
	return fields, nil
}
