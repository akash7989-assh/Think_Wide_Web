# Campaign Events Backend Service

This is the HTTP service for ingesting webhook campaign events and serving aggregated campaign metrics.

## Running the Service

```bash
go run .
```

The service will start on port `8080` (or the port defined in `$env:PORT`).

## Running Tests

```bash
go test -v ./...
```

For complete documentation, see the root [README.md](../README.md) and [NOTES.md](../NOTES.md).

