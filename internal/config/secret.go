package config

import "log/slog"

const redacted = "[REDACTED]"

// Secret holds a sensitive string (API key, database URL with password).
//
// Every way of printing a Secret (fmt verbs, slog, JSON/text marshaling)
// yields "[REDACTED]". The raw value is only available through Reveal, so a
// leak requires an explicit, greppable call.
type Secret string

// Reveal returns the underlying sensitive value.
func (s Secret) Reveal() string { return string(s) }

// String implements fmt.Stringer.
func (Secret) String() string { return redacted }

// GoString implements fmt.GoStringer (used by the %#v verb).
func (Secret) GoString() string { return redacted }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText implements encoding.TextMarshaler (covers JSON and other encoders).
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
