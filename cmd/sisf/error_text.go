package main

import (
	"errors"

	"github.com/bestagentkits/sisf/pkg/core"
)

// errorMessage is the message of err for a person: a typed error without
// its code, which is for programs and reads like a crash beside a flag name.
func errorMessage(err error) string {
	var typed *core.Error
	if errors.As(err, &typed) {
		return typed.Message
	}
	return err.Error()
}
