package contkit

import "testing"

var _ Logger = (*logger)(nil)

type logger struct {
	print  func(...any)
	printf func(string, ...any)
}

func (l *logger) Print(v ...any) {
	l.print(v...)
}

func (l *logger) Printf(format string, v ...any) {
	l.printf(format, v...)
}

// WithTestingLogger adapts testing.T as a logger for
// Logger interface.
func WithTestingLogger(t *testing.T) Logger {
	return &logger{
		print:  t.Log,
		printf: t.Logf,
	}
}
