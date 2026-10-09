// 生成跨语言签名测试向量：canonicalize 与签名逻辑逐字对齐
// desktop_app/apps/desktop/src/main/updates/update-service.ts。
// 用法：node tests/vectors/generate.mjs
import { generateKeyPairSync, sign } from "node:crypto";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

function canonicalize(value) {
  if (value === null || typeof value === "boolean" || typeof value === "number") {
    return JSON.stringify(value);
  }
  if (typeof value === "string") return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map(canonicalize).join(",")}]`;
  if (typeof value === "object") {
    return `{${Object.entries(value)
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([key, entry]) => `${JSON.stringify(key)}:${canonicalize(entry)}`)
      .join(",")}}`;
  }
  throw new Error("unsupported value");
}

const keys = generateKeyPairSync("ed25519");
const publicKeyPem = keys.publicKey.export({ format: "pem", type: "spki" }).toString();

const desktopManifest = {
  artifacts: [
    { arch: "x64", platform: "win32", sha256: "a".repeat(64), size: 1024, url: "https://app.syntropy-lab.com/blobs/" + "a".repeat(64) },
    { arch: "arm64", platform: "darwin", sha256: "b".repeat(64), size: 2048, url: "https://app.syntropy-lab.com/blobs/" + "b".repeat(64) },
  ],
  channel: "beta",
  expiresAt: "2099-07-30T00:00:00.000Z",
  killSwitch: { expiresAt: "2099-07-30T00:00:00.000Z", minimumSafeVersion: "0.3.1" },
  minimumSupportedVersion: "0.2.0",
  publishedAt: "2026-10-05T00:00:00.000Z",
  releaseNotesUrl: "https://app.syntropy-lab.com/releases/0.4.0",
  version: "0.4.0",
};

const bom = {
  channel: "stable",
  components: [
    {
      artifacts: [{ arch: "amd64", filename: "mbot-linux-amd64", os: "linux", sha256: "c".repeat(64), size: 41943040 }],
      kind: "binary",
      name: "mbot",
      version: "0.3.0",
    },
    {
      artifacts: [{ arch: "amd64", filename: "codex-linux-amd64.tgz", os: "linux", sha256: "d".repeat(64), size: 123456789 }],
      kind: "archive",
      name: "codex",
      version: "0.160.0",
    },
  ],
  expiresAt: "2099-07-30T00:00:00.000Z",
  product: "syntropy",
  productVersion: "0.3.0",
  publishedAt: "2026-10-05T00:00:00.000Z",
};

function vector(name, payload) {
  const canonical = canonicalize(payload);
  return {
    name,
    payload,
    canonical,
    signature: sign(null, Buffer.from(canonical), keys.privateKey).toString("base64"),
  };
}

const output = {
  note: "由 tests/vectors/generate.mjs 生成；canonicalize 与 update-service.ts 一致。",
  publicKeyPem,
  vectors: [vector("desktop-manifest", desktopManifest), vector("bom", bom)],
};

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
writeFileSync(path.join(scriptDir, "sign-vectors.json"), `${JSON.stringify(output, null, 2)}\n`, "utf8");
console.log("wrote", path.join(scriptDir, "sign-vectors.json"));
console.log("canonical(desktop-manifest) length", output.vectors[0].canonical.length);
