#!/usr/bin/env node
// 把构建产物（dist/）打成带版本号的产物包：release/doclite-web-<version>.tar.gz（npm run release，先 build 再打包）。
//
// 包里有：
//   manifest.json   {name: "doclite-web", version, built_at, commit, files}，version 取 package.json
//   dist/…          vite build 的产物
//
// 桌面客户端只依赖这个包、不依赖 web 的源码：client/WEB_VERSION 记要用的版本，客户端打包时取
// web/release/doclite-web-<WEB_VERSION>.tar.gz（或 DOCLITE_WEB_ARTIFACT 指定的文件），核对清单里的版本后嵌进去
// （client/cmd/webartifact）。以后拆仓库，客户端改成从发布地址下载这个包就行。
//
// tar 自己写（ustar 格式 + gzip），不依赖系统的 tar 命令，Windows 上也一样。

import { execFileSync } from 'node:child_process'
import { mkdirSync, readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { gzipSync } from 'node:zlib'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const dist = path.join(root, 'dist')
const pkg = JSON.parse(readFileSync(path.join(root, 'package.json'), 'utf8'))
const version = String(pkg.version ?? '')
if (!/^\d+\.\d+\.\d+([-+][0-9A-Za-z.-]+)?$/.test(version)) {
  console.error(`package.json 的 version 不是 semver：${version}`)
  process.exit(1)
}

let indexStat
try {
  indexStat = statSync(path.join(dist, 'index.html'))
} catch {
  indexStat = null
}
if (!indexStat?.isFile()) {
  console.error('没有 dist/index.html：先 npm run build（npm run release 会先构建）')
  process.exit(1)
}

/** dist 下的全部文件（相对路径，/ 分隔，按名字排序）。 */
function walk(dir, prefix = '') {
  const out = []
  for (const name of readdirSync(dir).sort()) {
    const full = path.join(dir, name)
    const rel = prefix ? `${prefix}/${name}` : name
    const st = statSync(full)
    if (st.isDirectory()) out.push(...walk(full, rel))
    else if (st.isFile()) out.push(rel)
  }
  return out
}

function commit() {
  try {
    return execFileSync('git', ['rev-parse', '--short', 'HEAD'], { cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim()
  } catch {
    return null
  }
}

const files = walk(dist)
const builtAt = new Date()
const manifest = {
  name: 'doclite-web',
  version,
  built_at: builtAt.toISOString(),
  commit: commit(),
  files: files.length,
}

// ---------- ustar ----------

const mtime = Math.floor(builtAt.getTime() / 1000)

function octal(n, width) {
  return n.toString(8).padStart(width - 1, '0') + '\0'
}

function header(name, size) {
  const h = Buffer.alloc(512)
  let base = name
  let prefix = ''
  if (Buffer.byteLength(base) > 100) {
    const i = name.lastIndexOf('/', name.length - 1)
    prefix = name.slice(0, i)
    base = name.slice(i + 1)
    if (i < 0 || Buffer.byteLength(base) > 100 || Buffer.byteLength(prefix) > 155) throw new Error(`路径太长，ustar 放不下：${name}`)
  }
  h.write(base, 0, 100, 'utf8')
  h.write(octal(0o644, 8), 100, 'ascii')
  h.write(octal(0, 8), 108, 'ascii')
  h.write(octal(0, 8), 116, 'ascii')
  h.write(octal(size, 12), 124, 'ascii')
  h.write(octal(mtime, 12), 136, 'ascii')
  h.fill(' ', 148, 156)
  h.write('0', 156, 'ascii')
  h.write('ustar\0', 257, 'ascii')
  h.write('00', 263, 'ascii')
  h.write(prefix, 345, 155, 'utf8')
  let sum = 0
  for (const b of h) sum += b
  h.write(octal(sum, 7) + ' ', 148, 'ascii')
  return h
}

const parts = []
function add(name, data) {
  parts.push(header(name, data.length), data)
  const pad = (512 - (data.length % 512)) % 512
  if (pad) parts.push(Buffer.alloc(pad))
}

add('manifest.json', Buffer.from(JSON.stringify(manifest, null, 2) + '\n'))
for (const rel of files) add(`dist/${rel}`, readFileSync(path.join(dist, ...rel.split('/'))))
parts.push(Buffer.alloc(1024))

const outDir = path.join(root, 'release')
mkdirSync(outDir, { recursive: true })
const out = path.join(outDir, `doclite-web-${version}.tar.gz`)
const gz = gzipSync(Buffer.concat(parts), { level: 9 })
writeFileSync(out, gz)
console.log(`产物包：${path.relative(root, out)}（版本 ${version}，${files.length} 个文件，${(gz.length / 1024).toFixed(0)} KB）`)
