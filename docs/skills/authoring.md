# Skill Authoring Guide

Skills extend Marshal's capabilities by adding specialized prompts for specific tasks.

## What is a Skill?

A skill is a TOML file that defines:
- A trigger command (e.g., `/test`)
- Optional system prompt additions for the executor and/or critic
- A description for the `/skills` listing

## Skill Location

Skills are loaded from:
1. Built-in skills (compiled into Marshal)
2. `~/.config/marshal/skills/*.toml` (user skills)

## Skill File Format

```toml
name = "test"
trigger = "/test"
description = "Write comprehensive tests for the current code"

[[layers]]
role = "executor"
system_prompt = """
When writing tests:
- Use table-driven tests where appropriate
- Test both happy path and error cases
- Include edge cases and boundary conditions
- Use descriptive test names
- Mock external dependencies
"""

[[layers]]
role = "critic"
system_prompt = """
When reviewing tests:
- Verify test coverage is comprehensive
- Check for meaningful assertions
- Ensure tests are deterministic
- Validate error handling is tested
"""
```

## Fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Skill identifier |
| `trigger` | Yes | Command to activate (e.g., `/test`) |
| `description` | Yes | Help text shown in `/skills` |
| `layers` | No | Array of prompt layers |

## Layer Fields

| Field | Description |
|-------|-------------|
| `role` | Which model role: `executor`, `critic`, `marshal`, `compactor` |
| `system_prompt` | Text appended to that role's system prompt |

## Example: Security Audit Skill

```toml
name = "security"
trigger = "/security"
description = "Perform security audit on the code"

[[layers]]
role = "executor"
system_prompt = """
When reviewing code for security:
- Check for SQL injection vulnerabilities
- Validate input sanitization
- Look for hardcoded secrets
- Review authentication/authorization logic
- Check for insecure dependencies
- Verify proper error handling (no information leakage)
"""

[[layers]]
role = "critic"
system_prompt = """
When reviewing security audit results:
- Verify all findings are legitimate issues
- Check for false positives
- Ensure severity is appropriate
- Validate fixes don't break functionality
"""
```

## Example: Documentation Skill

```toml
name = "docs"
trigger = "/docs"
description = "Add or improve documentation"

[[layers]]
role = "executor"
system_prompt = """
When writing documentation:
- Add docstrings to all exported functions
- Include usage examples
- Document parameters and return values
- Note any side effects or panics
- Keep documentation close to the code
"""
```

## Built-in Skills

Marshal includes several built-in skills:

- `/test` - Write comprehensive tests
- `/security` - Security audit
- `/docs` - Add documentation
- `/refactor` - Code refactoring

## Best Practices

1. **Keep prompts focused**: Each skill should have a clear, specific purpose
2. **Use critic layers**: Adding critic guidance improves review quality
3. **Test your skills**: Try them on real code before relying on them
4. **Version control**: Keep skills in git to track changes

## Debugging Skills

Use `/skills` to see loaded skills and their triggers:

```
Marshal » /skills
Available skills:
  /test      - Write comprehensive tests
  /security  - Security audit
  /docs      - Add documentation
```
