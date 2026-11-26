//! npm-style command shims on Windows: the `.cmd` files npm, pnpm and Yarn put on the PATH for a
//! package's program. pluxy never runs a batch file (cmd.exe reads the arguments its own way, so
//! the ones confirmed would not be the ones run); a shim in one of the known layouts is read
//! instead, and the launch runs what it runs: the native program it names, or Node with the script
//! it names. Any other batch file is refused.
//!
//! The layouts, rendered by each tool's own shim code and checked against its test snapshots:
//! npm's `cmd-shim` 6 to 9 (npm 9 to 12), and the `@zkochan/cmd-shim` layout of pnpm, Yarn 1 and
//! Yarn 2+. A shim is read only when every non-empty line is one of its layout's lines.

/// What a shim runs, its target relative to the shim's own folder.
#[derive(Debug, PartialEq, Eq)]
pub enum Shim {
    /// The target itself, a native program.
    Native { target: String },
    /// Node (the `node.exe` beside the shim, else `node.exe` on the search path) with the
    /// arguments of the target's `#!` line, then the target script.
    Node { args: Vec<String>, target: String },
}

/// Why a batch file is not read as a shim.
pub const NOT_A_SHIM: &str = "is a batch file; pluxy runs a batch file only when it is an npm, \
pnpm or Yarn command shim it can read (cmd.exe would read the arguments its own way); name the \
program it runs as the executable override";

/// npm's `cmd-shim` header, the same in 6 to 9.
const NPM_HEAD: &[&str] = &[
    "@ECHO off",
    "GOTO start",
    ":find_dp0",
    "SET dp0=%~dp0",
    "EXIT /b",
    ":start",
    "SETLOCAL",
    "CALL :find_dp0",
];

/// npm's Node block: 9 sets PATHEXT on the last line, 6 to 8 inside the ELSE.
const NPM_NODE_9: &[&str] = &[
    "IF EXIST \"%dp0%\\node.exe\" (",
    "SET \"_prog=%dp0%\\node.exe\"",
    ") ELSE (",
    "SET \"_prog=node\"",
    ")",
];
const NPM_NODE_6: &[&str] = &[
    "IF EXIST \"%dp0%\\node.exe\" (",
    "SET \"_prog=%dp0%\\node.exe\"",
    ") ELSE (",
    "SET \"_prog=node\"",
    "SET PATHEXT=%PATHEXT:;.JS;=;%",
    ")",
];
const NPM_RUN_9: &str =
    "endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & set PATHEXT=%PATHEXT:;.JS;=;% & \"%_prog%\" ";
const NPM_RUN_6: &str = "endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & \"%_prog%\" ";

const PATHEXT: &str = "@SET PATHEXT=%PATHEXT:;.JS;=;%";

/// Reads text, a batch file's content, as a shim.
pub fn parse(text: &str) -> Option<Shim> {
    // Lines as cmd.exe sees them for these layouts: leading and trailing blanks and the line
    // break do not matter, nor how many blanks separate words; empty lines are skipped.
    let lines: Vec<String> = text
        .lines()
        .map(|l| l.split_whitespace().collect::<Vec<_>>().join(" "))
        .filter(|l| !l.is_empty())
        .collect();
    let lines: Vec<&str> = lines.iter().map(String::as_str).collect();
    npm(&lines).or_else(|| zkochan(&lines))
}

fn npm(lines: &[&str]) -> Option<Shim> {
    let rest = lines.strip_prefix(NPM_HEAD)?;
    if let [run] = rest {
        return Some(Shim::Native {
            target: run_line(run, "%dp0%", "")?,
        });
    }
    for (block, prefix) in [(NPM_NODE_9, NPM_RUN_9), (NPM_NODE_6, NPM_RUN_6)] {
        if let Some([run]) = rest.strip_prefix(block) {
            let (args, target) = node_line(run.strip_prefix(prefix)?, "%dp0%")?;
            return Some(Shim::Node { args, target });
        }
    }
    None
}

/// pnpm's, Yarn 1's and Yarn 2+'s layout: Node beside the shim, else Node on the PATH, both with
/// the same arguments and target (Yarn 1 sets up the ELSE's environment inside it).
fn zkochan(lines: &[&str]) -> Option<Shim> {
    let lines = match lines {
        ["@SETLOCAL", rest @ ..] => rest,
        rest => rest,
    };
    if let [run] = lines {
        return Some(Shim::Native {
            target: run_line(run.strip_prefix('@')?, "%~dp0", "")?,
        });
    }
    let (beside, on_path) = match lines {
        ["@IF EXIST \"%~dp0\\node.exe\" (", beside, ") ELSE (", PATHEXT, on_path, ")"]
        | ["@IF EXIST \"%~dp0\\node.exe\" (", beside, ") ELSE (", "@SETLOCAL", PATHEXT, on_path, ")"] => {
            (*beside, *on_path)
        }
        _ => return None,
    };
    let (args, target) = node_line(beside.strip_prefix("\"%~dp0\\node.exe\" ")?, "%~dp0")?;
    if node_line(on_path.strip_prefix("node ")?, "%~dp0")? != (args.clone(), target.clone()) {
        return None;
    }
    Some(Shim::Node { args, target })
}

/// The target of `"<dp0>\<target>" %*` (after prefix): a relative path with no quote, `%`, `..`,
/// drive or root.
fn run_line(line: &str, dp0: &str, prefix: &str) -> Option<String> {
    let quoted = line.strip_prefix(prefix)?.strip_suffix(" %*")?;
    let target = quoted
        .strip_prefix('"')?
        .strip_suffix('"')?
        .strip_prefix(dp0)?
        .strip_prefix('\\')?;
    let plain = !target.is_empty()
        && !target.contains(['"', '%', ':', '/'])
        && !target.starts_with('\\')
        && target
            .split('\\')
            .all(|c| !c.is_empty() && c != "." && c != "..");
    plain.then(|| target.to_string())
}

/// The `#!` arguments and target of `[<args> ]"<dp0>\<target>" %*`: arguments are plain words
/// (no quote, `%` or cmd metacharacter), as a shebang's would be.
fn node_line(line: &str, dp0: &str) -> Option<(Vec<String>, String)> {
    let at = line.find(&format!("\"{dp0}\\"))?;
    // The script starts a word of its own: cmd.exe would give Node an argument glued to it as one.
    if at > 0 && !line[..at].ends_with(' ') {
        return None;
    }
    let args: Vec<String> = line[..at].split_whitespace().map(String::from).collect();
    let word = |a: &String| {
        a.chars()
            .all(|c| c.is_ascii_alphanumeric() || "_.=:/+-,@".contains(c))
    };
    if !args.iter().all(word) {
        return None;
    }
    Some((args, run_line(&line[at..], dp0, "")?))
}

#[cfg(test)]
#[path = "shim_tests.rs"]
mod tests;
