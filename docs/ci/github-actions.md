# CI/CD Integration

Marshal can be integrated into CI/CD pipelines for automated code improvements, refactoring, and testing.

## GitHub Actions

See [marshal.yml](marshal.yml) for a complete example workflow that:

- Triggers on PR comments containing `/marshal`
- Runs Marshal in headless mode with NDJSON output
- Posts results back to the PR
- Uploads artifacts for debugging

### Quick Setup

1. Copy `marshal.yml` to `.github/workflows/marshal.yml` in your repository
2. Add your API key as a repository secret (e.g., `ANTHROPIC_API_KEY`)
3. Comment `/marshal fix the linting errors` on any PR

### Exit Codes

Marshal uses structured exit codes for reliable automation:

| Code | Meaning |
|------|---------|
| 0 | Task passed and merged |
| 1 | Task failed after all retry rounds |
| 2 | Configuration error |
| 3 | Git error |
| 4 | Pipeline integration failure |

### NDJSON Output

In CI mode, Marshal emits NDJSON events:

```json
{"event":"session_start","timestamp":"2026-04-14T10:00:00Z"}
{"event":"round_start","round":1,"timestamp":"2026-04-14T10:00:01Z"}
{"event":"round_end","round":1,"verdict":"PASS","timestamp":"2026-04-14T10:00:30Z"}
{"event":"session_end","verdict":"passed","total_tokens":5000,"duration_ms":30000}
```

Parse with `jq`:

```bash
marshal run --json "fix the bug" | jq -r 'select(.event == "session_end") | .verdict'
```

## Other CI Systems

### GitLab CI

```yaml
marshal-job:
  image: golang:1.23
  script:
    - go install github.com/alec/marshal/cmd/marshal@latest
    - marshal run --json "$CI_COMMIT_MESSAGE" | tee output.ndjson
  artifacts:
    paths:
      - output.ndjson
```

### CircleCI

```yaml
version: 2.1
jobs:
  marshal:
    docker:
      - image: cimg/go:1.23
    steps:
      - checkout
      - run:
          name: Run Marshal
          command: |
            go install github.com/alec/marshal/cmd/marshal@latest
            marshal run --json "refactor this code"
```

### Jenkins

```groovy
pipeline {
    agent { docker { image 'golang:1.23' } }
    stages {
        stage('Marshal') {
            steps {
                sh 'go install github.com/alec/marshal/cmd/marshal@latest'
                sh 'marshal run --json "update dependencies"'
            }
        }
    }
}
```

## Best Practices

1. **Use dedicated API keys**: Create separate API keys for CI usage
2. **Set appropriate timeouts**: CI tasks may take 5-10 minutes
3. **Cache results**: Upload NDJSON output as artifacts for debugging
4. **Start with `--exit`**: For simpler CI integration, use `--exit` flag
5. **Test locally first**: Run `marshal run "task"` locally before automating

## Pipeline Mode

For complex multi-file changes, use pipeline mode:

```bash
marshal pipeline --no-tui "add comprehensive error handling"
```

Pipeline mode:
1. Decomposes the task into a DAG
2. Executes tasks in parallel where safe
3. Runs integration critic before merging
4. Reports combined results

See the example workflow in [marshal.yml](marshal.yml) for the `marshal-pipeline` job.
