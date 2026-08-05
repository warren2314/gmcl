package migrate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var createTablePattern = regexp.MustCompile(`(?is)CREATE\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?\s+([^\s(]+)\s*\(`)

// TestMigrationCreateTablesHaveUniqueColumns is a fast fresh-schema guard.
// PostgreSQL rejects a CREATE TABLE that declares the same column twice; this
// catches that class of migration failure even when a local database is not
// available to the test runner.
func TestMigrationCreateTablesHaveUniqueColumns(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("locate migrations: files=%d err=%v", len(files), err)
	}
	for _, filename := range files {
		body, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, match := range createTablePattern.FindAllStringSubmatchIndex(text, -1) {
			table := text[match[2]:match[3]]
			open := match[1] - 1
			close := matchingSQLParen(text, open)
			if close < 0 {
				t.Fatalf("%s: CREATE TABLE %s has no closing parenthesis", filepath.Base(filename), table)
			}
			seen := map[string]bool{}
			for _, declaration := range splitSQLTopLevel(text[open+1 : close]) {
				fields := strings.Fields(strings.TrimSpace(declaration))
				if len(fields) < 2 {
					continue
				}
				name := strings.ToLower(strings.Trim(fields[0], `"`))
				switch name {
				case "primary", "unique", "check", "foreign", "constraint", "exclude":
					continue
				}
				if seen[name] {
					t.Errorf("%s: CREATE TABLE %s declares column %q more than once", filepath.Base(filename), table, name)
				}
				seen[name] = true
			}
		}
	}
}

func matchingSQLParen(text string, open int) int {
	depth := 0
	inSingle := false
	for index := open; index < len(text); index++ {
		switch text[index] {
		case '\'':
			if inSingle && index+1 < len(text) && text[index+1] == '\'' {
				index++
				continue
			}
			inSingle = !inSingle
		case '(':
			if !inSingle {
				depth++
			}
		case ')':
			if !inSingle {
				depth--
				if depth == 0 {
					return index
				}
			}
		}
	}
	return -1
}

func splitSQLTopLevel(text string) []string {
	depth := 0
	start := 0
	inSingle := false
	parts := []string{}
	for index := 0; index < len(text); index++ {
		switch text[index] {
		case '\'':
			if inSingle && index+1 < len(text) && text[index+1] == '\'' {
				index++
				continue
			}
			inSingle = !inSingle
		case '(':
			if !inSingle {
				depth++
			}
		case ')':
			if !inSingle {
				depth--
			}
		case ',':
			if !inSingle && depth == 0 {
				parts = append(parts, text[start:index])
				start = index + 1
			}
		}
	}
	return append(parts, text[start:])
}
