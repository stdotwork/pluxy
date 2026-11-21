package main

import (
	"context"
	"errors"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

// An account can point at Google's own host, and Google's errors name the Google project or
// account of the owner's upstream account, which a client holding a pluxy key must not learn.
// maskGoogle wraps the handlers whose errors reach clients (Generate, CountTokens and Endpoint)
// and masks a returned *llm.Error whose raw body has Google's error shape
// (geminigenerate.MaskProject), after the handler classified it. This covers an error status, an
// error inside a stream, and every other way the handler returns one. Login, Check, Quota and
// Models are the owner's, who reads them to find which project needs an API enabled, and are not
// wrapped. An error of another vendor is left as it is.
func maskGoogle(h sdk.Handler) sdk.Handler {
	return func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
		err := h(ctx, call, out)
		var e *llm.Error
		if err != nil && errors.As(err, &e) && geminigenerate.IsGoogleErrorShape(e.Raw) {
			return geminigenerate.MaskProject(e)
		}
		return err
	}
}
