package logger

import "go.uber.org/zap"

// zapFieldAdapter forwards leveled log calls to a zap.Logger, converting each
// structured field of type F with toField. The exported per-seam adapters embed
// it so every logging interface shares one implementation.
type zapFieldAdapter[F any] struct {
	z       *zap.Logger
	toField func(F) zap.Field
}

func newZapFieldAdapter[F any](z *zap.Logger, toField func(F) zap.Field) zapFieldAdapter[F] {
	if z == nil {
		z = zap.NewNop()
	}
	return zapFieldAdapter[F]{z: z, toField: toField}
}

func (a zapFieldAdapter[F]) zapFields(fields []F) []zap.Field {
	zfs := make([]zap.Field, 0, len(fields))
	for _, f := range fields {
		zfs = append(zfs, a.toField(f))
	}
	return zfs
}

func (a zapFieldAdapter[F]) Debug(msg string, fields ...F) { a.z.Debug(msg, a.zapFields(fields)...) }

func (a zapFieldAdapter[F]) Info(msg string, fields ...F) { a.z.Info(msg, a.zapFields(fields)...) }

func (a zapFieldAdapter[F]) Warn(msg string, fields ...F) { a.z.Warn(msg, a.zapFields(fields)...) }

func (a zapFieldAdapter[F]) Error(msg string, fields ...F) { a.z.Error(msg, a.zapFields(fields)...) }

func (a zapFieldAdapter[F]) Fatal(msg string, fields ...F) { a.z.Fatal(msg, a.zapFields(fields)...) }

func (a zapFieldAdapter[F]) Panic(msg string, fields ...F) { a.z.Panic(msg, a.zapFields(fields)...) }
