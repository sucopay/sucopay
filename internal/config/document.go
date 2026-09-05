package config

import "fmt"

// KeyVar is the environment variable the document init writes names for
// credentials.key. init cannot set it: a process sets the environment of
// what it starts, and init is not what starts serve. So init writes the key
// to a file and prints the line that sets the variable from it.
const KeyVar = "SUCO_CREDENTIALS_KEY"

// Document returns a configuration document holding the values suco Pay would
// otherwise choose on its own, including the ones equal to a built-in default.
// keyID is the identifier of the key the document is written for; the key
// itself is the environment's, and the document names [KeyVar] for it. The
// identifier is written quoted, since sixteen hexadecimal characters are
// now and then all decimal digits, or digits around one e, and YAML reads
// either as a number.
//
// Keys for a subsystem arrive with that subsystem.
func Document(keyID string) []byte {
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

credentials:
  # The key credentials are stored under, read from the environment. The
  # line suco init printed sets the variable from the key file.
  key: ${%s}
  # Which key the stored credentials were made under. Not a secret.
  key_id: %q
`, DefaultHost, DefaultPort, defaultBaseURL(DefaultPort), DefaultLogLevel, DefaultLogFormat, KeyVar, keyID)
}
