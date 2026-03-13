package arcana

import "errors"

var (
	// ErrForbidden is returned when a user lacks permission to access a graph.
	ErrForbidden = errors.New("arcana: forbidden")

	// ErrNotFound is returned when a requested graph or subscription does not exist.
	ErrNotFound = errors.New("arcana: not found")

	// ErrInvalidParams is returned when subscription parameters fail validation.
	ErrInvalidParams = errors.New("arcana: invalid params")

	// ErrTooManySubscriptions is returned when a seance exceeds its subscription limit.
	ErrTooManySubscriptions = errors.New("arcana: too many subscriptions")

	// ErrAlreadyStarted is returned when Start is called on a running engine.
	ErrAlreadyStarted = errors.New("arcana: already started")

	// ErrNotStarted is returned when operations are attempted before Start.
	ErrNotStarted = errors.New("arcana: not started")

	// ErrTransportNotReady is returned when WSTransport is used before Engine.Start.
	ErrTransportNotReady = errors.New("arcana: transport not ready")
)
