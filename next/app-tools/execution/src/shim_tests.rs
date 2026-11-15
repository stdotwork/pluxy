use super::*;

// Shim texts as each tool writes them: npm's from cmd-shim's own test snapshots
// (tap-snapshots/test/basic.js.test.cjs at v9.0.2 and v6.0.2), pnpm's and Yarn's rendered by
// their shim code (@zkochan/cmd-shim 9.0.8, yarn 1.22.22).

const NPM9_NODE: &str = "@ECHO off\r\nGOTO start\r\n:find_dp0\r\nSET dp0=%~dp0\r\nEXIT /b\r\n:start\r\nSETLOCAL\r\nCALL :find_dp0\r\n\r\nIF EXIST \"%dp0%\\node.exe\" (\r\n  SET \"_prog=%dp0%\\node.exe\"\r\n) ELSE (\r\n  SET \"_prog=node\"\r\n)\r\n\r\nendLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & set PATHEXT=%PATHEXT:;.JS;=;% & \"%_prog%\"  \"%dp0%\\node_modules\\@openai\\codex\\bin\\codex.js\" %*\r\n";

const NPM9_ARGS: &str = "@ECHO off\r\nGOTO start\r\n:find_dp0\r\nSET dp0=%~dp0\r\nEXIT /b\r\n:start\r\nSETLOCAL\r\nCALL :find_dp0\r\n\r\nIF EXIST \"%dp0%\\node.exe\" (\r\n  SET \"_prog=%dp0%\\node.exe\"\r\n) ELSE (\r\n  SET \"_prog=node\"\r\n)\r\n\r\nendLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & set PATHEXT=%PATHEXT:;.JS;=;% & \"%_prog%\" --expose_gc \"%dp0%\\from.env.args\" %*\r\n";

const NPM9_NATIVE: &str = "@ECHO off\r\nGOTO start\r\n:find_dp0\r\nSET dp0=%~dp0\r\nEXIT /b\r\n:start\r\nSETLOCAL\r\nCALL :find_dp0\r\n\"%dp0%\\node_modules\\@anthropic-ai\\claude-code\\bin\\claude.exe\"   %*\r\n";

const NPM6_NODE: &str = "@ECHO off\r\nGOTO start\r\n:find_dp0\r\nSET dp0=%~dp0\r\nEXIT /b\r\n:start\r\nSETLOCAL\r\nCALL :find_dp0\r\n\r\nIF EXIST \"%dp0%\\node.exe\" (\r\n  SET \"_prog=%dp0%\\node.exe\"\r\n) ELSE (\r\n  SET \"_prog=node\"\r\n  SET PATHEXT=%PATHEXT:;.JS;=;%\r\n)\r\n\r\nendLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & \"%_prog%\"  \"%dp0%\\from.env\" %*\r\n";

const NPM9_SH: &str = "@ECHO off\r\nGOTO start\r\n:find_dp0\r\nSET dp0=%~dp0\r\nEXIT /b\r\n:start\r\nSETLOCAL\r\nCALL :find_dp0\r\n\r\nIF EXIST \"%dp0%\\/usr/bin/sh.exe\" (\r\n  SET \"_prog=%dp0%\\/usr/bin/sh.exe\"\r\n) ELSE (\r\n  SET \"_prog=/usr/bin/sh\"\r\n)\r\n\r\nendLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & set PATHEXT=%PATHEXT:;.JS;=;% & \"%_prog%\"  \"%dp0%\\from.sh\" %*\r\n";

const PNPM_NODE: &str = "@SETLOCAL\r\n@IF EXIST \"%~dp0\\node.exe\" (\r\n  \"%~dp0\\node.exe\"  \"%~dp0\\node_modules\\pkg\\cli.js\" %*\r\n) ELSE (\r\n  @SET PATHEXT=%PATHEXT:;.JS;=;%\r\n  node  \"%~dp0\\node_modules\\pkg\\cli.js\" %*\r\n)\r\n";

const YARN1_NODE: &str = "@IF EXIST \"%~dp0\\node.exe\" (\r\n  \"%~dp0\\node.exe\"  \"%~dp0\\node_modules\\pkg\\cli.js\" %*\r\n) ELSE (\r\n  @SETLOCAL\r\n  @SET PATHEXT=%PATHEXT:;.JS;=;%\r\n  node  \"%~dp0\\node_modules\\pkg\\cli.js\" %*\r\n)";

const YARN_NATIVE: &str = "@\"%~dp0\\node_modules\\pkg\\bin\\tool.exe\"   %*\r\n";

fn node(args: &[&str], target: &str) -> Option<Shim> {
    Some(Shim::Node {
        args: args.iter().map(|a| a.to_string()).collect(),
        target: target.into(),
    })
}

fn native(target: &str) -> Option<Shim> {
    Some(Shim::Native {
        target: target.into(),
    })
}

#[test]
fn the_known_shim_layouts_are_read_as_what_they_run() {
    assert_eq!(
        parse(NPM9_NODE),
        node(&[], r"node_modules\@openai\codex\bin\codex.js")
    );
    assert_eq!(parse(NPM9_ARGS), node(&["--expose_gc"], "from.env.args"));
    assert_eq!(
        parse(NPM9_NATIVE),
        native(r"node_modules\@anthropic-ai\claude-code\bin\claude.exe")
    );
    assert_eq!(parse(NPM6_NODE), node(&[], "from.env"));
    assert_eq!(parse(PNPM_NODE), node(&[], r"node_modules\pkg\cli.js"));
    assert_eq!(parse(YARN1_NODE), node(&[], r"node_modules\pkg\cli.js"));
    assert_eq!(parse(YARN_NATIVE), native(r"node_modules\pkg\bin\tool.exe"));
    // Line breaks without carriage returns read the same.
    assert_eq!(
        parse(&NPM9_NODE.replace("\r\n", "\n")),
        node(&[], r"node_modules\@openai\codex\bin\codex.js")
    );
}

#[test]
fn any_other_batch_file_is_not_a_shim() {
    let cases = [
        // Another interpreter than Node.
        NPM9_SH.to_string(),
        // An extra command anywhere.
        NPM9_NATIVE.replace("CALL :find_dp0", "CALL :find_dp0\r\ndel /q %TEMP%\\x"),
        format!("{PNPM_NODE}\r\necho done"),
        // pnpm's program arguments after the target.
        PNPM_NODE.replace("cli.js\" %*", "cli.js\" --inspect %*"),
        // The two Node lines disagree.
        PNPM_NODE.replacen("cli.js", "other.js", 1),
        // Targets outside the shim's folder, absolute, or with cmd syntax in them.
        NPM9_NATIVE.replace(r"node_modules\@anthropic-ai", r"..\..\x"),
        NPM9_NATIVE.replace(r"%dp0%\node_modules", r"C:\x"),
        NPM9_NATIVE.replace("claude.exe", "claude%PATH%.exe"),
        NPM9_ARGS.replace("--expose_gc", "--a&calc"),
        // An argument glued to the script: cmd.exe would pass them to Node as one.
        NPM9_ARGS.replace("--expose_gc \"", "--expose_gc\""),
        PNPM_NODE.replace("node.exe\"  \"", "node.exe\"--x\""),
        // Arguments after %*, or no %*.
        NPM9_NATIVE.replace("%*", "%* & calc"),
        NPM9_NATIVE.replace("   %*", ""),
        // A plain batch file.
        "@echo off\r\nnode \"%~dp0\\cli.js\" %*\r\n".into(),
        String::new(),
    ];
    for c in cases {
        assert_eq!(parse(&c), None, "{c:?}");
    }
}
