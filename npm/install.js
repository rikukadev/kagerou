'use strict'

// GitHub Releases のバイナリを取ってくるだけの薄い wrapper。
//
// npm に置くのは `npx kagerou diagnose` のため。診断は他人のリポジトリで走らせる
// ものなので、Go を入れていない JS のプロジェクトでもその場で試せる必要がある。
//
// 依存パッケージは持たない(バイナリを落として実行する側が、自前で依存を
// 引き連れてくるのは筋が悪い)。tar は macOS/Linux に元からあるものを使う。

const { createHash } = require('node:crypto')
const { execFileSync } = require('node:child_process')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')

const REPO = 'rikukadev/kagerou'
const VERSION = require('./package.json').version

// goreleaser の name_template と対応させる(.goreleaser.yml)。
const GOOS = { darwin: 'darwin', linux: 'linux' }
const GOARCH = { x64: 'amd64', arm64: 'arm64' }

// bin/ ではなく vendor/。ルートの .gitignore が bin/ を無視しており、
// wrapper ごとパッケージから消えたことがある(npm pack の中身を CI で検査している)。
const vendorDir = path.join(__dirname, 'vendor')
const binPath = path.join(vendorDir, 'kagerou')

function target() {
  const goos = GOOS[process.platform]
  const goarch = GOARCH[process.arch]
  if (!goos || !goarch) {
    throw new Error(
      `${process.platform}/${process.arch} 向けのバイナリは配布していない。` +
        `go install github.com/rikukadev/kagerou/cmd/kagerou@v${VERSION} を使う`
    )
  }
  return { goos, goarch }
}

async function fetchBuffer(url) {
  const res = await fetch(url, { redirect: 'follow' })
  if (!res.ok) throw new Error(`${url}: HTTP ${res.status}`)
  return Buffer.from(await res.arrayBuffer())
}

async function install() {
  const { goos, goarch } = target()
  const name = `kagerou_${VERSION}_${goos}_${goarch}.tar.gz`
  const base = `https://github.com/${REPO}/releases/download/v${VERSION}`

  const [tarball, sums] = await Promise.all([
    fetchBuffer(`${base}/${name}`),
    fetchBuffer(`${base}/checksums.txt`),
  ])

  // 落としたものに実行権限を付けて起動する場所なので、必ず checksums.txt と
  // 突き合わせる。ここを飛ばすと「配信経路を取られたら終わり」になる。
  const line = sums
    .toString('utf8')
    .split('\n')
    .map((l) => l.trim().split(/\s+/))
    .find(([, file]) => file === name)
  if (!line) throw new Error(`checksums.txt に ${name} が無い`)
  const got = createHash('sha256').update(tarball).digest('hex')
  if (got !== line[0]) {
    throw new Error(`checksum が一致しない: ${got} != ${line[0]}`)
  }

  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'kagerou-'))
  try {
    const archive = path.join(tmp, name)
    fs.writeFileSync(archive, tarball)
    fs.mkdirSync(vendorDir, { recursive: true })
    execFileSync('tar', ['-xzf', archive, '-C', tmp, 'kagerou'])
    fs.copyFileSync(path.join(tmp, 'kagerou'), binPath)
    fs.chmodSync(binPath, 0o755)
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true })
  }
  return binPath
}

// ensure は bin wrapper から呼ぶ。postinstall が動かない環境
// (--ignore-scripts、オフラインで後から復帰)でも実行時に拾えるようにする。
async function ensure() {
  if (fs.existsSync(binPath)) return binPath
  return install()
}

module.exports = { ensure, install, binPath, VERSION }

if (require.main === module) {
  // postinstall。ここで落ちても `npm install` 全体は壊さない。
  // 無関係なパッケージのインストールを、こちらの都合で失敗させたくない。
  const quiet = process.argv.includes('--quiet')
  install().catch((err) => {
    if (!quiet) {
      console.error(`kagerou: ${err.message}`)
      process.exit(1)
    }
    console.error(`kagerou: バイナリの取得に失敗した(初回実行時に再試行する): ${err.message}`)
  })
}
