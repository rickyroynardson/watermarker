package utils

import (
	"errors"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

func NewValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name, _, _ := strings.Cut(fld.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			return fld.Name
		}
		return name
	})
	return v
}

func ValidationMessage(err error) string {
	var errs validator.ValidationErrors
	if !errors.As(err, &errs) || len(errs) == 0 {
		return "invalid request"
	}
	e := errs[0]
	switch e.Tag() {
	case "required":
		return e.Field() + " is required"
	case "min":
		return e.Field() + " must have at least " + e.Param() + " items"
	case "unique":
		return e.Field() + " must not contain duplicates"
	case "max":
		return e.Field() + " is too long"
	default:
		return "invalid request"
	}
}
