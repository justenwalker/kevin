package plugin

// Value is one output value a step publishes or receives from a dependency.
type Value interface {
	// Reveal returns the value's actual content.
	Reveal() string
	// IsSensitive reports whether this value must never be logged or
	// displayed in full.
	IsSensitive() bool
	// String implements fmt.Stringer. A Sensitive-wrapped value redacts
	// here, so an accidental %v/%s/Println never prints one - call Reveal
	// for that.
	String() string
}

// String is a plain string value with nothing to hide.
type String string

// Reveal returns s's actual content.
func (s String) Reveal() string { return string(s) }

// IsSensitive always reports false: a String has nothing to hide.
func (s String) IsSensitive() bool { return false }

func (s String) String() string { return string(s) }

// Sensitive wraps any Value to mark it secret - a generated password or
// token, for example. It works on String today and on any future Value
// implementation without a parallel "Sensitive<Type>" type.
type Sensitive struct {
	Value
}

// IsSensitive always reports true, regardless of the wrapped Value.
func (Sensitive) IsSensitive() bool { return true }

func (Sensitive) String() string { return "[REDACTED]" }

// GoString implements fmt.GoStringer, so %#v also redacts - String needs no
// override, its default %#v (`plugin.String("x")`) already isn't secret.
func (Sensitive) GoString() string { return `plugin.Sensitive{"[REDACTED]"}` }

// StringMap wraps every value of m as a plain String, for a step whose
// outputs have nothing to hide.
func StringMap(m map[string]string) map[string]Value {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]Value, len(m))
	for k, v := range m {
		out[k] = String(v)
	}
	return out
}
