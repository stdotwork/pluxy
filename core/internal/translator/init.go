package translator

import (
	_ "github.com/luispater/CLIProxyAPI/internal/translator/claude/gemini"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/claude/gemini-cli"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/claude/openai/chat-completions"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/claude/openai/responses"

	_ "github.com/luispater/CLIProxyAPI/internal/translator/codex/claude"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/codex/gemini"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/codex/gemini-cli"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/codex/openai/chat-completions"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/codex/openai/responses"

	_ "github.com/luispater/CLIProxyAPI/internal/translator/gemini-cli/claude"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/gemini-cli/gemini"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/gemini-cli/openai/chat-completions"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/gemini-cli/openai/responses"

	_ "github.com/luispater/CLIProxyAPI/internal/translator/gemini/claude"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/gemini/gemini"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/gemini/gemini-cli"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/gemini/openai/chat-completions"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/gemini/openai/responses"

	_ "github.com/luispater/CLIProxyAPI/internal/translator/openai/claude"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/openai/gemini"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/openai/gemini-cli"
	_ "github.com/luispater/CLIProxyAPI/internal/translator/openai/openai/responses"
)
