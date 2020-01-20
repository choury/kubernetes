package qcloud

type QcloudError interface {
	// Satisfy the generic error interface.
	error

	// Returns the short phrase depicting the classification of the error.
	Code() string

	// Returns the original error if one was set.  Nil is returned if not set.
	OrignalErr() error
}