package importer

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestParsePHPConfig(t *testing.T) {
	src := `<?php
return array (
  'salt' => 'abc',
  'quote' => 'it\'s a \\ backslash and a \n that stays',
  'enabled' => true,
  'off' => FALSE,
  'nothing' => NULL,
  'count' => 200,
  'negative' => -5,
  'ratio' => 1.5,
  'db' =>
  array (
    'type' => 'sqlite',
    'pdo_options' =>
    array (
    ),
  ),
  'list' =>
  array (
    0 => 'a',
    1 => 'b',
  ),
  'sparse' =>
  array (
    10004 => 'proxy',
    2 => 'x',
  ),
);`
	got, err := parsePHPConfig(src)
	if err != nil {
		t.Fatalf("parsePHPConfig: %v", err)
	}
	want := map[string]any{
		"salt":     "abc",
		"quote":    `it's a \ backslash and a \n that stays`,
		"enabled":  true,
		"off":      false,
		"nothing":  nil,
		"count":    int64(200),
		"negative": int64(-5),
		"ratio":    1.5,
		"db":       map[string]any{"type": "sqlite", "pdo_options": []any{}},
		"list":     []any{"a", "b"},
		"sparse":   map[string]any{"10004": "proxy", "2": "x"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePHPConfig:\n got %#v\nwant %#v", got, want)
	}
}

func TestParsePHPConfigHandEdited(t *testing.T) {
	src := `<?php
# comment
// another
return [
	/* block */ 'a' => "tab\there \"quoted\" \$5", 'b', 'c',
	7 => 'seven', 'eight',
]; ?>
`
	got, err := parsePHPConfig(src)
	if err != nil {
		t.Fatalf("parsePHPConfig: %v", err)
	}
	want := map[string]any{"a": "tab\there \"quoted\" $5", "0": "b", "1": "c", "7": "seven", "8": "eight"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePHPConfig:\n got %#v\nwant %#v", got, want)
	}
}

func TestParsePHPConfigRefusesCode(t *testing.T) {
	tests := map[string]string{
		"function call":   `<?php return array('a' => getenv('X'));`,
		"constant":        `<?php return array('a' => PHP_VERSION);`,
		"interpolation":   `<?php return array('a' => "$x");`,
		"concatenation":   `<?php return array('a' => 'x' . 'y');`,
		"statement after": `<?php return array(); echo 1;`,
		"no return":       `<?php array();`,
		"unterminated":    `<?php return array('a' => 'x`,
		"missing comma":   `<?php return array('a' 'b');`,
	}
	for name, src := range tests {
		if v, err := parsePHPConfig(src); err == nil {
			t.Errorf("%s: no error, got %#v", name, v)
		}
	}
	_, err := parsePHPConfig("<?php\nreturn array(\n'a' => foo(),\n);")
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Errorf("error = %v, want it to name line 3", err)
	}
}

// The files written by a real FreshRSS must parse.
func TestParsePHPConfigReference(t *testing.T) {
	for _, path := range []string{
		"../../testdata/reference/sqlite/data/config.php",
		"../../testdata/reference/sqlite/data/users/alice/config.php",
		"../../testdata/reference/pgsql/data/config.php",
	} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		v, err := parsePHPConfig(string(src))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			t.Errorf("%s: got %T, want a map", path, v)
			continue
		}
		if strings.HasSuffix(path, "alice/config.php") {
			archiving, _ := m["archiving"].(map[string]any)
			if archiving["keep_max"] != int64(200) || m["apiPasswordHash"] == "" {
				t.Errorf("%s: archiving = %#v, apiPasswordHash = %#v", path, m["archiving"], m["apiPasswordHash"])
			}
		} else if salt, _ := m["salt"].(string); len(salt) != 64 {
			t.Errorf("%s: salt = %#v, want 64 characters", path, m["salt"])
		}
	}
}
