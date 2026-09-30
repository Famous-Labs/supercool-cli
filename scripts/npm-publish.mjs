#!/usr/bin/env node
// Publishes the npm packages for a release: one package per platform holding
// that platform's binary (from GoReleaser's dist/), then the wrapper whose
// optionalDependencies pin them. Run by the release workflow with npm
// trusted publishing (OIDC): no npm token is stored.
//
//   node scripts/npm-publish.mjs <version> [--dry-run]
import { execFileSync } from "node:child_process";
import { copyFileSync, chmodSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync, existsSync } from "node:fs";
import { join } from "node:path";

const version = (process.argv[2] || "").replace(/^v/, "");
const dryRun = process.argv.includes("--dry-run");
if (!/^\d+\.\d+\.\d+/.test(version)) {
  console.error("usage: npm-publish.mjs <version> [--dry-run]");
  process.exit(1);
}

// npm platform/arch -> GoReleaser's dist folder suffix.
const targets = [
  { os: "darwin", cpu: "arm64", go: "darwin_arm64" },
  { os: "darwin", cpu: "x64", go: "darwin_amd64_v1" },
  { os: "linux", cpu: "arm64", go: "linux_arm64" },
  { os: "linux", cpu: "x64", go: "linux_amd64_v1" },
  { os: "win32", cpu: "arm64", go: "windows_arm64" },
  { os: "win32", cpu: "x64", go: "windows_amd64_v1" },
];

function distDir(goSuffix) {
  // GoReleaser names folders <build-id>_<os>_<arch>[_<variant>]; match loosely.
  const want = goSuffix.replace(/_v[\d.]+$/, "");
  const hit = readdirSync("dist").find((d) => d.startsWith("supercool_") && d.replace(/_v[\d.]+$/, "").endsWith(want));
  if (!hit) throw new Error(`no dist folder for ${goSuffix}`);
  return join("dist", hit);
}

// A prerelease (1.0.0-rc.1) goes out under "next", never as "latest".
const distTag = version.includes("-") ? "next" : "latest";

function publish(dir) {
  const args = ["publish", dir, "--access", "public", "--provenance", "--tag", distTag];
  if (dryRun) args.push("--dry-run");
  execFileSync("npm", args, { stdio: "inherit" });
}

const out = "npm-out";
rmSync(out, { recursive: true, force: true });
for (const t of targets) {
  const name = `@famous-labs/supercool-cli-${t.os}-${t.cpu}`;
  const dir = join(out, `${t.os}-${t.cpu}`);
  mkdirSync(join(dir, "bin"), { recursive: true });
  const exe = t.os === "win32" ? "supercool.exe" : "supercool";
  copyFileSync(join(distDir(t.go), exe), join(dir, "bin", exe));
  chmodSync(join(dir, "bin", exe), 0o755);
  writeFileSync(
    join(dir, "package.json"),
    JSON.stringify(
      {
        name,
        version,
        description: `The supercool binary for ${t.os}-${t.cpu}. Install @famous-labs/supercool-cli instead.`,
        repository: { type: "git", url: "git+https://github.com/Famous-Labs/supercool-cli.git" },
        license: "MIT",
        os: [t.os],
        cpu: [t.cpu],
        files: ["bin"],
      },
      null,
      2
    ) + "\n"
  );
  publish(dir);
}

const wrapperDir = join(out, "supercool-cli");
mkdirSync(join(wrapperDir, "bin"), { recursive: true });
const pkg = JSON.parse(readFileSync("npm/supercool-cli/package.json", "utf8"));
pkg.version = version;
for (const dep of Object.keys(pkg.optionalDependencies)) pkg.optionalDependencies[dep] = version;
writeFileSync(join(wrapperDir, "package.json"), JSON.stringify(pkg, null, 2) + "\n");
copyFileSync("npm/supercool-cli/bin/supercool.js", join(wrapperDir, "bin", "supercool.js"));
if (existsSync("README.md")) copyFileSync("README.md", join(wrapperDir, "README.md"));
publish(wrapperDir);
