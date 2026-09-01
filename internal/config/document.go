package config

import "fmt"

// Document returns a configuration document holding the values suco Pay would
// otherwise choose on its own, including the ones equal to a built-in default.
//
// Keys for a subsystem arrive with that subsystem.
func Document() []byte {
	return fmt.Appendf(nil, `# Written by suco init.
# Commit this file. Keep secrets in the environment, not here.

listen:
  # The interface to bind. Reaching this instance from another machine is a
  # deployment decision, so it is made here rather than assumed.
  host: %s
  port: %d
  # How others reach this instance, which differs from host behind a proxy.
  base_url: %s

log:
  # debug, info, warn or error.
  level: %s
  # text to read in a terminal, json for whatever collects it.
  format: %s
`, DefaultHost, DefaultPort, defaultBaseURL(DefaultPort), DefaultLogLevel, DefaultLogFormat)
}
