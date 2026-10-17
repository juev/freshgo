package config

import (
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"

	"go.yaml.in/yaml/v3"
)

// Settings are the values of a configuration file by the names of the
// flags they stand for.
type Settings map[string]string

// ReadFile reads a configuration file: a YAML mapping of the names of the
// flags of Bind, without the dash, to their values. A name there is no flag
// for, or a value the flag does not take, is refused here, before a command
// starts.
func ReadFile(path string) (Settings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("configuration file: %w", err)
	}
	var s Settings
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("configuration file %s: %w", path, err)
	}
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	Bind(fs, func(string) string { return "" })
	for _, name := range slices.Sorted(maps.Keys(s)) {
		if fs.Lookup(name) == nil {
			return nil, fmt.Errorf("configuration file %s: unknown setting %q", path, name)
		}
		if err := fs.Set(name, s[name]); err != nil {
			return nil, fmt.Errorf("configuration file %s: %s: %w", path, name, err)
		}
	}
	return s, nil
}

// Apply sets the flags of fs to the values of the file. It is called before
// the command line is parsed, so that a flag given there wins over the file,
// and the file over the environment, whose values are the defaults of the
// flags.
func (s Settings) Apply(fs *flag.FlagSet) {
	for name, value := range s {
		// ReadFile has tried every value on the same flags.
		_ = fs.Set(name, value)
	}
}
