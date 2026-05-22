package selflog

import "github.com/jasondillingham/leonard/internal/adapters"

// init registers the "self-logging" adapter. No implicit-enable
// rule: operators opt in via [[adapters]] in .leonard/config.toml.
// Self-logging is observable (it writes a log file alongside
// .leonard/leonard.db) so silently turning it on without the
// operator's awareness would be surprising.
func init() {
	adapters.Register(Name, New)
}
