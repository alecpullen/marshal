# TUI Keybindings

Marshal's interactive TUI uses standard terminal navigation patterns with Emacs-style keybindings.

## Global Keys

| Key | Action |
|-----|--------|
| `Ctrl+C` | Quit Marshal |
| `Ctrl+L` | Clear screen (refresh) |
| `Tab` | Focus next element |
| `Shift+Tab` | Focus previous element |

## Input/Prompt Keys

| Key | Action |
|-----|--------|
| `Enter` | Submit prompt (single-line mode) |
| `Ctrl+Enter` | Submit prompt (multiline mode) |
| `Ctrl+S` | Toggle multiline mode |
| `Ctrl+E` | Open external editor |
| `Up/Down` | Navigate command history |
| `Ctrl+A` | Beginning of line |
| `Ctrl+E` | End of line |
| `Ctrl+K` | Kill to end of line |
| `Ctrl+U` | Kill entire line |
| `Ctrl+W` | Kill previous word |
| `Ctrl+Y` | Yank (paste) |
| `Ctrl+Space` | Set mark (start selection) |
| `Ctrl+X` | Cut selection |
| `Ctrl+V` or `Cmd+V` | Paste from clipboard |

## Slash Commands

Type `/` at the prompt to see available commands. Common commands:

| Command | Description |
|---------|-------------|
| `/help` | Show all commands |
| `/add <file>` | Add file to context |
| `/drop <file>` | Remove file from context |
| `/ls` | List files in context |
| `/diff` | Show current diff |
| `/commit` | Commit changes |
| `/ship` | Merge staging to target |
| `/undo` | Undo last task |
| `/history` | Show task history |
| `/tokens` | Show token usage |
| `/map` | Show repo map |
| `/settings` | Show current settings |
| `/model` | Show active models |
| `/skills` | List available skills |
| `/reset` | Reset session |
| `/web <url>` | Fetch web content |
| `/paste` | Paste from clipboard |
| `/copy` | Copy last response |
| `/run <cmd>` | Run shell command |
| `/test [args]` | Run tests |
| `/git <cmd>` | Run git command |
| `/lint` | Run linter |
| `/editor` | Open external editor |
| `/multiline-mode` | Toggle Enter behavior |
| `/voice` | Start voice input (if enabled) |

## Multiline Mode

In multiline mode:
- `Enter` inserts a newline
- `Ctrl+Enter` submits the prompt
- `Ctrl+S` toggles back to single-line mode

## Scrolling

| Key | Action |
|-----|--------|
| `Page Up/Page Down` | Scroll chat history |
| `Ctrl+Home` | Jump to beginning |
| `Ctrl+End` | Jump to end |
| `Mouse wheel` | Scroll |

## Mouse Support

- **Scroll**: Scroll through chat history
- **Click**: Focus input or buttons
- **Drag**: Select text

## External Editor

When using `/editor` or `Ctrl+E`:
1. Opens `$VISUAL` or `$EDITOR` (defaults to `vim`)
2. Edit your prompt in the external editor
3. Save and exit to submit
4. Marshal resumes with your edited text

## Tips

- Use `Ctrl+S` for long prompts that need newlines
- Command history persists across prompts (use Up/Down)
- Tab completion available for file paths with `/add`
- Copy/paste works with system clipboard
