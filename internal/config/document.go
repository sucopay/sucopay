package config

import "fmt"

// Document returns a configuration document holding the values suco Pay would
// otherwise choose on its own.
//
// Every value is written out, including the ones that match a built-in
// default, so that whoever runs the instance can read what it decided instead
// of inferring it. Keys for a subsystem arrive with that subsystem: a document
// naming one that nothing acts on would describe a server that ignores it.
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
`, DefaultHost, DefaultPort, defaultBaseURL(DefaultPort))
}
