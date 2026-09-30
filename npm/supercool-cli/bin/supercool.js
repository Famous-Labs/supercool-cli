#!/usr/bin/env node
// Runs the native supercool binary from the platform package npm installed
// alongside this one (the optionalDependencies pattern: no postinstall
// download, works with --ignore-scripts).
"use strict";

const { spawnSync } = require("child_process");
const path = require("path");

const pkg = `@famous-labs/supercool-cli-${process.platform}-${process.arch}`;
const exe = process.platform === "win32" ? "supercool.exe" : "supercool";

let bin;
try {
  bin = path.join(path.dirname(require.resolve(`${pkg}/package.json`)), "bin", exe);
} catch {
  console.error(
    `supercool: no build for ${process.platform}/${process.arch} was installed (${pkg}).\n` +
      "Reinstall without --no-optional / --omit=optional, or use: curl -fsSL https://supercool.com/install.sh | sh"
  );
  process.exit(1);
}

const result = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
  console.error(`supercool: couldn't start ${bin}: ${result.error.message}`);
  process.exit(1);
}
if (result.status === null) {
  // Killed before it could say anything (on macOS, SIGKILL usually means the
  // system refused the binary): never exit silently.
  console.error(
    `supercool: the binary was stopped by ${result.signal || "a signal"} (${bin}).\n` +
      "Please report this at https://github.com/Famous-Labs/supercool-cli/issues, " +
      "or install with: curl -fsSL https://supercool.com/install.sh | sh"
  );
  process.exit(1);
}
process.exit(result.status);
