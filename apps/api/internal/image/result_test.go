package image

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestResultValidation(t *testing.T) {
	b, i := uuid.New(), uuid.New()
	valid := Result{Version: 1, JobType: "composite", BatchID: b, ImageID: i,
		Status: "done", OutputKey: "processed/" + b.String() + "/" + i.String() + ".png"}
	require.NoError(t, valid.Validate())
	for _, mutate := range []func(*Result){
		func(r *Result) { r.Version = 2 },
		func(r *Result) { r.JobType = "extract" },
		func(r *Result) { r.ImageID = uuid.Nil },
		func(r *Result) { r.BatchID = uuid.Nil },
		func(r *Result) { r.Status = "pending" },
		func(r *Result) { r.OutputKey = ".png" },
		func(r *Result) { r.OutputKey += "/../other.png" },
		func(r *Result) { r.ImageID = uuid.New() },
		func(r *Result) { r.Error = "cannot be both done and failed" },
		func(r *Result) { r.Status = "failed"; r.OutputKey = "" },
		func(r *Result) { r.Status = "failed"; r.OutputKey = ""; r.Error = strings.Repeat("x", 4097) },
	} {
		r := valid
		mutate(&r)
		require.Error(t, r.Validate(), "%+v", r)
	}
	valid.Status, valid.OutputKey, valid.Error = "failed", "", "invalid image bytes"
	require.NoError(t, valid.Validate())
	valid.OutputKey = "unexpected"
	require.Error(t, valid.Validate())
}
