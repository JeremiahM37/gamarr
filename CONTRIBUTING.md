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

1. Add the platform to `internal/platform/platform.go`
2. Map Prowlarr categories in the platform map
3. Add Myrient path if applicable
4. Write tests

## License

GPL-3.0
