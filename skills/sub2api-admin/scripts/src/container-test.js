const fs = require("fs");
const path = require("path");
const crypto = require("crypto");
const { spawnSync } = require("child_process");

const DEFAULT_IMAGE = "node:20-bookworm";
const DEFAULT_PNPM_VERSION = "9.15.5";
const VALID_SUITES = new Set(["typecheck", "build", "lint", "critical", "all"]);
const VALID_ACTIONS = new Set(["start", "status", "logs", "wait", "clean", "list"]);

function repoRootFromScript() {
  return path.resolve(__dirname, "../../../..");
}

function stateRoot(repoRoot) {
  return path.join(repoRoot, ".state", "sub2api-admin", "container-tests");
}

function jobFile(repoRoot, jobId) {
  if (!/^[a-z0-9-]+$/.test(jobId)) throw new Error("invalid container test job id");
  return path.join(stateRoot(repoRoot), `${jobId}.json`);
}

function docker(args, { allowFailure = false } = {}) {
  const result = spawnSync("docker", args, { encoding: "utf8" });
  const stdout = (result.stdout || "").trim();
  const stderr = (result.stderr || "").trim();
  if (result.error) throw new Error(`docker command failed to start: ${result.error.message}`);
  if (result.status !== 0 && !allowFailure) {
    throw new Error(`docker ${args.join(" ")} failed (${result.status}): ${stderr || stdout || "no output"}`);
  }
  return { status: result.status, stdout, stderr };
}

function requireAbsoluteRepo(repoValue) {
  const repoRoot = path.resolve(repoValue || repoRootFromScript());
  if (!path.isAbsolute(repoRoot)) throw new Error("--repo must resolve to an absolute path");
  if (!fs.existsSync(path.join(repoRoot, "frontend", "package.json"))) {
    throw new Error(`frontend/package.json not found under repo: ${repoRoot}`);
  }
  return repoRoot;
}

function validateVersion(value, name) {
  if (!/^\d+\.\d+\.\d+$/.test(value)) throw new Error(`${name} must be a semantic version, got ${value}`);
  return value;
}

function validateImage(value) {
  if (!/^[A-Za-z0-9./:@_-]+$/.test(value)) throw new Error(`invalid Docker image: ${value}`);
  return value;
}

function suiteCommand(suite) {
  const critical = [
    "src/api/__tests__/client.spec.ts",
    "src/api/__tests__/tokenRefresh.spec.ts",
    "src/api/__tests__/channelMonitorV2.spec.ts",
    "src/views/admin/__tests__/SettingsView.spec.ts",
  ];
  switch (suite) {
    case "typecheck":
      return "pnpm typecheck";
    case "build":
      return "pnpm build";
    case "lint":
      return "pnpm lint:check";
    case "critical":
      return `pnpm exec vitest run ${critical.join(" ")}`;
    case "all":
      return `pnpm lint:check && pnpm typecheck && pnpm build && pnpm exec vitest run ${critical.join(" ")}`;
    default:
      throw new Error(`unsupported frontend test suite: ${suite}`);
  }
}

function testScript(pnpmVersion, suite) {
  return [
    "set -euo pipefail",
    "corepack enable",
    `corepack prepare pnpm@${pnpmVersion} --activate`,
    "printf 'container.node=' && node --version",
    "printf 'container.pnpm=' && pnpm --version",
    "pnpm install --frozen-lockfile",
    `printf 'container.suite=${suite}\\n'`,
    suiteCommand(suite),
  ].join("; ");
}

function readJob(repoRoot, jobId) {
  const file = jobFile(repoRoot, jobId);
  if (!fs.existsSync(file)) throw new Error(`container test job not found: ${jobId}`);
  return JSON.parse(fs.readFileSync(file, "utf8"));
}

function inspectJob(job) {
  const result = docker(["inspect", "--format", "{{json .State}}", job.containerName], { allowFailure: true });
  if (result.status !== 0) {
    return { jobId: job.jobId, status: "missing", containerName: job.containerName, detail: result.stderr || result.stdout };
  }
  const state = JSON.parse(result.stdout);
  return {
    jobId: job.jobId,
    status: state.Status,
    running: Boolean(state.Running),
    exitCode: state.ExitCode,
    startedAt: state.StartedAt,
    finishedAt: state.FinishedAt,
    containerName: job.containerName,
    suite: job.suite,
    image: job.image,
    pnpmVersion: job.pnpmVersion,
  };
}

function start(flags) {
  const repoRoot = requireAbsoluteRepo(flags.repo);
  const suite = flags.suite || "typecheck";
  if (!VALID_SUITES.has(suite)) throw new Error(`--suite must be one of ${Array.from(VALID_SUITES).join(", ")}`);
  const image = validateImage(flags.image || DEFAULT_IMAGE);
  const pnpmVersion = validateVersion(flags["pnpm-version"] || DEFAULT_PNPM_VERSION, "--pnpm-version");
  fs.mkdirSync(stateRoot(repoRoot), { recursive: true });

  const suffix = crypto.randomBytes(4).toString("hex");
  const jobId = `frontend-${Date.now()}-${suffix}`;
  const containerName = `sub2api-cli-${jobId}`;
  const volumeName = `${containerName}-node-modules`;
  const result = docker([
    "run",
    "-d",
    "--name",
    containerName,
    "--label",
    "com.pikastech.sub2api.cli=frontend-test",
    "--label",
    `com.pikastech.sub2api.job=${jobId}`,
    "-v",
    `${repoRoot}:/workspace`,
    "-v",
    `${volumeName}:/workspace/frontend/node_modules`,
    "-w",
    "/workspace/frontend",
    image,
    "bash",
    "-lc",
    testScript(pnpmVersion, suite),
  ]);
  const job = {
    jobId,
    containerName,
    volumeName,
    repoRoot,
    suite,
    image,
    pnpmVersion,
    containerId: result.stdout,
    createdAt: new Date().toISOString(),
  };
  fs.writeFileSync(jobFile(repoRoot, jobId), `${JSON.stringify(job, null, 2)}\n`, "utf8");
  return { ...job, status: "started" };
}

function logs(flags, job) {
  const tail = flags.tail || "200";
  if (!/^\d+$/.test(String(tail))) throw new Error("--tail must be a non-negative integer");
  const result = docker(["logs", "--tail", String(tail), job.containerName], { allowFailure: true });
  return {
    jobId: job.jobId,
    containerName: job.containerName,
    status: result.status === 0 ? "ok" : "missing",
    stdout: result.stdout,
    stderr: result.stderr,
  };
}

function waitFor(flags, job) {
  const timeout = flags.timeout || "120s";
  const match = String(timeout).match(/^(\d+)(ms|s|m)$/);
  if (!match || Number(match[1]) <= 0) throw new Error("--timeout must be a positive integer followed by ms, s, or m");
  const milliseconds = Number(match[1]) * ({ ms: 1, s: 1000, m: 60000 }[match[2]]);
  if (milliseconds > 3600000) throw new Error("--timeout must not exceed 60m");
  const result = spawnSync("timeout", ["--signal=TERM", `${Math.ceil(milliseconds / 1000)}s`, "docker", "wait", job.containerName], { encoding: "utf8" });
  if (result.error) throw new Error(`wait command failed: ${result.error.message}`);
  if (result.status === 124) return { ...inspectJob(job), timedOut: true, timeout };
  if (result.status !== 0) throw new Error(`docker wait failed (${result.status}): ${(result.stderr || result.stdout || "no output").trim()}`);
  return { ...inspectJob(job), timedOut: false, timeout, waitExitCode: Number((result.stdout || "").trim()) };
}

function clean(repoRoot, job) {
  const result = docker(["rm", "-f", job.containerName], { allowFailure: true });
  const volume = docker(["volume", "rm", job.volumeName], { allowFailure: true });
  fs.rmSync(jobFile(repoRoot, job.jobId), { force: true });
  return {
    jobId: job.jobId,
    containerRemoved: result.status === 0,
    volumeRemoved: volume.status === 0,
  };
}

function list(repoRoot) {
  const root = stateRoot(repoRoot);
  if (!fs.existsSync(root)) return { jobs: [] };
  const jobs = fs.readdirSync(root).filter((name) => name.endsWith(".json")).map((name) => {
    const job = JSON.parse(fs.readFileSync(path.join(root, name), "utf8"));
    return inspectJob(job);
  });
  return { jobs };
}

async function run(args) {
  const action = args.positional[2] || "start";
  if (!VALID_ACTIONS.has(action)) throw new Error(`unknown dev test-frontend action: ${action}`);
  const repoRoot = requireAbsoluteRepo(args.flags.repo);
  if (action === "start") return start(args.flags);
  if (action === "list") return list(repoRoot);
  const job = readJob(repoRoot, args.positional[3]);
  if (action === "status") return inspectJob(job);
  if (action === "logs") return logs(args.flags, job);
  if (action === "wait") return waitFor(args.flags, job);
  return clean(repoRoot, job);
}

module.exports = { run };
