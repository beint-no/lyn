# lyn

Opinionated static site generator replacing Hugo for the gtm sites. README.md is
the user reference; keep it in sync with behaviour.

* Go, latest stable patch release only: when Go ships a release, set the `go`
  directive in go.mod to it, run `go get -u ./...` and `go mod tidy`, and
  release lyn. No compatibility code for older Go. Dependencies: goldmark,
  tdewolff minify/parse, xxh3, fsnotify. Add others only with a measured reason.
* One way to do each thing. Do not add options, flags, config keys, alternative
  formats or fallbacks. A missing capability is added as the single obvious
  mechanism and documented in README.md.
* Anything broken or unused fails the build. New features come with the checks
  that keep them from rotting, plus a case in `build/build_test.go` TestFailures.
* Incremental correctness: any new input a template can read must be recorded
  by the recorder (`build/render.go`) and invalidated in `Builder.dirty`. Add a
  mutation to the randomized equivalence tests for it.
* Performance: measure with `go run ./bench/compare` before and after changes
  to the build pipeline, and keep cold builds at least 2x faster than Hugo.
* No comments in code. `gofmt`, `go vet ./...` and `go test ./...` must pass
  before committing.
