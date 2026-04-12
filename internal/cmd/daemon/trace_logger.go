package daemoncmd

type traceFunc func(string, ...any)

func tracef(trace traceFunc, format string, args ...any) {
	if trace == nil {
		return
	}
	trace(format, args...)
}
