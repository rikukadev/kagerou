#!/usr/bin/env node
'use strict'

const { spawnSync } = require('node:child_process')
const { ensure } = require('./install.js')

ensure()
  .then((bin) => {
    const r = spawnSync(bin, process.argv.slice(2), { stdio: 'inherit' })
    if (r.error) {
      console.error(`kagerou: ${r.error.message}`)
      process.exit(1)
    }
    // シグナルで死んだ場合 status は null になる。
    process.exit(r.status === null ? 1 : r.status)
  })
  .catch((err) => {
    console.error(`kagerou: ${err.message}`)
    process.exit(1)
  })
