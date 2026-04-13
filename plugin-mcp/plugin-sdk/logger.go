package plugin

// Logger provides structured logging capabilities for plugins. Hosts supply
// an implementation via Host.Logger(); plugins running as subprocesses use
// the stderr JSON-lines logger from github.com/hollis-labs/plugin-sdk/subprocess.
type Logger interface {
	Debug(msg string, keysAndValues ...interface{})
	Info(msg string, keysAndValues ...interface{})
	Warn(msg string, keysAndValues ...interface{})
	Error(msg string, keysAndValues ...interface{})
	With(keysAndValues ...interface{}) Logger
}
