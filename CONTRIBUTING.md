# Contributing to Marshal

Thank you for your interest in contributing to Marshal!

## Development Setup

```bash
git clone https://github.com/alec/marshal.git
cd marshal
go mod tidy
go build -o bin/marshal ./cmd/marshal
```

## Running Tests

```bash
go test ./...
```

## Linting

```bash
golangci-lint run
```

## Pull Request Process

1. Fork the repository and create your branch from `main`
2. Run tests and ensure they pass
3. Update documentation if needed
4. Ensure your code follows the existing patterns
5. Submit your pull request

## Code of Conduct

Be respectful and constructive in all interactions.
