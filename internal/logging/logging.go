// Package logging builds a slog logger for one package, with the attributes
// that a context carries.
//
//	var log = logging.New("proxy")
//
//	func serve(ctx context.Context) {
//		log.Ctx(ctx).Info("listening")
//	}
package logging

import (
	"context"
	"log/slog"
	"sync"
)

type attrsKey struct{}

// WithAttrs returns a context that carries attrs. An attr replaces an earlier
// attr that has the same key.
func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	if len(attrs) == 0 {
		return ctx
	}
	existing := Attrs(ctx)
	merged := make([]slog.Attr, 0, len(existing)+len(attrs))
	for _, a := range existing {
		if !hasKey(attrs, a.Key) {
			merged = append(merged, a)
		}
	}
	merged = append(merged, attrs...)
	return context.WithValue(ctx, attrsKey{}, merged)
}

// Attrs returns the attributes that ctx carries.
func Attrs(ctx context.Context) []slog.Attr {
	attrs, _ := ctx.Value(attrsKey{}).([]slog.Attr)
	return attrs
}

func hasKey(attrs []slog.Attr, key string) bool {
	for _, a := range attrs {
		if a.Key == key {
			return true
		}
	}
	return false
}

// Package builds a logger tagged with a package name. The zero value is not
// usable. Call [New].
type Package struct {
	name string
	once sync.Once
	log  *slog.Logger
}

// New returns a logger for the named package. Assign it to an unexported
// package-level variable.
func New(name string) *Package {
	return &Package{name: name}
}

// Ctx returns a logger that carries the package name and the attributes in
// ctx.
func (p *Package) Ctx(ctx context.Context) *slog.Logger {
	p.once.Do(func() {
		p.log = slog.Default().With(slog.String("package", p.name))
	})
	attrs := Attrs(ctx)
	if len(attrs) == 0 {
		return p.log
	}
	args := make([]any, 0, len(attrs))
	for _, a := range attrs {
		args = append(args, a)
	}
	return p.log.With(args...)
}
