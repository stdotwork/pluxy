package translator

import "context"

// RequestTransform converts a request payload from one schema to another.
type RequestTransform func(model string, rawJSON []byte, stream bool) []byte

// ResponseStreamTransform converts streaming responses between schemas.
type ResponseStreamTransform func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string

// ResponseNonStreamTransform converts non-stream responses between schemas.
type ResponseNonStreamTransform func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) string

type ResponseTokenCountTransform func(ctx context.Context, count int64) string

// ResponseTransform groups streaming and non-streaming transforms.
type ResponseTransform struct {
	Stream     ResponseStreamTransform
	NonStream  ResponseNonStreamTransform
	TokenCount ResponseTokenCountTransform
}
