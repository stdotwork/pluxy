# Archive

Extensions and test cases that pluxy no longer ships, kept as separate Go modules so `go build ./...`, `go vet ./...` and `go test ./...` in `next/` skip them. An extension here can still be built by hand, and packed for install with `go run ./cmd/pluxy-pack -archived` (see each folder's README).
