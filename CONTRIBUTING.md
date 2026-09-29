# Contributing to Gamarr

Thanks for your interest! Check the [issues](https://github.com/JeremiahM37/gamarr/issues) for things to work on.

## Quick Start

```bash
git clone https://github.com/JeremiahM37/gamarr.git
cd gamarr
go build -o gamarr ./cmd/gamarr/
go test ./...
```

## Adding a Platform

1. Add an entry to `internal/platform/registry.go` with its slug, categories, formats and metadata hints.
2. Keep automatic-detection extensions unique; put shared or ambiguous formats in `Formats`.
3. Add source registry paths if applicable.
4. Write detection, search and import tests.

## License

MIT — see [LICENSE](LICENSE). By contributing, you agree that your contributions will be licensed under the same terms.
